package core

import (
	"encoding/json"
	"strings"
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

// TestAgentResult_ToolCalls_RoundTripLargePayload pins the contract that
// ToolCalls() returns the untruncated JSON the LLM produced, even when the
// agent loop truncated Input for UI/log display. Regression test for silent
// mid-string EOFs callers hit when json.Unmarshal'ing Args from large tool
// calls.
func TestAgentResult_ToolCalls_RoundTripLargePayload(t *testing.T) {
	// Build a payload that overflows the 200-rune Input cap so the display
	// string would be unparseable JSON on its own.
	type payload struct {
		Query string `json:"query"`
		Pad   string `json:"pad"`
	}
	want := payload{Query: "find things", Pad: strings.Repeat("x", 1024)}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	r := AgentResult{
		Steps: []StepTrace{
			{
				Name:     "search",
				Type:     "tool",
				Input:    string(raw[:200]), // mimics TruncateStr(input, 200)
				Output:   "{}",
				RawArgs:  json.RawMessage(raw),
				Duration: time.Millisecond,
			},
		},
	}

	calls := r.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(calls))
	}
	var got payload
	if err := json.Unmarshal(calls[0].Args, &got); err != nil {
		t.Fatalf("Unmarshal ToolCalls[0].Args: %v (Args=%q)", err, string(calls[0].Args))
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, want)
	}
}

// TestAgentResult_ToolResults_RoundTripLargePayload mirrors the ToolCalls
// regression test for ToolResults — Output is bounded for display, but
// callers reading ToolResults().Content must get the original JSON intact.
func TestAgentResult_ToolResults_RoundTripLargePayload(t *testing.T) {
	type payload struct {
		Body string `json:"body"`
		Pad  string `json:"pad"`
	}
	want := payload{Body: "ok", Pad: strings.Repeat("y", 2048)}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	r := AgentResult{
		Steps: []StepTrace{
			{
				Name:      "fetch",
				Type:      "tool",
				Input:     "{}",
				Output:    string(raw[:500]), // mimics TruncateStr(output, 500)
				RawOutput: json.RawMessage(raw),
				Duration:  time.Millisecond,
			},
		},
	}

	results := r.ToolResults()
	if len(results) != 1 {
		t.Fatalf("ToolResults len = %d, want 1", len(results))
	}
	var got payload
	if err := json.Unmarshal(results[0].Content, &got); err != nil {
		t.Fatalf("Unmarshal ToolResults[0].Content: %v (Content=%q)", err, string(results[0].Content))
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, want)
	}
}

// TestAgentResult_ToolCalls_FallbackToInputWhenRawNil keeps backward-compat
// for trace objects constructed externally (workflow.exec, custom callers)
// that don't populate RawArgs/RawOutput.
func TestAgentResult_ToolCalls_FallbackToInputWhenRawNil(t *testing.T) {
	r := AgentResult{
		Steps: []StepTrace{
			{Name: "legacy", Type: "tool", Input: `{"q":"hi"}`, Output: `{"hits":1}`},
		},
	}
	calls := r.ToolCalls()
	if len(calls) != 1 || string(calls[0].Args) != `{"q":"hi"}` {
		t.Errorf("fallback to Input failed: %+v", calls)
	}
	results := r.ToolResults()
	if len(results) != 1 || string(results[0].Content) != `{"hits":1}` {
		t.Errorf("fallback to Output failed: %+v", results)
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
