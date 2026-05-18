package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Test helpers for root-package tests (retriever, scheduler, types).
// Agent-focused tests live in agent/ and have their own helpers there.

// nopStore satisfies the Store interface with no-ops.
type nopStore struct{}

func (nopStore) CreateThread(_ context.Context, _ Thread) error                                { return nil }
func (nopStore) GetThread(_ context.Context, _ string) (Thread, error)                         { return Thread{}, nil }
func (nopStore) ListThreads(_ context.Context, _ string, _ int) ([]Thread, error)              { return nil, nil }
func (nopStore) UpdateThread(_ context.Context, _ Thread) error                                { return nil }
func (nopStore) DeleteThread(_ context.Context, _ string) error                                { return nil }
func (nopStore) StoreMessage(_ context.Context, _ Message) error                               { return nil }
func (nopStore) GetMessages(_ context.Context, _ string, _ int) ([]Message, error)             { return nil, nil }
func (nopStore) SearchMessages(_ context.Context, _ []float32, _ int) ([]ScoredMessage, error) { return nil, nil }
func (nopStore) StoreDocument(_ context.Context, _ Document, _ []Chunk) error                  { return nil }
func (nopStore) ListDocuments(_ context.Context, _ int) ([]Document, error)                    { return nil, nil }
func (nopStore) DeleteDocument(_ context.Context, _ string) error                              { return nil }
func (nopStore) SearchChunks(_ context.Context, _ []float32, _ int, _ ...ChunkFilter) ([]ScoredChunk, error) {
	return nil, nil
}
func (nopStore) GetChunksByIDs(_ context.Context, _ []string) ([]Chunk, error) { return nil, nil }
func (nopStore) GetConfig(_ context.Context, _ string) (string, error)         { return "", nil }
func (nopStore) SetConfig(_ context.Context, _, _ string) error                { return nil }
func (nopStore) CreateScheduledAction(_ context.Context, _ ScheduledAction) error               { return nil }
func (nopStore) ListScheduledActions(_ context.Context) ([]ScheduledAction, error)              { return nil, nil }
func (nopStore) GetDueScheduledActions(_ context.Context, _ int64) ([]ScheduledAction, error)   { return nil, nil }
func (nopStore) UpdateScheduledAction(_ context.Context, _ ScheduledAction) error               { return nil }
func (nopStore) UpdateScheduledActionEnabled(_ context.Context, _ string, _ bool) error         { return nil }
func (nopStore) DeleteScheduledAction(_ context.Context, _ string) error                        { return nil }
func (nopStore) DeleteAllScheduledActions(_ context.Context) (int, error)                       { return 0, nil }
func (nopStore) FindScheduledActionsByDescription(_ context.Context, _ string) ([]ScheduledAction, error) {
	return nil, nil
}
func (nopStore) Init(_ context.Context) error { return nil }
func (nopStore) Close() error                 { return nil }

// mockProvider is a deterministic stub Provider for retriever tests.
type mockProvider struct {
	name      string
	responses []ChatResponse
	idx       int
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Chat(_ context.Context, _ ChatRequest) (ChatResponse, error) {
	if m.idx >= len(m.responses) {
		return ChatResponse{Content: "exhausted"}, nil
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, nil
}
func (m *mockProvider) ChatStream(_ context.Context, _ ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	resp, _ := m.Chat(context.Background(), ChatRequest{})
	ch <- StreamEvent{Type: EventTextDelta, Content: resp.Content}
	return resp, nil
}

// mockAgent is a test Agent with configurable behavior, used by scheduler tests.
type mockAgent struct {
	name   string
	desc   string
	result AgentResult
	err    error
	delay  time.Duration
}

func (m *mockAgent) Name() string        { return m.name }
func (m *mockAgent) Description() string { return m.desc }
func (m *mockAgent) Execute(ctx context.Context, _ AgentTask) (AgentResult, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}
	return m.result, m.err
}

// mockTool / mockToolCalc are minimal AnyTool implementations for tool registry tests.
type mockTool struct{}

func (mockTool) Name() string               { return "greet" }
func (mockTool) Definition() ToolDefinition { return ToolDefinition{Name: "greet", Description: "Say hello"} }
func (mockTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "hello from greet"}, nil
}

type mockToolCalc struct{}

func (mockToolCalc) Name() string               { return "calc" }
func (mockToolCalc) Definition() ToolDefinition { return ToolDefinition{Name: "calc", Description: "Calculate"} }
func (mockToolCalc) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "result from calc"}, nil
}

// errTool is used by registry tests that exercise error paths.
type errTool struct{}

func (errTool) Name() string               { return "fail" }
func (errTool) Definition() ToolDefinition { return ToolDefinition{Name: "fail", Description: "Always fails"} }
func (errTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{}, errors.New("tool broken")
}

// readTool and writeTool are two atomic AnyTool instances used by registry tests
// that previously asserted a "bundle of tools" shape; now they register as two
// independent tools.
type readTool struct{}

func (readTool) Name() string               { return "read" }
func (readTool) Definition() ToolDefinition { return ToolDefinition{Name: "read", Description: "Read file"} }
func (readTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "did read"}, nil
}

type writeTool struct{}

func (writeTool) Name() string               { return "write" }
func (writeTool) Definition() ToolDefinition { return ToolDefinition{Name: "write", Description: "Write file"} }
func (writeTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "did write"}, nil
}
