package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

// Task 3.1 — FinishReason set on natural stop.
func TestAgentResultFinishReasonNaturalStop(t *testing.T) {
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{Content: "done", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider)
	result, err := a.Execute(context.Background(), AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinishReason != core.FinishStop {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, core.FinishStop)
	}
}

// Task 3.2 — SuspendPayload is a skip placeholder per plan instructions.
func TestAgentResultSuspendPayload(t *testing.T) {
	// Build a provider whose first reply triggers a suspend-via-tool.
	// (Reuse existing suspend test harness; or stub a tool that returns
	// (zero, agent.Suspend(payload)).)
	t.Skip("flesh out using suspend_test.go patterns during execution")
}

// Task 3.3 — Warnings and ProviderMeta carried from the last LLM call.
func TestAgentResultWarningsAndProviderMeta(t *testing.T) {
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{
			Content:      "ok",
			FinishReason: core.FinishStop,
			Warnings:     []string{"fallback-model-used"},
			ProviderMeta: json.RawMessage(`{"safety":"ok"}`),
		}, nil
	})
	a := NewLLMAgent("t", "test", provider)
	result, err := a.Execute(context.Background(), AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "fallback-model-used" {
		t.Errorf("Warnings = %v, want [fallback-model-used]", result.Warnings)
	}
	if string(result.ProviderMeta) != `{"safety":"ok"}` {
		t.Errorf("ProviderMeta = %s, want {\"safety\":\"ok\"}", result.ProviderMeta)
	}
}

// Task 3.3 — Warnings accumulate across multiple iterations.
func TestAgentResultWarningsAccumulateAcrossIterations(t *testing.T) {
	iter := 0
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		i := iter
		iter++
		if i == 0 {
			// First iteration: tool call + a warning.
			return core.ChatResponse{
				ToolCalls:    []core.ToolCall{{ID: "1", Name: "noop", Args: []byte(`{}`)}},
				FinishReason: core.FinishToolCalls,
				Warnings:     []string{"warn-iter-0"},
			}, nil
		}
		// Second iteration: final response + another warning.
		return core.ChatResponse{
			Content:      "done",
			FinishReason: core.FinishStop,
			Warnings:     []string{"warn-iter-1"},
		}, nil
	})
	noop := newFnTool("noop", func(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
		return core.ToolResult{Content: []byte(`"ok"`)}, nil
	})
	a := NewLLMAgent("t", "test", provider, WithTools(noop))
	result, err := a.Execute(context.Background(), AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Warnings) != 2 {
		t.Errorf("Warnings = %v, want 2 entries", result.Warnings)
	}
}

// Task 4.3 — Iterations populated per LLM call.
func TestAgentResultIterationsPopulated(t *testing.T) {
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		close(ch)
		return core.ChatResponse{
			Content:      "ok",
			Usage:        core.Usage{InputTokens: 10, OutputTokens: 5},
			FinishReason: core.FinishStop,
		}, nil
	})
	a := NewLLMAgent("t", "test", provider)
	result, _ := a.Execute(context.Background(), AgentTask{Input: "x"})
	if len(result.Iterations) != 1 {
		t.Fatalf("Iterations len = %d, want 1", len(result.Iterations))
	}
	it := result.Iterations[0]
	if it.Iter != 0 {
		t.Errorf("Iter = %d, want 0", it.Iter)
	}
	if it.LLMCall.InputTokens != 10 || it.LLMCall.OutputTokens != 5 {
		t.Errorf("LLMCall = %+v, want InputTokens=10, OutputTokens=5", it.LLMCall)
	}
	if it.LLMCall.FinishReason != core.FinishStop {
		t.Errorf("LLMCall.FinishReason = %q, want %q", it.LLMCall.FinishReason, core.FinishStop)
	}
}

// Task 3.4 — Files aggregated from EventFileAttachment events.
func TestAgentResultFilesAggregated(t *testing.T) {
	// Stub a provider that emits an EventFileAttachment event through the
	// intermediate channel before returning its terminal response.
	provider := newFnProvider(func(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
		// Emit a file-attachment event into the provider's channel.
		// The forwarder will deliver it to the main stream channel.
		ch <- core.StreamEvent{
			Type:    core.EventFileAttachment,
			Name:    "report.pdf",
			Content: `{"name":"report.pdf","mime_type":"application/pdf","size":1234}`,
		}
		close(ch)
		return core.ChatResponse{Content: "done", FinishReason: core.FinishStop}, nil
	})
	a := NewLLMAgent("t", "test", provider)

	// Use ExecuteStream so the event flows through the full channel path.
	ch := make(chan core.StreamEvent, 64)
	result, err := a.ExecuteStream(context.Background(), AgentTask{Input: "x"}, ch)
	// Drain the channel so the goroutine doesn't block.
	for range ch {
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Errorf("Files = %d, want 1; files = %+v", len(result.Files), result.Files)
	}
}
