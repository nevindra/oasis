package eval

import (
	"context"
	"regexp"
	"testing"

	"github.com/nevindra/oasis/core"
)

func scoreOf(t *testing.T, s core.Scorer, run core.ScorerRun) core.Score {
	t.Helper()
	got, err := s.Score(context.Background(), run)
	if err != nil {
		t.Fatalf("%s.Score: %v", s.ID(), err)
	}
	if got.ScorerID != s.ID() {
		t.Fatalf("ScorerID = %q, want %q", got.ScorerID, s.ID())
	}
	return got
}

func TestExactMatch(t *testing.T) {
	if v := scoreOf(t, ExactMatch(), core.ScorerRun{Output: "hi", GroundTruth: "hi"}).Value; v != 1 {
		t.Fatalf("match: %v", v)
	}
	if v := scoreOf(t, ExactMatch(), core.ScorerRun{Output: "hi", GroundTruth: "bye"}).Value; v != 0 {
		t.Fatalf("mismatch: %v", v)
	}
}

func TestContains(t *testing.T) {
	if v := scoreOf(t, Contains(), core.ScorerRun{Output: "the quick fox", GroundTruth: "quick"}).Value; v != 1 {
		t.Fatalf("present: %v", v)
	}
	if v := scoreOf(t, Contains(), core.ScorerRun{Output: "the quick fox", GroundTruth: "slow"}).Value; v != 0 {
		t.Fatalf("absent: %v", v)
	}
}

func TestRegexMatch(t *testing.T) {
	re := regexp.MustCompile(`\d{3}-\d{4}`)
	if v := scoreOf(t, RegexMatch(re), core.ScorerRun{Output: "call 555-1234"}).Value; v != 1 {
		t.Fatalf("match: %v", v)
	}
	if v := scoreOf(t, RegexMatch(re), core.ScorerRun{Output: "no number"}).Value; v != 0 {
		t.Fatalf("nomatch: %v", v)
	}
}

func TestKeywordCoverage(t *testing.T) {
	s := KeywordCoverage("alpha", "beta", "gamma", "delta")
	if v := scoreOf(t, s, core.ScorerRun{Output: "Alpha and BETA only"}).Value; v != 0.5 {
		t.Fatalf("coverage: %v (want 0.5)", v)
	}
}

func TestContentSimilarity(t *testing.T) {
	// Identical token sets → 1.0
	if v := scoreOf(t, ContentSimilarity(), core.ScorerRun{Output: "a b c", GroundTruth: "c b a"}).Value; v != 1 {
		t.Fatalf("identical: %v", v)
	}
	// Disjoint → 0.0
	if v := scoreOf(t, ContentSimilarity(), core.ScorerRun{Output: "a b", GroundTruth: "x y"}).Value; v != 0 {
		t.Fatalf("disjoint: %v", v)
	}
}

func TestCompleteness(t *testing.T) {
	s := Completeness("intro", "body", "conclusion")
	v := scoreOf(t, s, core.ScorerRun{Output: "has intro and body"}).Value
	if v < 0.66 || v > 0.67 {
		t.Fatalf("completeness: %v (want ~0.667)", v)
	}
}

func TestToolCallAccuracy(t *testing.T) {
	run := core.ScorerRun{Steps: []core.StepTrace{
		{Name: "search", Type: core.StepTypeTool},
		{Name: "summarize", Type: core.StepTypeTool},
		{Name: "ignored_agent", Type: core.StepTypeAgent},
	}}
	s := ToolCallAccuracy(core.ExpectedStep{Name: "search"}, core.ExpectedStep{Name: "missing"})
	if v := scoreOf(t, s, run).Value; v != 0.5 {
		t.Fatalf("tool acc: %v (want 0.5)", v)
	}
}

func TestTrajectory(t *testing.T) {
	run := core.ScorerRun{Steps: []core.StepTrace{
		{Name: "a", Type: core.StepTypeTool},
		{Name: "b", Type: core.StepTypeTool},
		{Name: "c", Type: core.StepTypeTool},
	}}
	exact := Trajectory(core.ExpectedTrajectory{
		Steps:    []core.ExpectedStep{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		Strategy: core.ExactMatch,
	})
	if v := scoreOf(t, exact, run).Value; v != 1 {
		t.Fatalf("exact: %v", v)
	}
	ordered := Trajectory(core.ExpectedTrajectory{
		Steps:    []core.ExpectedStep{{Name: "a"}, {Name: "c"}},
		Strategy: core.OrderedSubset,
	})
	if v := scoreOf(t, ordered, run).Value; v != 1 {
		t.Fatalf("ordered subset: %v", v)
	}
	unordered := Trajectory(core.ExpectedTrajectory{
		Steps:    []core.ExpectedStep{{Name: "c"}, {Name: "z"}},
		Strategy: core.UnorderedSubset,
	})
	if v := scoreOf(t, unordered, run).Value; v != 0.5 {
		t.Fatalf("unordered subset: %v (want 0.5)", v)
	}
}
