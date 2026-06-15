package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis/core"
)

// judgeResult is the structured output every LLM judge requests from the model.
type judgeResult struct {
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// judgeSchema is derived once at package init; all judges share it.
var judgeSchema = core.DeriveSchema[judgeResult]()

// maxJudgeField bounds each rendered field in the judge prompt to keep token
// cost and memory bounded on large outputs.
const maxJudgeField = 8000

// LLMJudge is the base for all built-in LLM-as-judge scorers. The provider is
// injected explicitly and reused across calls. It satisfies core.AsyncScorer so
// that under ScoreModeAuto the runtime runs it off the hot path.
type LLMJudge struct {
	id           string
	provider     core.Provider
	instructions string // criterion-specific system prompt
}

func newJudge(id, instructions string, provider core.Provider) *LLMJudge {
	return &LLMJudge{id: id, provider: provider, instructions: instructions}
}

func (j *LLMJudge) ID() string         { return j.id }
func (j *LLMJudge) PrefersAsync() bool { return true }

func (j *LLMJudge) Score(ctx context.Context, run core.ScorerRun) (core.Score, error) {
	req := core.ChatRequest{
		Messages: []core.ChatMessage{
			core.SystemMessage(j.instructions),
			core.UserMessage(buildJudgeInput(run)),
		},
		ResponseSchema: &core.ResponseSchema{Name: "score", Schema: judgeSchema},
	}
	resp, err := core.Chat(ctx, j.provider, req)
	if err != nil {
		return core.Score{}, fmt.Errorf("eval: judge %q: %w", j.id, err)
	}
	var jr judgeResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Content)), &jr); err != nil {
		return core.Score{}, fmt.Errorf("eval: judge %q: parse response %q: %w", j.id, resp.Content, err)
	}
	return core.Score{ScorerID: j.id, Value: clamp01(jr.Score), Reason: jr.Reason}, nil
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// buildJudgeInput renders a ScorerRun into the user message every judge sees.
// Only non-empty sections are included; each field is truncated to maxJudgeField.
func buildJudgeInput(run core.ScorerRun) string {
	var b strings.Builder
	b.WriteString("[INPUT]\n")
	b.WriteString(truncateField(run.Input))
	b.WriteString("\n\n[OUTPUT]\n")
	b.WriteString(truncateField(run.Output))
	if run.GroundTruth != "" {
		b.WriteString("\n\n[REFERENCE ANSWER]\n")
		b.WriteString(truncateField(run.GroundTruth))
	}
	if len(run.Context) > 0 {
		b.WriteString("\n\n[CONTEXT]")
		for _, c := range run.Context {
			b.WriteString("\n- ")
			b.WriteString(truncateField(c))
		}
	}
	if steps := toolSteps(run.Steps); len(steps) > 0 {
		b.WriteString("\n\n[TOOL CALLS]")
		for _, s := range steps {
			b.WriteString("\n- ")
			b.WriteString(s.Name)
			if s.Input != "" {
				b.WriteString("(")
				b.WriteString(truncateTo(s.Input, 500))
				b.WriteString(")")
			}
		}
	}
	if run.Expected != nil && len(run.Expected.Steps) > 0 {
		b.WriteString("\n\n[EXPECTED TOOL CALLS]")
		for _, e := range run.Expected.Steps {
			b.WriteString("\n- ")
			b.WriteString(e.Name)
		}
	}
	return b.String()
}

func truncateField(s string) string { return truncateTo(s, maxJudgeField) }

func truncateTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}

// toolSteps returns only the tool-call steps from a trace, in order. Shared by
// the LLM judges and the deterministic tool-call / trajectory scorers.
func toolSteps(steps []core.StepTrace) []core.StepTrace {
	var out []core.StepTrace
	for _, s := range steps {
		if s.Type == core.StepTypeTool {
			out = append(out, s)
		}
	}
	return out
}

// toolNames returns the names of the tool-call steps, in order.
func toolNames(steps []core.StepTrace) []string {
	ts := toolSteps(steps)
	out := make([]string, 0, len(ts))
	for _, s := range ts {
		out = append(out, s.Name)
	}
	return out
}
