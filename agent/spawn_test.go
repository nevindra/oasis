// spawn_test.go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

func TestWithSubAgentSpawningDefaults(t *testing.T) {
	cfg := BuildConfig([]AgentOption{WithSubAgentSpawning()})
	if !cfg.SpawnEnabled {
		t.Fatal("SpawnEnabled should be true")
	}
	if cfg.SpawnDepthLimit != 1 {
		t.Fatalf("SpawnDepthLimit = %d, want 1", cfg.SpawnDepthLimit)
	}
	if len(cfg.DeniedSpawnTools) != 0 {
		t.Fatalf("DeniedSpawnTools = %v, want empty", cfg.DeniedSpawnTools)
	}
}

func TestWithSubAgentSpawningCustom(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithSubAgentSpawning(
			MaxSpawnDepth(3),
			DenySpawnTools("shell_exec", "file_write"),
		),
	})
	if cfg.SpawnDepthLimit != 3 {
		t.Fatalf("SpawnDepthLimit = %d, want 3", cfg.SpawnDepthLimit)
	}
	if len(cfg.DeniedSpawnTools) != 2 {
		t.Fatalf("DeniedSpawnTools len = %d, want 2", len(cfg.DeniedSpawnTools))
	}
}

func TestDenySpawnToolsAccumulates(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithSubAgentSpawning(
			DenySpawnTools("shell_exec"),
			DenySpawnTools("file_write"),
		),
	})
	if len(cfg.DeniedSpawnTools) != 2 {
		t.Fatalf("DeniedSpawnTools len = %d, want 2", len(cfg.DeniedSpawnTools))
	}
}

func TestSpawnDepthContext(t *testing.T) {
	ctx := context.Background()
	if d := spawnDepth(ctx); d != 0 {
		t.Fatalf("default depth = %d, want 0", d)
	}
	ctx = withSpawnDepth(ctx, 3)
	if d := spawnDepth(ctx); d != 3 {
		t.Fatalf("depth after set = %d, want 3", d)
	}
}

// syncMockProvider is a thread-safe version of mockProvider for spawn tests.
// The parent and child agents share the same provider, so Chat() must be
// safe for concurrent calls.
type syncMockProvider struct {
	name      string
	responses []core.ChatResponse
	mu        sync.Mutex
	idx       int
}

func (m *syncMockProvider) Name() string { return m.name }
func (m *syncMockProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	resp := m.next()
	ch <- core.StreamEvent{Type: core.EventTextDelta, Content: resp.Content}
	return resp, nil
}
func (m *syncMockProvider) next() core.ChatResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return core.ChatResponse{Content: "exhausted"}
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp
}

// TestNetworkSpawnAgent moved to network/spawn_test.go (cycle-break: agent
// cannot import network).

func TestSpawnAgentBasic(t *testing.T) {
	// Thread-safe provider shared between parent and child.
	// Response order: parent call 1 (spawn), child call (response), parent call 2 (final).
	provider := &syncMockProvider{
		name: "test",
		responses: []core.ChatResponse{
			// Parent call 1: LLM decides to spawn
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"say hello","name":"greeter"}`),
			}}},
			// Child call: direct text response
			{Content: "hello from child"},
			// Parent call 2: final response
			{Content: "done"},
		},
	}

	agent := New("parent", "test parent", provider,
		WithSubAgentSpawning(),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}
}

func TestBuildStepTraceSpawnAgent(t *testing.T) {
	tc := core.ToolCall{
		ID:   "1",
		Name: "spawn_agent",
		Args: json.RawMessage(`{"task":"research topic X","name":"researcher"}`),
	}
	res := toolExecResult{
		content:  "found 3 results",
		duration: 2 * time.Second,
	}
	trace := buildStepTrace(tc, res)
	if trace.Type != "agent" {
		t.Errorf("Type = %q, want %q", trace.Type, "agent")
	}
	if trace.Name != "researcher" {
		t.Errorf("Name = %q, want %q", trace.Name, "researcher")
	}
	if !strings.Contains(trace.Input, "research topic X") {
		t.Errorf("Input = %q, want to contain task", trace.Input)
	}
}

func TestBuildStepTraceSpawnAgentNoName(t *testing.T) {
	tc := core.ToolCall{
		ID:   "1",
		Name: "spawn_agent",
		Args: json.RawMessage(`{"task":"do something quick"}`),
	}
	res := toolExecResult{content: "ok"}
	trace := buildStepTrace(tc, res)
	if trace.Type != "agent" {
		t.Errorf("Type = %q, want %q", trace.Type, "agent")
	}
	if trace.Name == "" || trace.Name == "spawn_agent" {
		t.Errorf("Name = %q, want auto-generated from task", trace.Name)
	}
}

func TestSpawnAgentBlockedInExecuteCode(t *testing.T) {
	// Construct the safeDispatch wrapper as dispatchBuiltins does for execute_code.
	innerCalled := false
	dispatch := func(ctx context.Context, tc core.ToolCall) DispatchResult {
		innerCalled = true
		return DispatchResult{Content: "ok"}
	}

	safeDispatchFn := func(ctx context.Context, tc core.ToolCall) DispatchResult {
		if tc.Name == "execute_plan" || tc.Name == "execute_code" || tc.Name == "spawn_agent" {
			return DispatchResult{Content: "error: " + tc.Name + " cannot be called from within execute_code", IsError: true}
		}
		return dispatch(ctx, tc)
	}

	// spawn_agent should be blocked.
	result := safeDispatchFn(context.Background(), core.ToolCall{Name: "spawn_agent", Args: json.RawMessage(`{}`)})
	if !result.IsError {
		t.Fatal("spawn_agent should be blocked inside execute_code")
	}
	if !strings.Contains(result.Content, "cannot be called from within execute_code") {
		t.Errorf("unexpected error: %q", result.Content)
	}

	// Regular tools should pass through.
	result = safeDispatchFn(context.Background(), core.ToolCall{Name: "greet", Args: json.RawMessage(`{}`)})
	if result.IsError {
		t.Fatalf("regular tool should not be blocked: %s", result.Content)
	}
	if !innerCalled {
		t.Fatal("inner dispatch should have been called for regular tool")
	}
}

// --- Task 6: Depth Limiting Tests ---

func TestSpawnAgentDepthLimit(t *testing.T) {
	// Provider: parent spawns a child. Child has spawn_agent stripped from tools
	// (depth=1 default), so it responds directly.
	provider := &syncMockProvider{
		name: "test",
		responses: []core.ChatResponse{
			// Parent: spawn a sub-agent
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"spawn again","name":"child"}`),
			}}},
			// Sub-agent: direct response (spawn_agent stripped at max depth)
			{Content: "child response"},
			// Parent: final response
			{Content: "parent done"},
		},
	}

	agent := New("parent", "test", provider,
		WithSubAgentSpawning(), // default depth=1
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "parent done" {
		t.Errorf("Output = %q, want %q", result.Output, "parent done")
	}
}

func TestSpawnAgentDepth2(t *testing.T) {
	provider := &syncMockProvider{
		name: "test",
		responses: []core.ChatResponse{
			// Parent: spawn child
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"spawn grandchild","name":"child"}`),
			}}},
			// Child: spawn grandchild
			{ToolCalls: []core.ToolCall{{
				ID:   "2",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"do work","name":"grandchild"}`),
			}}},
			// Grandchild: direct response (spawn_agent stripped at max depth)
			{Content: "grandchild done"},
			// Child: final response
			{Content: "child done"},
			// Parent: final response
			{Content: "parent done"},
		},
	}

	agent := New("parent", "test", provider,
		WithSubAgentSpawning(MaxSpawnDepth(2)),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "parent done" {
		t.Errorf("Output = %q, want %q", result.Output, "parent done")
	}
}

// --- Task 7: Tool Denial Tests ---

// syncCallbackProvider is a thread-safe provider that calls onChat for each request.
type syncCallbackProvider struct {
	name   string
	onChat func(core.ChatRequest) core.ChatResponse
}

func (p *syncCallbackProvider) Name() string { return p.name }
func (p *syncCallbackProvider) ChatStream(_ context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	resp := p.onChat(req)
	return resp, nil
}

func TestSpawnAgentDenyTools(t *testing.T) {
	var mu sync.Mutex
	var allToolNames [][]string
	callIdx := 0

	provider := &syncCallbackProvider{
		name: "test",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			mu.Lock()
			var names []string
			for _, td := range req.Tools {
				names = append(names, td.Name)
			}
			allToolNames = append(allToolNames, names)
			idx := callIdx
			callIdx++
			mu.Unlock()

			if idx == 0 {
				return core.ChatResponse{ToolCalls: []core.ToolCall{{
					ID:   "1",
					Name: "spawn_agent",
					Args: json.RawMessage(`{"task":"test","name":"worker"}`),
				}}}
			}
			return core.ChatResponse{Content: "done"}
		},
	}

	agent := New("parent", "test", provider,
		WithTools(mockTool{}, mockToolCalc{}),
		WithSubAgentSpawning(
			DenySpawnTools("calc"),
		),
	)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(allToolNames) < 2 {
		t.Fatalf("expected at least 2 Chat calls, got %d", len(allToolNames))
	}

	// Check the sub-agent's call (second call)
	childTools := allToolNames[1]
	for _, name := range childTools {
		if name == "calc" {
			t.Error("denied tool 'calc' should not be in sub-agent tools")
		}
		if name == "ask_user" {
			t.Error("ask_user should always be blocked in sub-agents")
		}
	}
	found := false
	for _, name := range childTools {
		if name == "greet" {
			found = true
		}
	}
	if !found {
		t.Error("allowed tool 'greet' should be in sub-agent tools")
	}
}

// --- Task 8: Parallel Spawn Test ---

func TestSpawnAgentParallel(t *testing.T) {
	const numSpawns = 3
	barrier := make(chan struct{})
	started := make(chan struct{}, numSpawns)

	var mu sync.Mutex
	callIdx := 0

	provider := &syncCallbackProvider{
		name: "test",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			mu.Lock()
			idx := callIdx
			callIdx++
			mu.Unlock()

			if idx == 0 {
				var tcs []core.ToolCall
				for i := 0; i < numSpawns; i++ {
					tcs = append(tcs, core.ToolCall{
						ID:   fmt.Sprintf("spawn_%d", i),
						Name: "spawn_agent",
						Args: json.RawMessage(fmt.Sprintf(`{"task":"task %d","name":"worker_%d"}`, i, i)),
					})
				}
				return core.ChatResponse{ToolCalls: tcs}
			}

			// Sub-agent calls: signal started, block on barrier.
			if idx >= 1 && idx <= numSpawns {
				started <- struct{}{}
				<-barrier
				return core.ChatResponse{Content: fmt.Sprintf("result_%d", idx-1)}
			}

			// Parent final response
			return core.ChatResponse{Content: "all done"}
		},
	}

	agent := New("parent", "test", provider,
		WithSubAgentSpawning(),
	)

	done := make(chan struct{})
	var result AgentResult
	var execErr error
	go func() {
		result, execErr = agent.Execute(context.Background(), AgentTask{Input: "test"})
		close(done)
	}()

	// All 3 sub-agents must start before any can finish.
	for i := 0; i < numSpawns; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("sub-agent did not start — likely running sequentially")
		}
	}

	close(barrier)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not finish in time")
	}

	if execErr != nil {
		t.Fatal(execErr)
	}
	if result.Output != "all done" {
		t.Errorf("Output = %q, want %q", result.Output, "all done")
	}
}

// --- Task 9: Error Path Tests ---

func TestSpawnAgentEmptyTask(t *testing.T) {
	provider := &syncMockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"","name":"worker"}`),
			}}},
			{Content: "done"},
		},
	}
	agent := New("parent", "test", provider, WithSubAgentSpawning())
	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}
}

func TestSpawnAgentInvalidArgs(t *testing.T) {
	provider := &syncMockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{invalid json`),
			}}},
			{Content: "done"},
		},
	}
	agent := New("parent", "test", provider, WithSubAgentSpawning())
	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}
}

// --- Fix #3: Spawn inherits parent Tracer ---

// TestSpawnAgentInheritsParentTracer verifies that a spawned sub-agent runs
// with the parent's tracer, so its iterations and LLM calls appear under the
// parent's span tree.
func TestSpawnAgentInheritsParentTracer(t *testing.T) {
	tracer := &recordingTracer{}

	provider := &syncMockProvider{
		name: "test",
		responses: []core.ChatResponse{
			// Parent: spawn a child
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"do work","name":"worker"}`),
			}}},
			// Child: final answer
			{Content: "child done"},
			// Parent: final answer
			{Content: "parent done"},
		},
	}

	a := New("parent", "test", provider,
		WithTracer(tracer),
		WithSubAgentSpawning(),
	)
	_, err := a.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}

	names := tracer.names()
	// Parent and child both go through ExecuteWithSpan, so we expect at least
	// two "agent.execute" spans — one for the parent, one for the child.
	count := 0
	for _, n := range names {
		if n == "agent.execute" {
			count++
		}
	}
	if count < 2 {
		t.Errorf("expected at least 2 agent.execute spans (parent + child), got %d in %v", count, names)
	}
}

// --- Fix #6: WithGeneration deep-copy ---

// TestWithGenerationDeepCopy verifies that mutations to the caller's Generation
// value after calling WithGeneration do not affect the agent's stored config.
func TestWithGenerationDeepCopy(t *testing.T) {
	temp := 0.5
	g := Generation{Temperature: &temp}
	cfg := BuildConfig([]AgentOption{WithGeneration(g)})

	// Mutate the original pointer value.
	*g.Temperature = 0.99

	if cfg.GenParams == nil {
		t.Fatal("GenParams should be set")
	}
	if cfg.GenParams.Temperature == nil {
		t.Fatal("Temperature should be set")
	}
	if *cfg.GenParams.Temperature != 0.5 {
		t.Errorf("stored Temperature = %v, want 0.5 (deep-copy should isolate from caller mutation)", *cfg.GenParams.Temperature)
	}
}

// --- Fix #8: Spawn forwards stream events ---

// streamingCallbackProvider emits each non-tool response as an EventTextDelta
// during ChatStream, so tests can verify child events reach the parent channel.
type streamingCallbackProvider struct {
	name   string
	onChat func(core.ChatRequest) core.ChatResponse
}

func (p *streamingCallbackProvider) Name() string { return p.name }
func (p *streamingCallbackProvider) ChatStream(_ context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	resp := p.onChat(req)
	if resp.Content != "" {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: resp.Content}
	}
	return resp, nil
}

// TestSpawnAgentStreamEventsForwarded verifies that when a parent agent runs
// ExecuteStream and spawns a child, the child's EventTextDelta events reach
// the parent's channel.
func TestSpawnAgentStreamEventsForwarded(t *testing.T) {
	var mu sync.Mutex
	callIdx := 0

	provider := &streamingCallbackProvider{
		name: "test",
		onChat: func(_ core.ChatRequest) core.ChatResponse {
			mu.Lock()
			idx := callIdx
			callIdx++
			mu.Unlock()

			switch idx {
			case 0:
				// Parent: call spawn_agent.
				return core.ChatResponse{ToolCalls: []core.ToolCall{{
					ID:   "spawn_1",
					Name: "spawn_agent",
					Args: json.RawMessage(`{"task":"do work","name":"worker"}`),
				}}}
			case 1:
				// Child: final answer with text content (emitted as EventTextDelta).
				return core.ChatResponse{Content: "child result"}
			default:
				// Parent: synthesize final answer.
				return core.ChatResponse{Content: "all done"}
			}
		},
	}

	a := New("parent", "test", provider,
		WithSubAgentSpawning(),
	)

	ch := make(chan core.StreamEvent, 64)
	_, err := a.Execute(context.Background(), AgentTask{Input: "test"}, core.WithStream(ch))
	if err != nil {
		t.Fatal(err)
	}

	// Drain and count EventTextDelta events.
	var textDeltas []string
	for ev := range ch {
		if ev.Type == core.EventTextDelta {
			textDeltas = append(textDeltas, ev.Content)
		}
	}

	// We expect at least "child result" from the child and "all done" from the parent.
	foundChild := false
	foundParent := false
	for _, d := range textDeltas {
		if d == "child result" {
			foundChild = true
		}
		if d == "all done" {
			foundParent = true
		}
	}
	if !foundChild {
		t.Errorf("child EventTextDelta %q not found in parent stream; got %v", "child result", textDeltas)
	}
	if !foundParent {
		t.Errorf("parent EventTextDelta %q not found in stream; got %v", "all done", textDeltas)
	}
}
