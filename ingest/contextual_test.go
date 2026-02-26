package ingest

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// mockContextProvider returns a canned context prefix for each chunk.
type mockContextProvider struct {
	prefix string
	calls  atomic.Int32
	onChat func()
}

func (m *mockContextProvider) Chat(_ context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
	m.calls.Add(1)
	if m.onChat != nil {
		m.onChat()
	}
	return oasis.ChatResponse{Content: m.prefix}, nil
}

func (m *mockContextProvider) ChatStream(_ context.Context, _ oasis.ChatRequest, _ chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{}, fmt.Errorf("not implemented")
}

func (m *mockContextProvider) Name() string { return "mock-context" }

// mockErrorProvider always returns an error.
type mockErrorProvider struct{}

func (m *mockErrorProvider) Chat(_ context.Context, _ oasis.ChatRequest) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{}, fmt.Errorf("llm unavailable")
}
func (m *mockErrorProvider) ChatStream(_ context.Context, _ oasis.ChatRequest, _ chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{}, fmt.Errorf("not implemented")
}
func (m *mockErrorProvider) Name() string { return "mock-error" }

func TestEnrichChunksWithContext(t *testing.T) {
	chunks := []oasis.Chunk{
		{ID: "c1", Content: "Go is a programming language."},
		{ID: "c2", Content: "Go supports concurrency."},
	}
	provider := &mockContextProvider{prefix: "This is about Go."}

	enrichChunksWithContext(context.Background(), provider, chunks, "Full document about Go.", 3, nil)

	for i, c := range chunks {
		if !strings.HasPrefix(c.Content, "This is about Go.\n\n") {
			t.Errorf("chunks[%d].Content missing prefix: %q", i, c.Content)
		}
	}
	if provider.calls.Load() != 2 {
		t.Errorf("got %d LLM calls, want 2", provider.calls.Load())
	}
}

func TestEnrichChunksWithContext_GracefulDegradation(t *testing.T) {
	original := "Original content."
	chunks := []oasis.Chunk{
		{ID: "c1", Content: original},
	}
	provider := &mockErrorProvider{}

	enrichChunksWithContext(context.Background(), provider, chunks, "doc", 1, nil)

	if chunks[0].Content != original {
		t.Errorf("chunk content changed on error: got %q, want %q", chunks[0].Content, original)
	}
}

func TestEnrichChunksWithContext_CancelledContext(t *testing.T) {
	chunks := []oasis.Chunk{
		{ID: "c1", Content: "A"},
		{ID: "c2", Content: "B"},
		{ID: "c3", Content: "C"},
	}
	provider := &mockContextProvider{prefix: "context"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	enrichChunksWithContext(ctx, provider, chunks, "doc", 1, nil)

	// All chunks should retain original content (no LLM calls made).
	for i, c := range chunks {
		if strings.Contains(c.Content, "context") {
			t.Errorf("chunks[%d] was enriched despite cancelled context", i)
		}
	}
}

func TestEnrichChunksWithContext_EmptyChunks(t *testing.T) {
	provider := &mockContextProvider{prefix: "ctx"}
	enrichChunksWithContext(context.Background(), provider, nil, "doc", 3, nil)
	if provider.calls.Load() != 0 {
		t.Errorf("got %d calls for empty chunks, want 0", provider.calls.Load())
	}
}

func TestIngestorContextualEnrichment_Flat(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}
	provider := &mockContextProvider{prefix: "Added context."}

	ing := NewIngestor(store, emb,
		WithContextualEnrichment(provider),
		WithContextWorkers(2),
	)

	_, err := ing.IngestText(context.Background(), "Hello world. This is a test.", "src", "title")
	if err != nil {
		t.Fatal(err)
	}

	if len(store.chunks) == 0 {
		t.Fatal("no chunks stored")
	}
	for i, c := range store.chunks {
		if !strings.HasPrefix(c.Content, "Added context.\n\n") {
			t.Errorf("chunk[%d] missing contextual prefix: %q", i, c.Content)
		}
	}
}

func TestIngestorContextualEnrichment_ParentChild(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}
	provider := &mockContextProvider{prefix: "Child context."}

	// Create enough text to generate parent + child chunks.
	text := strings.Repeat("This is a sentence for testing purposes. ", 200)

	ing := NewIngestor(store, emb,
		WithStrategy(StrategyParentChild),
		WithContextualEnrichment(provider),
	)

	_, err := ing.IngestText(context.Background(), text, "src", "title")
	if err != nil {
		t.Fatal(err)
	}

	var hasParent, hasChild bool
	for _, c := range store.chunks {
		if c.ParentID == "" && len(c.Embedding) == 0 {
			// Parent chunk — should NOT have contextual prefix.
			if strings.HasPrefix(c.Content, "Child context.") {
				t.Errorf("parent chunk has contextual prefix: %q", c.Content[:60])
			}
			hasParent = true
		}
		if c.ParentID != "" && len(c.Embedding) > 0 {
			// Child chunk — SHOULD have contextual prefix.
			if !strings.HasPrefix(c.Content, "Child context.\n\n") {
				t.Errorf("child chunk missing prefix: %q", c.Content[:min(60, len(c.Content))])
			}
			hasChild = true
		}
	}
	if !hasParent {
		t.Error("no parent chunks found")
	}
	if !hasChild {
		t.Error("no child chunks found")
	}
}

func TestIngestorNoContextualEnrichment(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}

	// No WithContextualEnrichment — should not modify chunks.
	ing := NewIngestor(store, emb)

	_, err := ing.IngestText(context.Background(), "Hello world.", "src", "title")
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range store.chunks {
		if strings.Contains(c.Content, "\n\n") {
			t.Errorf("chunk has unexpected prefix separator: %q", c.Content)
		}
	}
}

func TestTruncateDocText(t *testing.T) {
	text := "hello world this is a test document"

	// Truncate at 11 bytes → "hello world" (word boundary)
	got := truncateDocText(text, 11)
	if got != "hello world" {
		t.Errorf("truncateDocText(11) = %q, want %q", got, "hello world")
	}

	// Truncate at 15 → "hello world thi" (mid-word "this" excluded)
	got = truncateDocText(text, 15)
	if len(got) > 15 {
		t.Errorf("truncateDocText(15) = %q (len %d), exceeds limit", got, len(got))
	}

	// No truncation needed
	got = truncateDocText(text, 1000)
	if got != text {
		t.Errorf("truncateDocText(1000) = %q, want original", got)
	}

	// Zero limit → no truncation
	got = truncateDocText(text, 0)
	if got != text {
		t.Errorf("truncateDocText(0) = %q, want original", got)
	}
}
