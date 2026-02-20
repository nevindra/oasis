package oasis

import (
	"context"
	"encoding/json"
	"errors"
)

// nopStore satisfies the Store interface with no-ops.
// Embed this in test-specific store structs to avoid implementing every method.
type nopStore struct{}

func (nopStore) CreateThread(_ context.Context, _ Thread) error                               { return nil }
func (nopStore) GetThread(_ context.Context, _ string) (Thread, error)                        { return Thread{}, nil }
func (nopStore) ListThreads(_ context.Context, _ string, _ int) ([]Thread, error)             { return nil, nil }
func (nopStore) UpdateThread(_ context.Context, _ Thread) error                               { return nil }
func (nopStore) DeleteThread(_ context.Context, _ string) error                               { return nil }
func (nopStore) StoreMessage(_ context.Context, _ Message) error                              { return nil }
func (nopStore) GetMessages(_ context.Context, _ string, _ int) ([]Message, error)            { return nil, nil }
func (nopStore) SearchMessages(_ context.Context, _ []float32, _ int) ([]ScoredMessage, error) { return nil, nil }
func (nopStore) StoreDocument(_ context.Context, _ Document, _ []Chunk) error              { return nil }
func (nopStore) ListDocuments(_ context.Context, _ int) ([]Document, error)                { return nil, nil }
func (nopStore) DeleteDocument(_ context.Context, _ string) error                          { return nil }
func (nopStore) SearchChunks(_ context.Context, _ []float32, _ int, _ ...ChunkFilter) ([]ScoredChunk, error) { return nil, nil }
func (nopStore) GetChunksByIDs(_ context.Context, _ []string) ([]Chunk, error)             { return nil, nil }
func (nopStore) GetConfig(_ context.Context, _ string) (string, error)                        { return "", nil }
func (nopStore) SetConfig(_ context.Context, _, _ string) error                               { return nil }
func (nopStore) CreateScheduledAction(_ context.Context, _ ScheduledAction) error             { return nil }
func (nopStore) ListScheduledActions(_ context.Context) ([]ScheduledAction, error)            { return nil, nil }
func (nopStore) GetDueScheduledActions(_ context.Context, _ int64) ([]ScheduledAction, error) { return nil, nil }
func (nopStore) UpdateScheduledAction(_ context.Context, _ ScheduledAction) error             { return nil }
func (nopStore) UpdateScheduledActionEnabled(_ context.Context, _ string, _ bool) error       { return nil }
func (nopStore) DeleteScheduledAction(_ context.Context, _ string) error                      { return nil }
func (nopStore) DeleteAllScheduledActions(_ context.Context) (int, error)                     { return 0, nil }
func (nopStore) FindScheduledActionsByDescription(_ context.Context, _ string) ([]ScheduledAction, error) {
	return nil, nil
}
func (nopStore) CreateSkill(_ context.Context, _ Skill) error                        { return nil }
func (nopStore) GetSkill(_ context.Context, _ string) (Skill, error)                 { return Skill{}, nil }
func (nopStore) ListSkills(_ context.Context) ([]Skill, error)                       { return nil, nil }
func (nopStore) UpdateSkill(_ context.Context, _ Skill) error                        { return nil }
func (nopStore) DeleteSkill(_ context.Context, _ string) error                       { return nil }
func (nopStore) SearchSkills(_ context.Context, _ []float32, _ int) ([]ScoredSkill, error) { return nil, nil }
func (nopStore) Init(_ context.Context) error                                         { return nil }
func (nopStore) Close() error                                                         { return nil }

// --- Tool mocks (shared across agent_test.go, workflow_test.go) ---

type mockTool struct{}

func (m mockTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "greet", Description: "Say hello"}}
}

func (m mockTool) Execute(_ context.Context, name string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "hello from " + name}, nil
}

type mockToolCalc struct{}

func (m mockToolCalc) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "calc", Description: "Calculate"}}
}
func (m mockToolCalc) Execute(_ context.Context, name string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "result from " + name}, nil
}

type errTool struct{}

func (e errTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "fail", Description: "Always fails"}}
}
func (e errTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{}, errors.New("tool broken")
}

// callbackProvider captures ChatRequest via onChat callback for assertions.
type callbackProvider struct {
	name     string
	response ChatResponse
	onChat   func(ChatRequest)
}

func (c *callbackProvider) Name() string { return c.name }
func (c *callbackProvider) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	if c.onChat != nil {
		c.onChat(req)
	}
	return c.response, nil
}
func (c *callbackProvider) ChatWithTools(_ context.Context, req ChatRequest, _ []ToolDefinition) (ChatResponse, error) {
	if c.onChat != nil {
		c.onChat(req)
	}
	return c.response, nil
}
func (c *callbackProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	if c.onChat != nil {
		c.onChat(req)
	}
	return c.response, nil
}

// contextReadingTool is a tool that captures context in Execute for testing.
type contextReadingTool struct {
	onExecute func(ctx context.Context)
}

func (t *contextReadingTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "ctx_reader", Description: "Reads context"}}
}
func (t *contextReadingTool) Execute(ctx context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	if t.onExecute != nil {
		t.onExecute(ctx)
	}
	return ToolResult{Content: "ok"}, nil
}

type multiTool struct{}

func (m multiTool) Definitions() []ToolDefinition {
	return []ToolDefinition{
		{Name: "read", Description: "Read file"},
		{Name: "write", Description: "Write file"},
	}
}
func (m multiTool) Execute(_ context.Context, name string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "did " + name}, nil
}
