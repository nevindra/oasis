package core

import (
	"encoding/json"
	"testing"
	"time"
)

// TestToolNameConstants_Frozen pins the wire-format values the LLM and the
// dispatch path agree on. A rename is a breaking protocol change — this test
// catches accidental edits before they ship.
func TestToolNameConstants_Frozen(t *testing.T) {
	cases := map[string]string{
		"ToolPrefixAgent": ToolPrefixAgent,
		"ToolAskUser":     ToolAskUser,
		"ToolExecutePlan": ToolExecutePlan,
		"ToolSpawnAgent":  ToolSpawnAgent,
	}
	want := map[string]string{
		"ToolPrefixAgent": "agent_",
		"ToolAskUser":     "ask_user",
		"ToolExecutePlan": "execute_plan",
		"ToolSpawnAgent":  "spawn_agent",
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("%s = %q, want %q (renaming changes the wire format)", name, got, want[name])
		}
	}
}

func TestIterationTraceShape(t *testing.T) {
	it := IterationTrace{
		Iter:      0,
		Model:     "gpt-4o",
		StartedAt: time.Now(),
		Duration:  100 * time.Millisecond,
		LLMCall: LLMCallTrace{
			Duration:     90 * time.Millisecond,
			InputTokens:  100,
			OutputTokens: 50,
			FinishReason: FinishStop,
		},
		ToolCalls: nil,
		Usage:     Usage{InputTokens: 100, OutputTokens: 50},
	}
	if it.LLMCall.FinishReason != FinishStop {
		t.Errorf("LLMCall.FinishReason not preserved")
	}
}

func TestAgentResultNewFields(t *testing.T) {
	r := AgentResult{
		FinishReason:   FinishStop,
		Warnings:       []string{"x"},
		ProviderMeta:   []byte(`{"a":1}`),
		SuspendPayload: []byte(`{"q":"more info?"}`),
		Object:         []byte(`{"title":"x"}`),
	}
	if r.FinishReason != FinishStop {
		t.Errorf("FinishReason lost")
	}
	if len(r.Warnings) != 1 {
		t.Errorf("Warnings lost")
	}
	// Sources, Files, Iterations also exist; default to nil/empty.
	if r.Sources != nil || r.Files != nil || r.Iterations != nil {
		t.Errorf("expected nil slices on fresh struct, got %+v", r)
	}
}

// TestAgentResult_JSONWireShape pins the frozen snake_case wire format for the
// fields that previously marshaled as CamelCase (Output, Thinking, Attachments,
// Usage, Steps) and round-trips the whole struct. A field that loses or renames
// its json tag breaks this test before it ships.
func TestAgentResult_JSONWireShape(t *testing.T) {
	r := AgentResult{
		Output:       "hi",
		Thinking:     "reasoning",
		Attachments:  []Attachment{{MimeType: "image/png", Data: []byte("x")}},
		Usage:        Usage{InputTokens: 10, OutputTokens: 5},
		Steps:        []StepTrace{{Name: "web_search", Type: StepTypeTool}},
		FinishReason: FinishStop,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	// The previously-untagged fields must now appear as snake_case keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"output", "thinking", "attachments", "usage", "steps"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing snake_case key %q in wire shape: %s", key, b)
		}
	}
	// CamelCase keys must be gone (the wire-shape break is intentional and pinned).
	for _, key := range []string{"Output", "Thinking", "Attachments", "Usage", "Steps"} {
		if _, ok := raw[key]; ok {
			t.Errorf("unexpected CamelCase key %q still present: %s", key, b)
		}
	}

	var back AgentResult
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Output != r.Output || back.Thinking != r.Thinking {
		t.Errorf("round-trip mismatch: got %+v", back)
	}
	if len(back.Attachments) != 1 || back.Attachments[0].MimeType != "image/png" {
		t.Errorf("Attachments lost in round-trip: %+v", back.Attachments)
	}
	if back.Usage != r.Usage {
		t.Errorf("Usage lost in round-trip: %+v", back.Usage)
	}
	if len(back.Steps) != 1 || back.Steps[0].Name != "web_search" {
		t.Errorf("Steps lost in round-trip: %+v", back.Steps)
	}

	// Empty result: omitempty fields drop out, required fields stay.
	empty, err := json.Marshal(AgentResult{})
	if err != nil {
		t.Fatal(err)
	}
	var emptyRaw map[string]json.RawMessage
	if err := json.Unmarshal(empty, &emptyRaw); err != nil {
		t.Fatal(err)
	}
	if _, ok := emptyRaw["attachments"]; ok {
		t.Errorf("attachments should be omitempty on empty result: %s", empty)
	}
	if _, ok := emptyRaw["steps"]; ok {
		t.Errorf("steps should be omitempty on empty result: %s", empty)
	}
	if _, ok := emptyRaw["output"]; !ok {
		t.Errorf("output (no omitempty) should always be present: %s", empty)
	}
}

// TestAgentTask_JSONWireShape pins AgentTask's snake_case wire format.
func TestAgentTask_JSONWireShape(t *testing.T) {
	task := AgentTask{
		Input:       "do the thing",
		Attachments: []Attachment{{MimeType: "text/plain"}},
		ThreadID:    "t1",
		UserID:      "u1",
		ChatID:      "c1",
		Extra:       map[string]any{"k": "v"},
	}
	b, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"input", "attachments", "thread_id", "user_id", "chat_id", "extra"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing snake_case key %q in wire shape: %s", key, b)
		}
	}
	for _, key := range []string{"Input", "ThreadID", "UserID", "ChatID", "Extra"} {
		if _, ok := raw[key]; ok {
			t.Errorf("unexpected CamelCase key %q still present: %s", key, b)
		}
	}

	var back AgentTask
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Input != task.Input || back.ThreadID != task.ThreadID ||
		back.UserID != task.UserID || back.ChatID != task.ChatID {
		t.Errorf("round-trip mismatch: got %+v", back)
	}

	// Empty task: only input (no omitempty) survives.
	empty, err := json.Marshal(AgentTask{})
	if err != nil {
		t.Fatal(err)
	}
	var emptyRaw map[string]json.RawMessage
	if err := json.Unmarshal(empty, &emptyRaw); err != nil {
		t.Fatal(err)
	}
	if _, ok := emptyRaw["thread_id"]; ok {
		t.Errorf("thread_id should be omitempty: %s", empty)
	}
	if _, ok := emptyRaw["input"]; !ok {
		t.Errorf("input (no omitempty) should always be present: %s", empty)
	}
}
