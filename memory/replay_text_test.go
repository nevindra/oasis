// memory/replay_text_test.go
package memory

import (
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

// A turn with interleaved narration replays each text segment as the Content
// of the FOLLOWING tool-call message — the wire shape the model originally
// produced — closing with the final content.
func TestExpandHistory_TextStepsAttachToNextToolCall(t *testing.T) {
	history := []core.Message{
		{Role: core.RoleUser, Content: "make a deck"},
		{Role: core.RoleAssistant, Content: "done, deck is ready", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "text", Type: core.StepTypeText, Output: "let me search first", RawOutput: "let me search first"},
			{Name: "web_search", Type: core.StepTypeTool, Output: "digest",
				RawOutput: "full results"},
			{Name: "text", Type: core.StepTypeText, Output: "got data, now writing", RawOutput: "got data, now writing"},
			{Name: "edit_file", Type: core.StepTypeTool, Output: "saved", RawOutput: "saved index.html"},
		})},
	}
	out := expandHistory(history, 2, 0, nil)
	// user, call(+narration), result, call(+narration), result, final
	if len(out) != 6 {
		t.Fatalf("len = %d, want 6: %+v", len(out), out)
	}
	if out[1].Content != "let me search first" || len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].Name != "web_search" {
		t.Fatalf("first narration should ride on the web_search call: %+v", out[1])
	}
	if out[3].Content != "got data, now writing" || len(out[3].ToolCalls) != 1 || out[3].ToolCalls[0].Name != "edit_file" {
		t.Fatalf("second narration should ride on the edit_file call: %+v", out[3])
	}
	if out[5].Content != "done, deck is ready" || len(out[5].ToolCalls) != 0 {
		t.Fatalf("final text wrong: %+v", out[5])
	}
	// No standalone bare-text assistant messages mid-turn — a generated
	// bare-text message would terminate the loop.
	for i, m := range out[1:5] {
		if m.Role == core.RoleAssistant && len(m.ToolCalls) == 0 {
			t.Fatalf("bare-text assistant message mid-turn at %d: %+v", i+1, m)
		}
	}
}

// A trailing narration segment with no following tool call must not duplicate
// the row content (hook-stopped turns persist the same text in both places).
func TestExpandHistory_TrailingTextNotDuplicated(t *testing.T) {
	history := []core.Message{
		{Role: core.RoleAssistant, Content: "same final text", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "render_options", Type: core.StepTypeTool, Output: "ok", RawOutput: "ok"},
			{Name: "text", Type: core.StepTypeText, Output: "same final text", RawOutput: "same final text"},
		})},
	}
	out := expandHistory(history, 2, 0, nil)
	count := 0
	for _, m := range out {
		if m.Content == "same final text" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("final text appears %d times, want 1: %+v", count, out)
	}
}

// A trailing narration segment on a turn WITHOUT row content still replays
// (as the closing assistant text) rather than being dropped.
func TestExpandHistory_TrailingTextWithoutFinalContent(t *testing.T) {
	history := []core.Message{
		{Role: core.RoleAssistant, Content: "", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "web_search", Type: core.StepTypeTool, Output: "digest", RawOutput: "full"},
			{Name: "text", Type: core.StepTypeText, Output: "interrupted narration", RawOutput: "interrupted narration"},
		})},
	}
	out := expandHistory(history, 2, 0, nil)
	last := out[len(out)-1]
	if last.Content != "interrupted narration" {
		t.Fatalf("trailing narration dropped: %+v", out)
	}
}

// Old (digested) turns replay the narration's 500-char display digest;
// verbatim turns replay the full text.
func TestExpandHistory_TextStepDigestOnOldTurns(t *testing.T) {
	longText := strings.Repeat("n", 600)
	history := []core.Message{
		{Role: core.RoleAssistant, Content: "old final", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "text", Type: core.StepTypeText, Output: longText[:500], RawOutput: longText},
			{Name: "web_search", Type: core.StepTypeTool, Output: "digest", RawOutput: "full body"},
		})},
		{Role: core.RoleAssistant, Content: "recent final", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "text", Type: core.StepTypeText, Output: longText[:500], RawOutput: longText},
			{Name: "web_search", Type: core.StepTypeTool, Output: "digest", RawOutput: "full body"},
		})},
	}
	out := expandHistory(history, 1, 0, nil) // only the last turn verbatim
	if len(out[0].Content) != 500 {
		t.Fatalf("old turn's narration should be the 500-char digest, got len %d", len(out[0].Content))
	}
	if len(out[0].ToolCalls) != 1 {
		t.Fatalf("narration should ride on the tool call: %+v", out[0])
	}
	if out[1].Content != "digest" {
		t.Fatalf("old tool output = %q, want digest", out[1].Content)
	}
	// Recent turn: full narration.
	if out[3].Content != longText {
		t.Fatalf("recent turn's narration should be verbatim, got len %d", len(out[3].Content))
	}
}

// Protected tools don't charge the verbatim budget — they replay full
// regardless of the window, so charging them would shrink the window
// without saving anything.
func TestExpandHistory_ProtectedToolsFreeOfBudget(t *testing.T) {
	protected := []string{"skill_activate"}
	history := []core.Message{
		{Role: core.RoleAssistant, Content: "t1", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "tool", Type: core.StepTypeTool, Output: "digest-t1", RawOutput: strings.Repeat("a", 1000)},
		})},
		{Role: core.RoleAssistant, Content: "t2", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "skill_activate", Type: core.StepTypeTool, Output: "digest-skill", RawOutput: strings.Repeat("s", 100_000)},
		})},
		{Role: core.RoleAssistant, Content: "t3", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "tool", Type: core.StepTypeTool, Output: "digest-t3", RawOutput: strings.Repeat("c", 1000)},
		})},
	}
	// Floor 1 (t3, cost 1000, budget 2500→1500); t2's only step is protected
	// (cost 0, stays in window); t1 (1000 ≤ 1500) also verbatim.
	out := expandHistory(history, 1, 2500, protected)
	// Each turn expands to [call, result, final]: t1=out[0..2], t2=out[3..5], t3=out[6..8].
	if len(out[1].Content) != 1000 {
		t.Fatalf("t1 should be verbatim (protected skill must not consume budget), got len %d", len(out[1].Content))
	}
	if len(out[4].Content) != 100_000 {
		t.Fatalf("protected skill output should replay full, got len %d", len(out[4].Content))
	}
}

// The budget window extends verbatim replay beyond the floor while raw
// outputs fit, and degrades contiguously once exhausted.
func TestExpandHistory_VerbatimBudgetWindow(t *testing.T) {
	turn := func(final, raw string) core.Message {
		return core.Message{Role: core.RoleAssistant, Content: final, Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "tool", Type: core.StepTypeTool, Output: "digest-" + final, RawOutput: raw},
		})}
	}
	history := []core.Message{
		turn("t1", strings.Repeat("a", 1000)), // oldest
		turn("t2", strings.Repeat("b", 1000)),
		turn("t3", strings.Repeat("c", 1000)),
		turn("t4", strings.Repeat("d", 1000)), // newest
	}

	// Floor 1, budget 2500: t4 (floor, cost 1000, budget→1500), t3 (1000 ≤
	// 1500, budget→500), t2 (1000 > 500 → stop). t1, t2 digest.
	out := expandHistory(history, 1, 2500, nil)
	byTurn := map[string]string{}
	for i := 0; i < len(out); i += 3 { // call, result, final per turn
		byTurn[out[i+2].Content] = out[i+1].Content
	}
	if !strings.HasPrefix(byTurn["t4"], "d") || len(byTurn["t4"]) != 1000 {
		t.Fatalf("t4 (floor) should be verbatim, got len %d", len(byTurn["t4"]))
	}
	if !strings.HasPrefix(byTurn["t3"], "c") || len(byTurn["t3"]) != 1000 {
		t.Fatalf("t3 (in budget) should be verbatim, got %q", byTurn["t3"][:20])
	}
	if byTurn["t2"] != "digest-t2" {
		t.Fatalf("t2 (over budget) should digest, got %q", byTurn["t2"][:20])
	}
	if byTurn["t1"] != "digest-t1" {
		t.Fatalf("t1 (over budget) should digest, got %q", byTurn["t1"][:20])
	}

	// Zero budget: floor-only (old behavior).
	out = expandHistory(history, 2, 0, nil)
	byTurn = map[string]string{}
	for i := 0; i < len(out); i += 3 {
		byTurn[out[i+2].Content] = out[i+1].Content
	}
	if len(byTurn["t4"]) != 1000 || len(byTurn["t3"]) != 1000 {
		t.Fatalf("floor turns should be verbatim: t4=%d t3=%d", len(byTurn["t4"]), len(byTurn["t3"]))
	}
	if byTurn["t2"] != "digest-t2" {
		t.Fatalf("t2 beyond floor should digest with zero budget, got %q", byTurn["t2"][:20])
	}
}
