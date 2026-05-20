package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nevindra/oasis/core"
)

// mustAttachmentBase64 fails the test if base64 decode fails. Used to keep
// test data readable while still routing through the validating constructor.
func mustAttachmentBase64(t *testing.T, mime, encoded string) Attachment {
	t.Helper()
	att, err := core.NewAttachmentFromBase64(mime, encoded)
	if err != nil {
		t.Fatalf("decode test attachment: %v", err)
	}
	return att
}

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
func (nopStore) Init(_ context.Context) error { return nil }
func (nopStore) Close() error                                                         { return nil }

// --- Tool mocks (shared across agent_test.go, workflow_test.go) ---
//
// Each mock implements AnyTool directly. The "greet"/"calc"/"fail" tools are
// atomic; multiTool was a bundle in the old shape and now lives as two
// independent atomic instances (readTool + writeTool).

type mockTool struct{}

func (m mockTool) Name() string               { return "greet" }
func (m mockTool) Definition() ToolDefinition { return ToolDefinition{Name: "greet", Description: "Say hello"} }
func (m mockTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return core.TextResult("hello from greet"), nil
}

type mockToolCalc struct{}

func (m mockToolCalc) Name() string               { return "calc" }
func (m mockToolCalc) Definition() ToolDefinition { return ToolDefinition{Name: "calc", Description: "Calculate"} }
func (m mockToolCalc) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return core.TextResult("result from calc"), nil
}

type errTool struct{}

func (e errTool) Name() string               { return "fail" }
func (e errTool) Definition() ToolDefinition { return ToolDefinition{Name: "fail", Description: "Always fails"} }
func (e errTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
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

func (t *contextReadingTool) Name() string { return "ctx_reader" }
func (t *contextReadingTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "ctx_reader", Description: "Reads context"}
}
func (t *contextReadingTool) ExecuteRaw(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
	if t.onExecute != nil {
		t.onExecute(ctx)
	}
	return core.TextResult("ok"), nil
}

// readTool and writeTool replace the legacy bundle-style multiTool with two
// atomic tools. Tests previously asserting "two definitions from one Add" now
// register both and observe two tools in the registry.

type readTool struct{}

func (readTool) Name() string               { return "read" }
func (readTool) Definition() ToolDefinition { return ToolDefinition{Name: "read", Description: "Read file"} }
func (readTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return core.TextResult("did read"), nil
}

type writeTool struct{}

func (writeTool) Name() string               { return "write" }
func (writeTool) Definition() ToolDefinition { return ToolDefinition{Name: "write", Description: "Write file"} }
func (writeTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return core.TextResult("did write"), nil
}
