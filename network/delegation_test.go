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

// streamingStubAgent emits events into its stream before returning, to test
// how child events are forwarded into the parent's channel.
type streamingStubAgent struct {
	name string
	desc string
	evs  []core.StreamEvent
}

func (s *streamingStubAgent) Name() string        { return s.name }
func (s *streamingStubAgent) Description() string { return s.desc }
func (s *streamingStubAgent) Execute(_ context.Context, _ agent.AgentTask, opts ...core.RunOption) (agent.AgentResult, error) {
	rcfg := core.ApplyRunOptions(opts...)
	if rcfg.Stream != nil {
		for _, ev := range s.evs {
			rcfg.Stream <- ev
		}
		close(rcfg.Stream)
	}
	return agent.AgentResult{Output: "done"}, nil
}

// TestForwardedChildEventsCarryAgentStamp: every event a child emits during a
// delegation (tool calls included) reaches the parent stream with Agent set
// to the child's name, so consumers can separate child activity from the
// router's own transcript.
func TestForwardedChildEventsCarryAgentStamp(t *testing.T) {
	sub := &streamingStubAgent{
		name: "worker",
		desc: "Emits events",
		evs: []core.StreamEvent{
			{Type: core.EventToolCallStart, ID: "t1", Name: "shell", Args: json.RawMessage(`{}`)},
			{Type: core.EventToolCallResult, ID: "t1", Name: "shell", Content: "ok"},
			{Type: core.EventTextDelta, Content: "child text"},
		},
	}

	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			if countAssistantToolTurns(req) == 0 {
				return core.ChatResponse{ToolCalls: []core.ToolCall{delegationCall("1", "worker", "emit stuff")}}
			}
			return core.ChatResponse{Content: "final"}
		},
	}

	net := New("net", "test", router, WithChildren(sub))

	ch := make(chan core.StreamEvent, 128)
	var collected []core.StreamEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			collected = append(collected, ev)
		}
	}()
	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}, core.WithStream(ch)); err != nil {
		t.Fatal(err)
	}
	<-done

	var childTool, childText, routerOwn bool
	for _, ev := range collected {
		if ev.Type == core.EventToolCallStart && ev.Name == "shell" {
			if ev.Agent != "worker" {
				t.Errorf("child tool event Agent = %q, want %q", ev.Agent, "worker")
			}
			childTool = true
		}
		if ev.Type == core.EventTextDelta && ev.Content == "child text" {
			if ev.Agent != "worker" {
				t.Errorf("child text event Agent = %q, want %q", ev.Agent, "worker")
			}
			childText = true
		}
		// The network's own agent-start/finish events must NOT be stamped —
		// they are the parent's delegation markers, not child activity.
		if ev.Type == core.EventAgentStart || ev.Type == core.EventAgentFinish {
			if ev.Agent != "" {
				t.Errorf("%s event Agent = %q, want empty", ev.Type, ev.Agent)
			}
			routerOwn = true
		}
	}
	if !childTool || !childText || !routerOwn {
		t.Fatalf("missing expected events: tool=%v text=%v own=%v (got %d events)", childTool, childText, routerOwn, len(collected))
	}
}

// TestNetworkRouterSelfClone: a router with WithSelfClone fans out copies of
// ITSELF (not children) — two spawn_subagent calls in one message run two
// router clones concurrently, named <network>-N.
func TestNetworkRouterSelfClone(t *testing.T) {
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			last := req.Messages[len(req.Messages)-1]
			if strings.HasPrefix(last.Content, "PART:") {
				return core.ChatResponse{Content: "clone did " + last.Content}
			}
			if countAssistantToolTurns(req) == 0 {
				argsA, _ := json.Marshal(map[string]string{"task": "PART:A"})
				argsB, _ := json.Marshal(map[string]string{"task": "PART:B"})
				return core.ChatResponse{ToolCalls: []core.ToolCall{
					{ID: "1", Name: core.ToolSelfClone, Args: argsA},
					{ID: "2", Name: core.ToolSelfClone, Args: argsB},
				}}
			}
			return core.ChatResponse{Content: "router merged"}
		},
	}

	sub := &stubAgent{name: "worker", desc: "unused child", fn: func(agent.AgentTask) (agent.AgentResult, error) {
		return agent.AgentResult{Output: "child"}, nil
	}}

	net := New("team", "router with clones", router,
		WithChildren(sub),
		WithAgentOptions(agent.WithSelfClone(4, time.Minute)),
	)

	ch := make(chan core.StreamEvent, 256)
	var starts []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			if ev.Type == core.EventAgentStart {
				starts = append(starts, ev.Name)
			}
		}
	}()

	result, err := net.Execute(context.Background(), agent.AgentTask{Input: "split"}, core.WithStream(ch))
	if err != nil {
		t.Fatal(err)
	}
	<-done

	if result.Output != "router merged" {
		t.Errorf("output = %q, want router merged", result.Output)
	}
	if len(starts) != 2 {
		t.Fatalf("agent starts = %v, want 2 clone starts", starts)
	}
	for _, s := range starts {
		if !strings.HasPrefix(s, "team-") {
			t.Errorf("clone name %q, want team-N", s)
		}
	}
}

// TestUnifiedTaskTool: the roster is advertised as ONE task tool (no agent_*
// defs), and task calls route by subagent — to a child, to "self" (router
// clone), and unknown targets error with the valid list.
func TestUnifiedTaskTool(t *testing.T) {
	var childExecs atomic.Int32
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			childExecs.Add(1)
			return agent.AgentResult{Output: "child report: " + task.Input}, nil
		},
	}

	var advertised []string
	var results []string
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			last := req.Messages[len(req.Messages)-1]
			if strings.HasPrefix(last.Content, "SOLO:") {
				return core.ChatResponse{Content: "clone did " + last.Content}
			}
			for _, m := range req.Messages {
				if m.Role == "tool" {
					results = append(results, m.Content)
				}
			}
			if countAssistantToolTurns(req) == 0 {
				advertised = nil
				for _, tl := range req.Tools {
					advertised = append(advertised, tl.Name)
				}
				a1, _ := json.Marshal(map[string]string{"subagent": "worker", "task": "part one"})
				a2, _ := json.Marshal(map[string]string{"subagent": "self", "task": "SOLO:part two"})
				a3, _ := json.Marshal(map[string]string{"subagent": "ghost", "task": "part three"})
				return core.ChatResponse{ToolCalls: []core.ToolCall{
					{ID: "1", Name: core.ToolTask, Args: a1},
					{ID: "2", Name: core.ToolTask, Args: a2},
					{ID: "3", Name: core.ToolTask, Args: a3},
				}}
			}
			return core.ChatResponse{Content: "merged"}
		},
	}

	net := New("team", "unified", router,
		WithChildren(sub),
		WithAgentOptions(agent.WithSelfClone(4, time.Minute)),
	)

	result, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "merged" {
		t.Errorf("output = %q, want merged", result.Output)
	}
	if childExecs.Load() != 1 {
		t.Errorf("child executed %d times, want 1", childExecs.Load())
	}

	// Advertised defs: exactly one task tool, no agent_* or spawn_subagent.
	taskDefs := 0
	for _, name := range advertised {
		if name == core.ToolTask {
			taskDefs++
		}
		if strings.HasPrefix(name, core.ToolPrefixAgent) || name == core.ToolSelfClone {
			t.Errorf("legacy delegation def %q still advertised", name)
		}
	}
	if taskDefs != 1 {
		t.Errorf("task defs advertised = %d, want 1", taskDefs)
	}

	// Tool results: child report, clone report, unknown-subagent error.
	joined := strings.Join(results, "\n")
	for _, want := range []string{"child report: part one", "clone did SOLO:part two", `unknown subagent "ghost"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("tool results missing %q in:\n%s", want, joined)
		}
	}
}

// TestRouterCloneDelegatesToRoster: a router self-clone keeps the router's
// delegation surface — its task tool advertises the roster (without "self",
// clones cannot spawn further clones) and a task call addressed to a roster
// name routes back through the network's dispatch to that child.
func TestRouterCloneDelegatesToRoster(t *testing.T) {
	var childExecs atomic.Int32
	var childTask string
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			childExecs.Add(1)
			childTask = task.Input
			return agent.AgentResult{Output: "worker report: " + task.Input}, nil
		},
	}

	var cloneTaskParams string // task def params advertised to the clone
	var cloneSawReport bool
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			isCloneRun := false
			for _, m := range req.Messages {
				if m.Role == "user" && m.Content == "CLONEWORK" {
					isCloneRun = true
				}
			}
			if isCloneRun {
				if countAssistantToolTurns(req) == 0 {
					for _, tl := range req.Tools {
						if tl.Name == core.ToolTask {
							cloneTaskParams = string(tl.Parameters)
						}
					}
					args, _ := json.Marshal(map[string]string{"subagent": "worker", "task": "from clone"})
					return core.ChatResponse{ToolCalls: []core.ToolCall{{ID: "c1", Name: core.ToolTask, Args: args}}}
				}
				last := req.Messages[len(req.Messages)-1]
				if strings.Contains(last.Content, "worker report: from clone") {
					cloneSawReport = true
				}
				return core.ChatResponse{Content: "clone merged"}
			}
			if countAssistantToolTurns(req) == 0 {
				args, _ := json.Marshal(map[string]string{"subagent": "self", "task": "CLONEWORK"})
				return core.ChatResponse{ToolCalls: []core.ToolCall{{ID: "r1", Name: core.ToolTask, Args: args}}}
			}
			return core.ChatResponse{Content: "router merged"}
		},
	}

	net := New("team", "router with clones", router,
		WithChildren(sub),
		WithAgentOptions(agent.WithSelfClone(2, time.Minute)),
	)

	result, err := net.Execute(context.Background(), agent.AgentTask{Input: "MAIN"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "router merged" {
		t.Errorf("output = %q, want router merged", result.Output)
	}
	if childExecs.Load() != 1 {
		t.Fatalf("child executed %d times, want 1 (clone's delegation must reach the roster child)", childExecs.Load())
	}
	if childTask != "from clone" {
		t.Errorf("child task = %q, want %q", childTask, "from clone")
	}
	if !cloneSawReport {
		t.Error("clone never received the worker's report as its task tool result")
	}
	if cloneTaskParams == "" {
		t.Fatal("clone was not offered the task tool")
	}
	if !strings.Contains(cloneTaskParams, `"worker"`) {
		t.Errorf("clone task def enum missing roster child: %s", cloneTaskParams)
	}
	if strings.Contains(cloneTaskParams, `"self"`) {
		t.Errorf("clone task def must not offer \"self\" (no recursive clones): %s", cloneTaskParams)
	}
}
