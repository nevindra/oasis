package core

import (
	"testing"
	"time"
)

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

// Verify ToolCallTrace alias is interchangeable with StepTrace.
func TestToolCallTraceAlias(t *testing.T) {
	var tct ToolCallTrace = StepTrace{Name: "x"}
	if tct.Name != "x" {
		t.Errorf("alias broken")
	}
	var st StepTrace = ToolCallTrace{Name: "y"}
	if st.Name != "y" {
		t.Errorf("alias broken")
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
