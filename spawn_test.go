// spawn_test.go
package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWithSubAgentSpawningDefaults(t *testing.T) {
	cfg := buildConfig([]AgentOption{WithSubAgentSpawning()})
	if !cfg.spawnEnabled {
		t.Fatal("spawnEnabled should be true")
	}
	if cfg.maxSpawnDepth != 1 {
		t.Fatalf("maxSpawnDepth = %d, want 1", cfg.maxSpawnDepth)
	}
	if len(cfg.denySpawnTools) != 0 {
		t.Fatalf("denySpawnTools = %v, want empty", cfg.denySpawnTools)
	}
}

func TestWithSubAgentSpawningCustom(t *testing.T) {
	cfg := buildConfig([]AgentOption{
		WithSubAgentSpawning(
			MaxSpawnDepth(3),
			DenySpawnTools("shell_exec", "file_write"),
		),
	})
	if cfg.maxSpawnDepth != 3 {
		t.Fatalf("maxSpawnDepth = %d, want 3", cfg.maxSpawnDepth)
	}
	if len(cfg.denySpawnTools) != 2 {
		t.Fatalf("denySpawnTools len = %d, want 2", len(cfg.denySpawnTools))
	}
}

func TestDenySpawnToolsAccumulates(t *testing.T) {
	cfg := buildConfig([]AgentOption{
		WithSubAgentSpawning(
			DenySpawnTools("shell_exec"),
			DenySpawnTools("file_write"),
		),
	})
	if len(cfg.denySpawnTools) != 2 {
		t.Fatalf("denySpawnTools len = %d, want 2", len(cfg.denySpawnTools))
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
	responses []ChatResponse
	mu        sync.Mutex
	idx       int
}

func (m *syncMockProvider) Name() string { return m.name }
func (m *syncMockProvider) Chat(_ context.Context, _ ChatRequest) (ChatResponse, error) {
	return m.next(), nil
}
func (m *syncMockProvider) ChatStream(_ context.Context, _ ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	resp := m.next()
	ch <- StreamEvent{Type: EventTextDelta, Content: resp.Content}
	return resp, nil
}
func (m *syncMockProvider) next() ChatResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return ChatResponse{Content: "exhausted"}
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp
}

func TestNetworkSpawnAgent(t *testing.T) {
	provider := &syncMockProvider{
		name: "test",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"do something","name":"worker"}`),
			}}},
			{Content: "child done"},
			{Content: "network done"},
		},
	}

	network := NewNetwork("net", "test network", provider,
		WithSubAgentSpawning(),
	)

	result, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "network done" {
		t.Errorf("Output = %q, want %q", result.Output, "network done")
	}
}

func TestSpawnAgentBasic(t *testing.T) {
	// Thread-safe provider shared between parent and child.
	// Response order: parent call 1 (spawn), child call (response), parent call 2 (final).
	provider := &syncMockProvider{
		name: "test",
		responses: []ChatResponse{
			// Parent call 1: LLM decides to spawn
			{ToolCalls: []ToolCall{{
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

	agent := NewLLMAgent("parent", "test parent", provider,
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
	tc := ToolCall{
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
	tc := ToolCall{
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
	dispatch := func(ctx context.Context, tc ToolCall) DispatchResult {
		innerCalled = true
		return DispatchResult{Content: "ok"}
	}

	safeDispatchFn := func(ctx context.Context, tc ToolCall) DispatchResult {
		if tc.Name == "execute_plan" || tc.Name == "execute_code" || tc.Name == "spawn_agent" {
			return DispatchResult{Content: "error: " + tc.Name + " cannot be called from within execute_code", IsError: true}
		}
		return dispatch(ctx, tc)
	}

	// spawn_agent should be blocked.
	result := safeDispatchFn(context.Background(), ToolCall{Name: "spawn_agent", Args: json.RawMessage(`{}`)})
	if !result.IsError {
		t.Fatal("spawn_agent should be blocked inside execute_code")
	}
	if !strings.Contains(result.Content, "cannot be called from within execute_code") {
		t.Errorf("unexpected error: %q", result.Content)
	}

	// Regular tools should pass through.
	result = safeDispatchFn(context.Background(), ToolCall{Name: "greet", Args: json.RawMessage(`{}`)})
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
		responses: []ChatResponse{
			// Parent: spawn a sub-agent
			{ToolCalls: []ToolCall{{
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

	agent := NewLLMAgent("parent", "test", provider,
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
		responses: []ChatResponse{
			// Parent: spawn child
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"spawn grandchild","name":"child"}`),
			}}},
			// Child: spawn grandchild
			{ToolCalls: []ToolCall{{
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

	agent := NewLLMAgent("parent", "test", provider,
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
	onChat func(ChatRequest) ChatResponse
}

func (p *syncCallbackProvider) Name() string { return p.name }
func (p *syncCallbackProvider) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	return p.onChat(req), nil
}
func (p *syncCallbackProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
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
		onChat: func(req ChatRequest) ChatResponse {
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
				return ChatResponse{ToolCalls: []ToolCall{{
					ID:   "1",
					Name: "spawn_agent",
					Args: json.RawMessage(`{"task":"test","name":"worker"}`),
				}}}
			}
			return ChatResponse{Content: "done"}
		},
	}

	agent := NewLLMAgent("parent", "test", provider,
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

// TestNetworkSpawnAgentStripsAgentTools verifies that when a Network spawns a
// sub-agent, the child does not inherit the `agent_*` router tool defs.
// The child is an LLMAgent whose dispatch does not route the agent_ prefix,
// so inheriting those defs would waste tokens and produce "unknown tool"
// errors if the child called them.
func TestNetworkSpawnAgentStripsAgentTools(t *testing.T) {
	var mu sync.Mutex
	var allToolNames [][]string
	callIdx := 0

	provider := &syncCallbackProvider{
		name: "test",
		onChat: func(req ChatRequest) ChatResponse {
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
				return ChatResponse{ToolCalls: []ToolCall{{
					ID:   "1",
					Name: "spawn_agent",
					Args: json.RawMessage(`{"task":"do work","name":"worker"}`),
				}}}
			}
			return ChatResponse{Content: "done"}
		},
	}

	worker := NewLLMAgent("worker_agent", "does work", provider)
	network := NewNetwork("net", "routes work", provider,
		WithAgents(worker),
		WithTools(mockTool{}),
		WithSubAgentSpawning(),
	)

	_, err := network.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(allToolNames) < 2 {
		t.Fatalf("expected at least 2 Chat calls, got %d", len(allToolNames))
	}

	// Parent (router) sees agent_worker_agent.
	parentHasAgentTool := false
	for _, name := range allToolNames[0] {
		if name == "agent_worker_agent" {
			parentHasAgentTool = true
		}
	}
	if !parentHasAgentTool {
		t.Error("router should see agent_worker_agent in its tool list")
	}

	// Child (sub-agent) must NOT see any agent_* tool.
	for _, name := range allToolNames[1] {
		if strings.HasPrefix(name, "agent_") {
			t.Errorf("sub-agent should not inherit agent_* tool %q (cannot be routed by LLMAgent dispatch)", name)
		}
		if name == "ask_user" {
			t.Error("ask_user should always be blocked in sub-agents")
		}
	}
	// Sanity: child still has the direct tool.
	directFound := false
	for _, name := range allToolNames[1] {
		if name == "greet" {
			directFound = true
		}
	}
	if !directFound {
		t.Error("direct tool 'greet' should be inherited by sub-agent")
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
		onChat: func(req ChatRequest) ChatResponse {
			mu.Lock()
			idx := callIdx
			callIdx++
			mu.Unlock()

			if idx == 0 {
				var tcs []ToolCall
				for i := 0; i < numSpawns; i++ {
					tcs = append(tcs, ToolCall{
						ID:   fmt.Sprintf("spawn_%d", i),
						Name: "spawn_agent",
						Args: json.RawMessage(fmt.Sprintf(`{"task":"task %d","name":"worker_%d"}`, i, i)),
					})
				}
				return ChatResponse{ToolCalls: tcs}
			}

			// Sub-agent calls: signal started, block on barrier.
			if idx >= 1 && idx <= numSpawns {
				started <- struct{}{}
				<-barrier
				return ChatResponse{Content: fmt.Sprintf("result_%d", idx-1)}
			}

			// Parent final response
			return ChatResponse{Content: "all done"}
		},
	}

	agent := NewLLMAgent("parent", "test", provider,
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
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"","name":"worker"}`),
			}}},
			{Content: "done"},
		},
	}
	agent := NewLLMAgent("parent", "test", provider, WithSubAgentSpawning())
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
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{invalid json`),
			}}},
			{Content: "done"},
		},
	}
	agent := NewLLMAgent("parent", "test", provider, WithSubAgentSpawning())
	result, err := agent.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" {
		t.Errorf("Output = %q, want %q", result.Output, "done")
	}
}
