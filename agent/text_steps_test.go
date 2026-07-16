// agent/text_steps_test.go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

type echoTool struct{}

func (echoTool) Name() string { return "echo" }
func (echoTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "echo", Description: "echo", Parameters: []byte(`{"type":"object"}`)}
}
func (echoTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	return core.ToolResult{Content: "echoed"}, nil
}

// TestLoop_RecordsInterleavedTextSteps: narration accompanying a tool-call
// iteration must land in AgentResult.Steps as a text step, in order, BEFORE
// the tool step it preceded — that ordering is what history replay reproduces.
func TestLoop_RecordsInterleavedTextSteps(t *testing.T) {
	provider := &scriptedProvider{
		responses: []core.ChatResponse{
			{Content: "let me check that", ToolCalls: []core.ToolCall{{ID: "1", Name: "echo", Args: []byte(`{}`)}}},
			{Content: "the final answer"},
		},
	}
	a := New("a", "d", provider, WithTools(echoTool{}))

	result, err := a.Execute(context.Background(), AgentTask{Input: "q"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "the final answer" {
		t.Fatalf("Output = %q", result.Output)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("Steps = %d, want 2 (text + tool): %+v", len(result.Steps), result.Steps)
	}
	if result.Steps[0].Type != core.StepTypeText || result.Steps[0].RawOutput != "let me check that" {
		t.Fatalf("first step should be the narration text step: %+v", result.Steps[0])
	}
	if result.Steps[1].Type != core.StepTypeTool || result.Steps[1].Name != "echo" {
		t.Fatalf("second step should be the tool step: %+v", result.Steps[1])
	}
}

// agentEchoTool is a delegation-named tool (agent_ prefix) that flips the
// loop into agent-tools mode, where live streaming is disabled (useStream=false).
type agentEchoTool struct{}

func (agentEchoTool) Name() string { return "agent_echo" }
func (agentEchoTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "agent_echo", Description: "echo", Parameters: []byte(`{"type":"object"}`)}
}
func (agentEchoTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	return core.ToolResult{Content: "echoed"}, nil
}

// streamingScriptedProvider mirrors scriptedProvider but streams each
// response's Content as one EventTextDelta before returning — the shape a
// real provider produces on a live streaming call.
type streamingScriptedProvider struct {
	scriptedProvider
}

func (p *streamingScriptedProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	p.mu.Lock()
	var content string
	if p.idx < len(p.responses) {
		content = p.responses[p.idx].Content
	}
	p.mu.Unlock()
	if ch != nil && content != "" {
		select {
		case ch <- core.StreamEvent{Type: core.EventTextDelta, Content: content}:
		case <-ctx.Done():
		}
	}
	return p.scriptedProvider.ChatStream(ctx, req, ch)
}

// TestLoop_AgentToolsStreamNarrationBeforeToolCalls pins two behaviors:
// (1) registering agent_* delegation tools no longer disables live streaming
// — the router's narration reaches the channel as provider text deltas; and
// (2) the narration arrives BEFORE that iteration's tool_call_start, the
// ordering consumers rebuild transcripts from.
func TestLoop_AgentToolsStreamNarrationBeforeToolCalls(t *testing.T) {
	provider := &streamingScriptedProvider{scriptedProvider{
		responses: []core.ChatResponse{
			{Content: "let me delegate", ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_echo", Args: []byte(`{}`)}}},
			{Content: "final"},
		},
	}}
	a := New("a", "d", provider, WithTools(agentEchoTool{}))

	ch := make(chan core.StreamEvent, 64)
	result, err := a.Execute(context.Background(), AgentTask{Input: "q"}, core.WithStream(ch))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "final" {
		t.Fatalf("Output = %q", result.Output)
	}

	narrIdx, toolIdx := -1, -1
	i := 0
	for ev := range ch {
		switch {
		case ev.Type == core.EventTextDelta && ev.Content == "let me delegate":
			narrIdx = i
		case ev.Type == core.EventToolCallStart && toolIdx == -1:
			toolIdx = i
		}
		i++
	}
	if narrIdx == -1 {
		t.Fatal("narration never reached the stream despite agent tools being registered")
	}
	if toolIdx == -1 {
		t.Fatal("no tool_call_start event observed")
	}
	if narrIdx > toolIdx {
		t.Fatalf("narration arrived AFTER tool_call_start (narr=%d, tool=%d)", narrIdx, toolIdx)
	}
}

// stampTestAgent is a minimal core.Agent that emits one anonymous text
// delta on its stream and returns.
type stampTestAgent struct{}

func (stampTestAgent) Name() string        { return "stub" }
func (stampTestAgent) Description() string { return "stub" }
func (stampTestAgent) Execute(ctx context.Context, _ core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	cfg := core.ApplyRunOptions(opts...)
	if cfg.Stream != nil {
		select {
		case cfg.Stream <- core.StreamEvent{Type: core.EventTextDelta, Content: "child says hi"}:
		case <-ctx.Done():
		}
		close(cfg.Stream)
	}
	return core.AgentResult{Output: "child says hi"}, nil
}

// TestForwardSubagentStream_StampsChildDeltas: a delegated subagent's
// anonymous text deltas must arrive stamped with the agent name, so consumers
// can tell child narration apart from the (now also streaming) parent's own.
func TestForwardSubagentStream_StampsChildDeltas(t *testing.T) {
	ch := make(chan core.StreamEvent, 8)
	result, err := ExecuteAgent(context.Background(), stampTestAgent{}, "researcher", core.AgentTask{Input: "t"}, ch, nil)
	if err != nil {
		t.Fatalf("ExecuteAgent: %v", err)
	}
	if result.Output != "child says hi" {
		t.Fatalf("Output = %q", result.Output)
	}
	close(ch)
	found := false
	for ev := range ch {
		if ev.Type == core.EventTextDelta && ev.Content == "child says hi" {
			found = true
			if ev.Name != "researcher" {
				t.Fatalf("child delta not stamped: Name = %q, want researcher", ev.Name)
			}
		}
	}
	if !found {
		t.Fatal("child text delta was not forwarded")
	}
}

// TestLoop_NoTextStepWithoutNarration: a tool-call iteration with empty
// content must not synthesize an empty text step.
func TestLoop_NoTextStepWithoutNarration(t *testing.T) {
	provider := &scriptedProvider{
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "echo", Args: []byte(`{}`)}}},
			{Content: "done"},
		},
	}
	a := New("a", "d", provider, WithTools(echoTool{}))

	result, err := a.Execute(context.Background(), AgentTask{Input: "q"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, st := range result.Steps {
		if st.Type == core.StepTypeText {
			t.Fatalf("unexpected text step: %+v", st)
		}
	}
}
