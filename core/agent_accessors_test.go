package core

import (
	"testing"
	"time"
)

func sampleResult() AgentResult {
	return AgentResult{
		Output:   "final text",
		Thinking: "reasoning",
		Usage:    Usage{InputTokens: 10, OutputTokens: 5},
		Steps: []StepTrace{
			{
				Name:     "search",
				Type:     "tool",
				Input:    `{"q":"hi"}`,
				Output:   `{"hits":1}`,
				Duration: 2 * time.Millisecond,
				Usage:    Usage{InputTokens: 2, OutputTokens: 1},
			},
			{
				Name:     "fetch",
				Type:     "tool",
				Input:    `{"url":"x"}`,
				Output:   `{"body":"y"}`,
				Duration: 3 * time.Millisecond,
				Usage:    Usage{InputTokens: 3, OutputTokens: 2},
			},
		},
	}
}

func TestAgentResult_Text(t *testing.T) {
	r := sampleResult()
	if got, want := r.Text(), "final text"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestAgentResult_Reasoning(t *testing.T) {
	r := sampleResult()
	if got, want := r.Reasoning(), "reasoning"; got != want {
		t.Errorf("Reasoning() = %q, want %q", got, want)
	}
}

func TestAgentResult_ToolCalls(t *testing.T) {
	r := sampleResult()
	calls := r.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("ToolCalls() len = %d, want 2", len(calls))
	}
	if calls[0].Name != "search" {
		t.Errorf("ToolCalls()[0].Name = %q, want %q", calls[0].Name, "search")
	}
	if calls[1].Name != "fetch" {
		t.Errorf("ToolCalls()[1].Name = %q, want %q", calls[1].Name, "fetch")
	}
}

func TestAgentResult_ToolResults(t *testing.T) {
	r := sampleResult()
	results := r.ToolResults()
	if len(results) != 2 {
		t.Fatalf("ToolResults() len = %d, want 2", len(results))
	}
	if string(results[0].Content) != `{"hits":1}` {
		t.Errorf("ToolResults()[0].Content = %s, want %s", string(results[0].Content), `{"hits":1}`)
	}
	if string(results[1].Content) != `{"body":"y"}` {
		t.Errorf("ToolResults()[1].Content = %s, want %s", string(results[1].Content), `{"body":"y"}`)
	}
}

func TestAgentResult_LastStep(t *testing.T) {
	r := sampleResult()
	last := r.LastStep()
	if last.Name != "fetch" {
		t.Errorf("LastStep().Name = %q, want %q", last.Name, "fetch")
	}

	empty := AgentResult{}
	if zero := empty.LastStep(); zero.Name != "" {
		t.Errorf("LastStep() on empty result should be zero value, got %+v", zero)
	}
}

func TestAgentResult_StepByTool(t *testing.T) {
	r := sampleResult()
	step, ok := r.StepByTool("fetch")
	if !ok || step.Name != "fetch" {
		t.Errorf("StepByTool(fetch) = (%+v, %v)", step, ok)
	}

	_, ok = r.StepByTool("nope")
	if ok {
		t.Errorf("StepByTool(nope) should return ok=false")
	}
}
