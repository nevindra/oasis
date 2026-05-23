package network

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

// mockTool is a simple tool used in spawn tests.
type mockTool struct{}

func (m mockTool) Name() string { return "greet" }
func (m mockTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "greet", Description: "Say hello"}
}
func (m mockTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	return core.TextResult("hello from greet"), nil
}

// syncMockProvider is a thread-safe Provider for network spawn tests.
// Network and child agent share the same provider, so ChatStream must be safe
// for concurrent calls.
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

// routerCallbackProvider is a thread-safe Provider with a per-request callback.
// Used where tests need to inspect the tool list seen by each LLM call.
type routerCallbackProvider struct {
	name   string
	onChat func(core.ChatRequest) core.ChatResponse
}

func (p *routerCallbackProvider) Name() string { return p.name }
func (p *routerCallbackProvider) ChatStream(_ context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	return p.onChat(req), nil
}

// TestNetworkSpawnAgent verifies that a Network configured with
// WithSubAgentSpawning can spawn a child agent via the spawn_agent tool.
func TestNetworkSpawnAgent(t *testing.T) {
	provider := &syncMockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{
				ID:   "1",
				Name: "spawn_agent",
				Args: json.RawMessage(`{"task":"do something","name":"worker"}`),
			}}},
			{Content: "child done"},
			{Content: "network done"},
		},
	}

	net := NewNetwork("net", "test network", provider,
		agent.WithSubAgentSpawning(),
	)

	result, err := net.Execute(context.Background(), agent.AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "network done" {
		t.Errorf("Output = %q, want %q", result.Output, "network done")
	}
}

// TestNetworkSpawnAgentStripsAgentTools verifies that when a Network spawns a
// sub-agent, the child does not inherit agent_* router tool defs.
// The child is an LLMAgent whose dispatch does not route the agent_ prefix,
// so inheriting those defs would waste tokens and produce "unknown tool" errors.
// A shared provider is used for router + child so every LLM call's tool list
// can be inspected in one slice.
func TestNetworkSpawnAgentStripsAgentTools(t *testing.T) {
	var mu sync.Mutex
	var allToolNames [][]string
	var callIdx int

	sharedProvider := &routerCallbackProvider{
		name: "shared",
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
				// Router: spawn a child.
				return core.ChatResponse{ToolCalls: []core.ToolCall{{
					ID:   "1",
					Name: "spawn_agent",
					Args: json.RawMessage(`{"task":"do work","name":"worker"}`),
				}}}
			}
			// idx==1: child final answer; idx==2: router final answer.
			return core.ChatResponse{Content: "done"}
		},
	}

	worker := agent.NewLLMAgent("worker_agent", "does work", sharedProvider)
	network := NewNetwork("net", "routes work", sharedProvider,
		agent.WithAgents(worker),
		agent.WithTools(mockTool{}),
		agent.WithSubAgentSpawning(),
	)

	_, err := network.Execute(context.Background(), agent.AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(allToolNames) < 2 {
		t.Fatalf("expected at least 2 Chat calls (router + child), got %d", len(allToolNames))
	}

	// Router (first call) should see agent_worker_agent.
	parentHasAgentTool := false
	for _, name := range allToolNames[0] {
		if name == "agent_worker_agent" {
			parentHasAgentTool = true
		}
	}
	if !parentHasAgentTool {
		t.Errorf("router should see agent_worker_agent in its tool list; got %v", allToolNames[0])
	}

	// Child (second call) must NOT see any agent_* tool.
	for _, name := range allToolNames[1] {
		if strings.HasPrefix(name, "agent_") {
			t.Errorf("sub-agent should not inherit agent_* tool %q; child LLMAgent has no agent_ routing", name)
		}
		if name == "ask_user" {
			t.Error("ask_user should always be blocked in sub-agents")
		}
	}

	// Child should still have the direct tool.
	directFound := false
	for _, name := range allToolNames[1] {
		if name == "greet" {
			directFound = true
		}
	}
	if !directFound {
		t.Errorf("direct tool 'greet' should be inherited by sub-agent; child tools: %v", allToolNames[1])
	}
}
