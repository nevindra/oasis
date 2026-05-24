package core

import (
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
