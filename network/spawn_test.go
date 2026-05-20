package network

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

// syncMockProvider is a thread-safe Provider for network spawn tests.
// Network and child agent share the same provider, so Chat() must be safe
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
