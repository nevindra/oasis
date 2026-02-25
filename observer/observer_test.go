package observer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockProvider for observer tests.
type mockProvider struct {
	name     string
	chatResp oasis.ChatResponse
	chatErr  error
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Chat(_ context.Context, _ oasis.ChatRequest) (oasis.ChatResponse, error) {
	return m.chatResp, m.chatErr
}
func (m *mockProvider) ChatStream(_ context.Context, _ oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	ch <- oasis.StreamEvent{Type: oasis.EventTextDelta, Content: "hello"}
	ch <- oasis.StreamEvent{Type: oasis.EventTextDelta, Content: " world"}
	close(ch)
	return m.chatResp, m.chatErr
}

// mockProviderManyEvents sends count events then closes the channel.
type mockProviderManyEvents struct {
	name     string
	chatResp oasis.ChatResponse
	count    int
}

func (m *mockProviderManyEvents) Name() string { return m.name }
func (m *mockProviderManyEvents) Chat(_ context.Context, _ oasis.ChatRequest) (oasis.ChatResponse, error) {
	return m.chatResp, nil
}
func (m *mockProviderManyEvents) ChatStream(_ context.Context, _ oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	for i := range m.count {
		select {
		case ch <- oasis.StreamEvent{Type: oasis.EventTextDelta, Content: string(rune('a' + i%26))}:
		default:
			// Channel full — stop sending to avoid blocking forever in tests.
		}
	}
	close(ch)
	return m.chatResp, nil
}

// mockTool for observer tests.
type mockTool struct {
	defs   []oasis.ToolDefinition
	result oasis.ToolResult
	err    error
}

func (m *mockTool) Definitions() []oasis.ToolDefinition { return m.defs }
func (m *mockTool) Execute(_ context.Context, _ string, _ json.RawMessage) (oasis.ToolResult, error) {
	return m.result, m.err
}

// mockEmbedding for observer tests.
type mockEmbedding struct {
	name string
	dims int
	vecs [][]float32
	err  error
}

func (m *mockEmbedding) Name() string                                          { return m.name }
func (m *mockEmbedding) Dimensions() int                                       { return m.dims }
func (m *mockEmbedding) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return m.vecs, m.err
}

// testInstruments creates a no-op Instruments using the global OTEL providers
// (which are no-ops by default). This is safe for testing delegation behavior
// without any real OTEL backend.
func testInstruments(t *testing.T) *Instruments {
	t.Helper()
	inst, err := newInstruments(nil)
	if err != nil {
		t.Fatalf("newInstruments: %v", err)
	}
	return inst
}

// ---------------------------------------------------------------------------
// ObservedProvider tests
// ---------------------------------------------------------------------------

func TestObservedProviderName(t *testing.T) {
	inner := &mockProvider{name: "test-provider"}
	op := WrapProvider(inner, "test-model", testInstruments(t))

	got := op.Name()
	if got != "test-provider" {
		t.Errorf("Name() = %q, want %q", got, "test-provider")
	}
}

func TestObservedProviderChat(t *testing.T) {
	want := oasis.ChatResponse{
		Content: "hello from LLM",
		Usage:   oasis.Usage{InputTokens: 10, OutputTokens: 5},
	}
	inner := &mockProvider{name: "p", chatResp: want}
	op := WrapProvider(inner, "m", testInstruments(t))

	got, err := op.Chat(context.Background(), oasis.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat returned unexpected error: %v", err)
	}
	if got.Content != want.Content {
		t.Errorf("Content = %q, want %q", got.Content, want.Content)
	}
	if got.Usage != want.Usage {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want.Usage)
	}
}

func TestObservedProviderChatError(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	inner := &mockProvider{name: "p", chatErr: wantErr}
	op := WrapProvider(inner, "m", testInstruments(t))

	_, err := op.Chat(context.Background(), oasis.ChatRequest{})
	if !errors.Is(err, wantErr) {
		t.Errorf("Chat error = %v, want %v", err, wantErr)
	}
}

func TestObservedProviderChatWithToolsOnRequest(t *testing.T) {
	want := oasis.ChatResponse{
		Content: "tool response",
		ToolCalls: []oasis.ToolCall{
			{ID: "call-1", Name: "search", Args: json.RawMessage(`{"q":"go"}`)},
		},
		Usage: oasis.Usage{InputTokens: 20, OutputTokens: 15},
	}
	inner := &mockProvider{name: "p", chatResp: want}
	op := WrapProvider(inner, "m", testInstruments(t))

	tools := []oasis.ToolDefinition{{Name: "search", Description: "search things"}}
	got, err := op.Chat(context.Background(), oasis.ChatRequest{Tools: tools})
	if err != nil {
		t.Fatalf("Chat with tools returned unexpected error: %v", err)
	}
	if got.Content != want.Content {
		t.Errorf("Content = %q, want %q", got.Content, want.Content)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("ToolCalls length = %d, want 1", len(got.ToolCalls))
	}
	if got.ToolCalls[0].Name != "search" {
		t.Errorf("ToolCalls[0].Name = %q, want %q", got.ToolCalls[0].Name, "search")
	}
	if got.Usage != want.Usage {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want.Usage)
	}
}

func TestObservedProviderChatStream(t *testing.T) {
	want := oasis.ChatResponse{
		Content: "hello world",
		Usage:   oasis.Usage{InputTokens: 8, OutputTokens: 2},
	}
	inner := &mockProvider{name: "p", chatResp: want}
	op := WrapProvider(inner, "m", testInstruments(t))

	ch := make(chan oasis.StreamEvent, 10)
	got, err := op.ChatStream(context.Background(), oasis.ChatRequest{}, ch)
	if err != nil {
		t.Fatalf("ChatStream returned unexpected error: %v", err)
	}

	// The wrapper's goroutine forwards events from the inner wrappedCh to our ch
	// and closes our ch when done. Collect all events.
	var events []oasis.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 2 {
		t.Fatalf("received %d events, want 2", len(events))
	}
	if events[0].Content != "hello" || events[1].Content != " world" {
		t.Errorf("events = %v, want [hello, ' world']", events)
	}
	if got.Content != want.Content {
		t.Errorf("Content = %q, want %q", got.Content, want.Content)
	}
	if got.Usage != want.Usage {
		t.Errorf("Usage = %+v, want %+v", got.Usage, want.Usage)
	}
}

func TestObservedProviderChatStreamUnbuffered(t *testing.T) {
	want := oasis.ChatResponse{
		Content: "hello world",
		Usage:   oasis.Usage{InputTokens: 8, OutputTokens: 2},
	}
	inner := &mockProvider{name: "p", chatResp: want}
	op := WrapProvider(inner, "m", testInstruments(t))

	// Use an unbuffered channel — previously this would deadlock because the
	// forwarding goroutine blocked on ch <- ev while ChatStream waited on <-done.
	ch := make(chan oasis.StreamEvent)

	// Must read from ch concurrently since it's unbuffered.
	var events []oasis.StreamEvent
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for ev := range ch {
			events = append(events, ev)
		}
	}()

	got, err := op.ChatStream(context.Background(), oasis.ChatRequest{}, ch)
	if err != nil {
		t.Fatalf("ChatStream returned unexpected error: %v", err)
	}
	<-readDone

	if len(events) != 2 {
		t.Fatalf("received %d events, want 2", len(events))
	}
	if got.Content != want.Content {
		t.Errorf("Content = %q, want %q", got.Content, want.Content)
	}
}

func TestObservedProviderChatStreamContextCancel(t *testing.T) {
	// mockStreamMany sends more events than the channel buffer can hold.
	manyEvents := &mockProviderManyEvents{
		name:     "p",
		chatResp: oasis.ChatResponse{Content: "partial"},
		count:    200,
	}
	op := WrapProvider(manyEvents, "m", testInstruments(t))

	ctx, cancel := context.WithCancel(context.Background())

	// Small buffer — goroutine will need to select on ctx.Done.
	ch := make(chan oasis.StreamEvent, 2)

	// Read a couple events then cancel.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		n := 0
		for range ch {
			n++
			if n == 2 {
				cancel()
			}
		}
	}()

	_, _ = op.ChatStream(ctx, oasis.ChatRequest{}, ch)
	<-readDone
}

// ---------------------------------------------------------------------------
// ObservedTool tests
// ---------------------------------------------------------------------------

func TestObservedToolDefinitions(t *testing.T) {
	defs := []oasis.ToolDefinition{
		{Name: "search", Description: "web search"},
		{Name: "calc", Description: "calculator"},
	}
	inner := &mockTool{defs: defs}
	ot := WrapTool(inner, testInstruments(t))

	got := ot.Definitions()
	if len(got) != len(defs) {
		t.Fatalf("Definitions length = %d, want %d", len(got), len(defs))
	}
	for i, d := range got {
		if d.Name != defs[i].Name {
			t.Errorf("Definitions[%d].Name = %q, want %q", i, d.Name, defs[i].Name)
		}
		if d.Description != defs[i].Description {
			t.Errorf("Definitions[%d].Description = %q, want %q", i, d.Description, defs[i].Description)
		}
	}
}

func TestObservedToolExecute(t *testing.T) {
	want := oasis.ToolResult{Content: "result data"}
	inner := &mockTool{result: want}
	ot := WrapTool(inner, testInstruments(t))

	got, err := ot.Execute(context.Background(), "search", json.RawMessage(`{"q":"test"}`))
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if got.Content != want.Content {
		t.Errorf("Content = %q, want %q", got.Content, want.Content)
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty", got.Error)
	}
}

func TestObservedToolExecuteError(t *testing.T) {
	wantErr := errors.New("tool broken")
	inner := &mockTool{err: wantErr}
	ot := WrapTool(inner, testInstruments(t))

	_, err := ot.Execute(context.Background(), "search", json.RawMessage(`{}`))
	if !errors.Is(err, wantErr) {
		t.Errorf("Execute error = %v, want %v", err, wantErr)
	}
}

// ---------------------------------------------------------------------------
// ObservedEmbedding tests
// ---------------------------------------------------------------------------

func TestObservedEmbeddingName(t *testing.T) {
	inner := &mockEmbedding{name: "embed-provider"}
	oe := WrapEmbedding(inner, "embed-model", testInstruments(t))

	got := oe.Name()
	if got != "embed-provider" {
		t.Errorf("Name() = %q, want %q", got, "embed-provider")
	}
}

func TestObservedEmbeddingDimensions(t *testing.T) {
	inner := &mockEmbedding{dims: 768}
	oe := WrapEmbedding(inner, "embed-model", testInstruments(t))

	got := oe.Dimensions()
	if got != 768 {
		t.Errorf("Dimensions() = %d, want %d", got, 768)
	}
}

func TestObservedEmbeddingEmbed(t *testing.T) {
	want := [][]float32{
		{0.1, 0.2, 0.3},
		{0.4, 0.5, 0.6},
	}
	inner := &mockEmbedding{name: "e", dims: 3, vecs: want}
	oe := WrapEmbedding(inner, "embed-model", testInstruments(t))

	got, err := oe.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed returned unexpected error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Embed returned %d vectors, want %d", len(got), len(want))
	}
	for i := range got {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("vector[%d] length = %d, want %d", i, len(got[i]), len(want[i]))
		}
		for j := range got[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("vector[%d][%d] = %f, want %f", i, j, got[i][j], want[i][j])
			}
		}
	}
}

func TestObservedEmbeddingEmbedError(t *testing.T) {
	wantErr := errors.New("embedding service down")
	inner := &mockEmbedding{name: "e", dims: 3, err: wantErr}
	oe := WrapEmbedding(inner, "embed-model", testInstruments(t))

	_, err := oe.Embed(context.Background(), []string{"test"})
	if !errors.Is(err, wantErr) {
		t.Errorf("Embed error = %v, want %v", err, wantErr)
	}
}

// ---------------------------------------------------------------------------
// NewTracer tests
// ---------------------------------------------------------------------------

func TestNewTracerReturnsTracer(t *testing.T) {
	tracer := NewTracer()
	if tracer == nil {
		t.Fatal("NewTracer() returned nil")
	}

	// Start a span and verify it returns non-nil context and span.
	ctx, span := tracer.Start(context.Background(), "test.span",
		oasis.StringAttr("key", "value"),
		oasis.IntAttr("count", 42))
	if ctx == nil {
		t.Fatal("Start() returned nil context")
	}
	if span == nil {
		t.Fatal("Start() returned nil span")
	}

	// Verify span operations don't panic.
	span.SetAttr(oasis.BoolAttr("ok", true))
	span.Event("test.event", oasis.Float64Attr("score", 0.95))
	span.End()
}

func TestNewTracerErrorSpan(t *testing.T) {
	tracer := NewTracer()
	_, span := tracer.Start(context.Background(), "test.error")

	// Verify Error doesn't panic.
	span.Error(errors.New("test error"))
	span.End()
}
