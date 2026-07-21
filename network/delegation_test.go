package network

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

// ctxStubAgent is a stubAgent whose fn also receives the execution context,
// for timeout/cancellation tests.
type ctxStubAgent struct {
	name string
	desc string
	fn   func(context.Context, agent.AgentTask) (agent.AgentResult, error)
}

func (s *ctxStubAgent) Name() string        { return s.name }
func (s *ctxStubAgent) Description() string { return s.desc }
func (s *ctxStubAgent) Execute(ctx context.Context, task agent.AgentTask, opts ...core.RunOption) (agent.AgentResult, error) {
	rcfg := core.ApplyRunOptions(opts...)
	if rcfg.Stream != nil {
		close(rcfg.Stream)
	}
	return s.fn(ctx, task)
}

func delegationCall(id, agentName, task string) core.ToolCall {
	args, _ := json.Marshal(map[string]string{"task": task})
	return core.ToolCall{ID: id, Name: "agent_" + agentName, Args: args}
}

// TestDuplicateDelegationReplaysCompletedResult: the router delegates the same
// (agent, task) in two successive iterations. The child must execute once; the
// second tool result replays the cached output with a "do not delegate again"
// note.
func TestDuplicateDelegationReplaysCompletedResult(t *testing.T) {
	var execs atomic.Int32
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(agent.AgentTask) (agent.AgentResult, error) {
			execs.Add(1)
			return agent.AgentResult{Output: "worker report"}, nil
		},
	}

	var secondResult string
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			// Capture the tool result of the second delegation when it appears.
			for _, m := range req.Messages {
				if m.Role == "tool" && m.ToolCallID == "2" {
					secondResult = m.Content
				}
			}
			switch countAssistantToolTurns(req) {
			case 0:
				return core.ChatResponse{ToolCalls: []core.ToolCall{delegationCall("1", "worker", "do the thing")}}
			case 1:
				return core.ChatResponse{ToolCalls: []core.ToolCall{delegationCall("2", "worker", "do the thing")}}
			default:
				return core.ChatResponse{Content: "final"}
			}
		},
	}

	net := New("net", "test", router, WithChildren(sub))
	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}); err != nil {
		t.Fatal(err)
	}

	if got := execs.Load(); got != 1 {
		t.Errorf("child executed %d times, want 1", got)
	}
	if !strings.Contains(secondResult, "already completed this exact task") {
		t.Errorf("second delegation result = %q, want replay note", secondResult)
	}
	if !strings.Contains(secondResult, "worker report") {
		t.Errorf("second delegation result = %q, want cached output included", secondResult)
	}
}

// countAssistantToolTurns counts assistant messages carrying tool calls —
// a proxy for "which iteration is this" in router callbacks.
func countAssistantToolTurns(req core.ChatRequest) int {
	n := 0
	for _, m := range req.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			n++
		}
	}
	return n
}

// TestDuplicateDelegationSameWaveRunsChildOnce: two identical delegations in
// ONE assistant message must not run the child twice — one executes, the other
// is rejected (in-flight) or replayed (already done), depending on scheduling.
func TestDuplicateDelegationSameWaveRunsChildOnce(t *testing.T) {
	var execs atomic.Int32
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(agent.AgentTask) (agent.AgentResult, error) {
			execs.Add(1)
			time.Sleep(20 * time.Millisecond) // widen the in-flight window
			return agent.AgentResult{Output: "report"}, nil
		},
	}

	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			if countAssistantToolTurns(req) == 0 {
				return core.ChatResponse{ToolCalls: []core.ToolCall{
					delegationCall("1", "worker", "same task"),
					delegationCall("2", "worker", "same task"),
				}}
			}
			return core.ChatResponse{Content: "final"}
		},
	}

	net := New("net", "test", router, WithChildren(sub))
	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}); err != nil {
		t.Fatal(err)
	}
	if got := execs.Load(); got != 1 {
		t.Errorf("child executed %d times, want 1", got)
	}
}

// TestFailedDelegationCanBeRetried: a failed delegation is evicted from the
// ledger, so retrying the same task re-executes the child.
func TestFailedDelegationCanBeRetried(t *testing.T) {
	var execs atomic.Int32
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(agent.AgentTask) (agent.AgentResult, error) {
			if execs.Add(1) == 1 {
				return agent.AgentResult{}, errors.New("boom")
			}
			return agent.AgentResult{Output: "recovered"}, nil
		},
	}

	var results []string
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			for _, m := range req.Messages {
				if m.Role == "tool" {
					results = append(results, m.Content)
				}
			}
			switch countAssistantToolTurns(req) {
			case 0:
				return core.ChatResponse{ToolCalls: []core.ToolCall{delegationCall("1", "worker", "risky task")}}
			case 1:
				return core.ChatResponse{ToolCalls: []core.ToolCall{delegationCall("2", "worker", "risky task")}}
			default:
				return core.ChatResponse{Content: "final"}
			}
		},
	}

	net := New("net", "test", router, WithChildren(sub))
	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}); err != nil {
		t.Fatal(err)
	}
	if got := execs.Load(); got != 2 {
		t.Errorf("child executed %d times, want 2 (retry after failure)", got)
	}
}

// TestChildTimeoutFailsDelegationOnly: WithChildTimeout bounds one delegation;
// the run itself completes and the router sees an "error: ... timed out" result.
func TestChildTimeoutFailsDelegationOnly(t *testing.T) {
	sub := &ctxStubAgent{
		name: "slow",
		desc: "Sleeps forever",
		fn: func(ctx context.Context, _ agent.AgentTask) (agent.AgentResult, error) {
			<-ctx.Done()
			return agent.AgentResult{}, ctx.Err()
		},
	}

	var toolResult string
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			for _, m := range req.Messages {
				if m.Role == "tool" {
					toolResult = m.Content
				}
			}
			if countAssistantToolTurns(req) == 0 {
				return core.ChatResponse{ToolCalls: []core.ToolCall{delegationCall("1", "slow", "never finishes")}}
			}
			return core.ChatResponse{Content: "final"}
		},
	}

	net := New("net", "test", router,
		WithChildren(sub),
		WithChildTimeout(30*time.Millisecond),
	)
	result, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "final" {
		t.Errorf("run output = %q, want %q", result.Output, "final")
	}
	if !strings.HasPrefix(toolResult, "error:") || !strings.Contains(toolResult, "timed out") {
		t.Errorf("tool result = %q, want error mentioning timeout", toolResult)
	}
}

// TestAgentFinishEventCarriesError: a failed delegation emits an agent-finish
// stream event with IsError=true and the error text in Content, so consumers
// can render failed status without parsing the router's answer.
func TestAgentFinishEventCarriesError(t *testing.T) {
	sub := &stubAgent{
		name: "worker",
		desc: "Fails",
		fn: func(agent.AgentTask) (agent.AgentResult, error) {
			return agent.AgentResult{}, errors.New("kaput")
		},
	}

	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			if countAssistantToolTurns(req) == 0 {
				return core.ChatResponse{ToolCalls: []core.ToolCall{delegationCall("1", "worker", "will fail")}}
			}
			return core.ChatResponse{Content: "final"}
		},
	}

	net := New("net", "test", router, WithChildren(sub))

	ch := make(chan core.StreamEvent, 128)
	done := make(chan struct{})
	var finish *core.StreamEvent
	go func() {
		defer close(done)
		for ev := range ch {
			if ev.Type == core.EventAgentFinish {
				evCopy := ev
				finish = &evCopy
			}
		}
	}()
	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}, core.WithStream(ch)); err != nil {
		t.Fatal(err)
	}
	<-done

	if finish == nil {
		t.Fatal("no agent-finish event emitted")
	}
	if !finish.IsError {
		t.Error("agent-finish IsError = false, want true")
	}
	if !strings.HasPrefix(finish.Content, "error:") || !strings.Contains(finish.Content, "kaput") {
		t.Errorf("agent-finish content = %q, want error text", finish.Content)
	}
}
