package network

import (
	"context"
	"encoding/json"
	"sync"

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
	if ch != nil {
		defer close(ch)
	}
	resp := m.next()
	if ch != nil {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: resp.Content}
	}
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
	if ch != nil {
		defer close(ch)
	}
	return p.onChat(req), nil
}
