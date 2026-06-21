package eval

import (
	"context"
	"regexp"
	"strings"

	"github.com/nevindra/oasis/core"
)

// --- exact_match ---

type exactMatch struct{}

// ExactMatch scores 1 when Output equals GroundTruth (trimmed), else 0.
func ExactMatch() core.Scorer { return exactMatch{} }
func (exactMatch) ID() string { return "exact_match" }
func (exactMatch) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	v := 0.0
	if strings.TrimSpace(run.Output) == strings.TrimSpace(run.GroundTruth) {
		v = 1
	}
	return core.Score{ScorerID: "exact_match", Value: v}, nil
}

// --- contains ---

type containsScorer struct{}

// Contains scores 1 when GroundTruth is a non-empty substring of Output, else 0.
func Contains() core.Scorer       { return containsScorer{} }
func (containsScorer) ID() string { return "contains" }
func (containsScorer) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	v := 0.0
	if run.GroundTruth != "" && strings.Contains(run.Output, run.GroundTruth) {
		v = 1
	}
	return core.Score{ScorerID: "contains", Value: v}, nil
}

// --- regex_match ---

type regexMatch struct{ re *regexp.Regexp }

// RegexMatch scores 1 when re matches Output, else 0. Compile re with
// regexp.MustCompile at the call site so a bad pattern fails fast.
func RegexMatch(re *regexp.Regexp) core.Scorer { return regexMatch{re: re} }
func (regexMatch) ID() string                  { return "regex_match" }
func (r regexMatch) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	v := 0.0
	if r.re != nil && r.re.MatchString(run.Output) {
		v = 1
	}
	return core.Score{ScorerID: "regex_match", Value: v}, nil
}

// --- keyword_coverage ---

type keywordCoverage struct{ keywords []string }

// KeywordCoverage scores the fraction of keywords present in Output
// (case-insensitive). No keywords → 1.
func KeywordCoverage(keywords ...string) core.Scorer { return keywordCoverage{keywords: keywords} }
func (keywordCoverage) ID() string                   { return "keyword_coverage" }
func (k keywordCoverage) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	return core.Score{ScorerID: "keyword_coverage", Value: fractionPresent(run.Output, k.keywords)}, nil
}

// --- completeness ---

type completeness struct{ elements []string }

// Completeness scores the fraction of required elements present in Output
// (case-insensitive). No elements → 1.
func Completeness(elements ...string) core.Scorer { return completeness{elements: elements} }
func (completeness) ID() string                   { return "completeness" }
func (c completeness) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	return core.Score{ScorerID: "completeness", Value: fractionPresent(run.Output, c.elements)}, nil
}

// --- content_similarity ---

type contentSimilarity struct{}

// ContentSimilarity scores the token-set Jaccard overlap between Output and
// GroundTruth — cheap and allocation-light (no edit-distance matrix).
func ContentSimilarity() core.Scorer { return contentSimilarity{} }
func (contentSimilarity) ID() string { return "content_similarity" }
func (contentSimilarity) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	return core.Score{ScorerID: "content_similarity", Value: jaccard(run.Output, run.GroundTruth)}, nil
}

// --- tool_call_accuracy (deterministic) ---

type toolCallAccuracy struct{ expected []core.ExpectedStep }

// ToolCallAccuracy scores the fraction of expected tool calls (by name) that
// appear among the run's actual tool calls. No expectations → 1.
func ToolCallAccuracy(expected ...core.ExpectedStep) core.Scorer {
	return toolCallAccuracy{expected: expected}
}
func (toolCallAccuracy) ID() string { return "tool_call_accuracy" }
func (t toolCallAccuracy) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	if len(t.expected) == 0 {
		return core.Score{ScorerID: "tool_call_accuracy", Value: 1}, nil
	}
	actual := toolNames(run.Steps)
	hit := 0
	for _, e := range t.expected {
		if sliceHas(actual, e.Name) {
			hit++
		}
	}
	return core.Score{ScorerID: "tool_call_accuracy", Value: float64(hit) / float64(len(t.expected))}, nil
}

// --- trajectory (deterministic) ---

type trajectory struct{ expected core.ExpectedTrajectory }

// Trajectory scores the run's tool-call sequence against an expected one using
// the expected trajectory's Strategy. ExactMatch / OrderedSubset return 1 or 0;
// UnorderedSubset returns the fraction of expected steps present. LLMJudgeMatch
// falls back to the unordered fraction here (use TrajectoryLLM for true LLM
// matching). No expected steps → 1.
func Trajectory(expected core.ExpectedTrajectory) core.Scorer { return trajectory{expected: expected} }
func (trajectory) ID() string                                 { return "trajectory" }
func (t trajectory) Score(_ context.Context, run core.ScorerRun) (core.Score, error) {
	exp := t.expected.Steps
	if len(exp) == 0 {
		return core.Score{ScorerID: "trajectory", Value: 1}, nil
	}
	actual := toolNames(run.Steps)
	var v float64
	switch t.expected.Strategy {
	case core.ExactMatch:
		v = boolScore(exactSeq(actual, exp))
	case core.OrderedSubset:
		v = boolScore(orderedSubset(actual, exp))
	default: // UnorderedSubset and LLMJudgeMatch (fallback)
		hit := 0
		for _, e := range exp {
			if sliceHas(actual, e.Name) {
				hit++
			}
		}
		v = float64(hit) / float64(len(exp))
	}
	return core.Score{ScorerID: "trajectory", Value: v}, nil
}

// --- shared helpers ---

func fractionPresent(output string, needles []string) float64 {
	if len(needles) == 0 {
		return 1
	}
	out := strings.ToLower(output)
	hit := 0
	for _, n := range needles {
		if strings.Contains(out, strings.ToLower(n)) {
			hit++
		}
	}
	return float64(hit) / float64(len(needles))
}

func jaccard(a, b string) float64 {
	sa, sb := tokenSet(a), tokenSet(b)
	if len(sa) == 0 && len(sb) == 0 {
		return 1
	}
	if len(sa) == 0 || len(sb) == 0 {
		return 0
	}
	inter := 0
	for tok := range sa {
		if _, ok := sb[tok]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(sa)+len(sb)-inter)
}

func tokenSet(s string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, tok := range strings.Fields(strings.ToLower(s)) {
		m[tok] = struct{}{}
	}
	return m
}

func sliceHas(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func boolScore(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func exactSeq(actual []string, exp []core.ExpectedStep) bool {
	if len(actual) != len(exp) {
		return false
	}
	for i := range exp {
		if actual[i] != exp[i].Name {
			return false
		}
	}
	return true
}

func orderedSubset(actual []string, exp []core.ExpectedStep) bool {
	i := 0
	for _, a := range actual {
		if i < len(exp) && a == exp[i].Name {
			i++
		}
	}
	return i == len(exp)
}
