package memory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

func stepsMeta(t *testing.T, steps []core.StepTrace) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(map[string]any{"steps": steps})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A plain history (no steps metadata) must pass through unchanged.
func TestExpandHistory_PlainMessagesPassThrough(t *testing.T) {
	history := []core.Message{
		{Role: core.RoleUser, Content: "hi"},
		{Role: core.RoleAssistant, Content: "hello"},
	}
	out := expandHistory(history, 2, nil)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Content != "hi" || out[1].Content != "hello" {
		t.Fatalf("content mismatch: %+v", out)
	}
}

// A recent assistant turn with steps expands into paired tool_call/tool_result
// messages (full RawOutput) followed by the final text.
func TestExpandHistory_RecentTurnReplaysVerbatim(t *testing.T) {
	history := []core.Message{
		{Role: core.RoleUser, Content: "make a deck"},
		{Role: core.RoleAssistant, Content: "here is the plan", Metadata: stepsMeta(t, []core.StepTrace{
			{Name: "web_search", Type: core.StepTypeTool, Input: "q", Output: "digest",
				RawArgs: json.RawMessage(`{"query":"climate"}`), RawOutput: "full search results body"},
		})},
	}
	out := expandHistory(history, 2, nil)
	// user, assistant(tool_call), tool, assistant(text)
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4: %+v", len(out), out)
	}
	call := out[1]
	if call.Role != core.RoleAssistant || len(call.ToolCalls) != 1 {
		t.Fatalf("expected assistant tool-call message, got %+v", call)
	}
	if call.ToolCalls[0].Name != "web_search" || string(call.ToolCalls[0].Args) != `{"query":"climate"}` {
		t.Fatalf("tool call mismatch: %+v", call.ToolCalls[0])
	}
	result := out[2]
	if result.ToolCallID != call.ToolCalls[0].ID {
		t.Fatalf("tool result not paired: call id %q vs result id %q", call.ToolCalls[0].ID, result.ToolCallID)
	}
	if result.Content != "full search results body" {
		t.Fatalf("recent turn must replay RawOutput, got %q", result.Content)
	}
	if out[3].Content != "here is the plan" {
		t.Fatalf("final text missing: %+v", out[3])
	}
}

// Old turns replay the bounded display digest, not the full raw output.
func TestExpandHistory_OldTurnReplaysDigest(t *testing.T) {
	old := core.Message{Role: core.RoleAssistant, Content: "done", Metadata: stepsMeta(t, []core.StepTrace{
		{Name: "web_search", Type: core.StepTypeTool, Output: "digest only",
			RawArgs: json.RawMessage(`{}`), RawOutput: "huge full body"},
	})}
	history := []core.Message{
		old,
		{Role: core.RoleUser, Content: "next"},
		{Role: core.RoleAssistant, Content: "recent answer"},
	}
	out := expandHistory(history, 1, nil) // only the LAST assistant turn is verbatim
	var toolResult *core.ChatMessage
	for i := range out {
		if out[i].ToolCallID != "" {
			toolResult = &out[i]
		}
	}
	if toolResult == nil {
		t.Fatal("no tool result replayed")
	}
	if toolResult.Content != "digest only" {
		t.Fatalf("old turn must replay the digest, got %q", toolResult.Content)
	}
}

// Protected tools always replay in full, regardless of age — the skill
// activation case: instructions must survive the whole thread.
func TestExpandHistory_ProtectedToolAlwaysVerbatim(t *testing.T) {
	skillBody := "# Skill: deck-building\nfull instructions..."
	old := core.Message{Role: core.RoleAssistant, Content: "", Metadata: stepsMeta(t, []core.StepTrace{
		{Name: "skill_activate", Type: core.StepTypeTool, Output: "truncated…",
			RawArgs: json.RawMessage(`{"name":"deck-building"}`), RawOutput: skillBody},
	})}
	history := []core.Message{
		old,
		{Role: core.RoleUser, Content: "next"},
		{Role: core.RoleAssistant, Content: "recent answer"},
	}
	out := expandHistory(history, 1, []string{"skill_activate"})
	found := false
	for _, m := range out {
		if m.ToolCallID != "" && m.Content == skillBody {
			found = true
		}
	}
	if !found {
		t.Fatalf("protected tool output must replay in full; messages: %+v", out)
	}
}

// A step with no surviving output replays the cleared-content placeholder,
// and agent-delegation steps get their "agent_" prefix restored.
func TestExpandHistory_PlaceholderAndAgentPrefix(t *testing.T) {
	msg := core.Message{Role: core.RoleAssistant, Content: "ok", Metadata: stepsMeta(t, []core.StepTrace{
		{Name: "Batur", Type: core.StepTypeAgent, Input: "hitung 35jt x 12"},
	})}
	out := expandHistory([]core.Message{msg}, 0, nil)
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(out), out)
	}
	if out[0].ToolCalls[0].Name != "agent_Batur" {
		t.Fatalf("agent step must restore prefix, got %q", out[0].ToolCalls[0].Name)
	}
	if !strings.Contains(string(out[0].ToolCalls[0].Args), "hitung 35jt x 12") {
		t.Fatalf("fallback args must wrap the display input: %s", out[0].ToolCalls[0].Args)
	}
	if out[1].Content != prunedToolOutputPlaceholder {
		t.Fatalf("empty output must replay placeholder, got %q", out[1].Content)
	}
}

// Malformed metadata must never break replay — fall back to plain text.
func TestExpandHistory_MalformedMetadataFallsBack(t *testing.T) {
	msg := core.Message{Role: core.RoleAssistant, Content: "answer", Metadata: json.RawMessage(`{not json`)}
	out := expandHistory([]core.Message{msg}, 2, nil)
	if len(out) != 1 || out[0].Content != "answer" {
		t.Fatalf("malformed metadata must fall back to plain message: %+v", out)
	}
}

// TrimToBudget must preserve Metadata on surviving rows (replay depends on it).
func TestTrimToBudget_PreservesMetadata(t *testing.T) {
	meta := stepsMeta(t, []core.StepTrace{{Name: "greet", Type: core.StepTypeTool, Output: "hi"}})
	long := strings.Repeat("x", 4000)
	in := &RetrieveContext{History: []core.Message{
		{Role: core.RoleUser, Content: long},                           // will be trimmed away
		{Role: core.RoleAssistant, Content: "keep", Metadata: meta},    // must survive WITH metadata
		{Role: core.RoleUser, Content: "recent question"},              // must survive
	}}
	if err := (TrimToBudget{Budget: 60}).Process(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if len(in.History) != 2 {
		t.Fatalf("expected 2 surviving rows, got %d", len(in.History))
	}
	if in.History[0].Content != "keep" || len(in.History[0].Metadata) == 0 {
		t.Fatalf("surviving row lost its metadata: %+v", in.History[0])
	}
}
