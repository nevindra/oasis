package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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
func (nopStore) SearchMessages(_ context.Context, _ []float32, _ int, _ string) ([]ScoredMessage, error) { return nil, nil }
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

// capturedRequestProvider records every ChatRequest it receives and returns a
// terminal (no-tool-calls) response so the agent loop exits cleanly after one
// iteration.
type capturedRequestProvider struct {
	name string
	mu   sync.Mutex
	reqs []ChatRequest
}

func (p *capturedRequestProvider) Name() string { return p.name }
func (p *capturedRequestProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	return ChatResponse{Content: "done"}, nil
}

// last returns the most recently captured ChatRequest. Panics if none.
func (p *capturedRequestProvider) last() ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reqs[len(p.reqs)-1]
}

// callCount returns the number of Chat calls made to this provider.
func (p *capturedRequestProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// twoIterProvider returns a tool call on the first Chat call (forcing a
// second iteration) and plain text on the second call. Embed
// capturedRequestProvider so last() and callCount() are available and all
// ChatRequest values are captured.
type twoIterProvider struct {
	capturedRequestProvider
}

func (p *twoIterProvider) ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	n := len(p.reqs)
	p.mu.Unlock()
	if n == 1 {
		// Emit a tool call so the loop continues to a second iteration.
		return ChatResponse{
			ToolCalls: []core.ToolCall{{ID: "tc1", Name: "greet", Args: []byte(`{}`)}},
		}, nil
	}
	// Second iteration: plain text → loop terminates.
	return ChatResponse{Content: "done"}, nil
}

// flakyProvider wraps capturedRequestProvider; it consults errFn on each
// ChatStream call to decide whether to return an error or a normal response.
type flakyProvider struct {
	capturedRequestProvider
	mu    sync.Mutex
	errFn func() error // called once per ChatStream; nil return means succeed
}

func (p *flakyProvider) ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	p.mu.Lock()
	fn := p.errFn
	p.mu.Unlock()
	if fn != nil {
		if err := fn(); err != nil {
			close(ch)
			return ChatResponse{}, err
		}
	}
	return p.capturedRequestProvider.ChatStream(ctx, req, ch)
}
