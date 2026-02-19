package ingest

import (
	"context"
	"io"
	"strings"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// --- test doubles ---

type mockEmbedding struct {
	callCount int
	batchSizes []int
}

func (m *mockEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	m.callCount++
	m.batchSizes = append(m.batchSizes, len(texts))
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i] = make([]float32, 8)
	}
	return result, nil
}
func (m *mockEmbedding) Dimensions() int { return 8 }
func (m *mockEmbedding) Name() string    { return "mock" }

type mockStore struct {
	documents []oasis.Document
	chunks    []oasis.Chunk
}

func (s *mockStore) StoreDocument(_ context.Context, doc oasis.Document, chunks []oasis.Chunk) error {
	s.documents = append(s.documents, doc)
	s.chunks = append(s.chunks, chunks...)
	return nil
}

// Stubs for Store interface.
func (s *mockStore) StoreMessage(context.Context, oasis.Message) error   { return nil }
func (s *mockStore) GetMessages(context.Context, string, int) ([]oasis.Message, error) {
	return nil, nil
}
func (s *mockStore) SearchMessages(context.Context, []float32, int) ([]oasis.ScoredMessage, error) {
	return nil, nil
}
func (s *mockStore) SearchChunks(context.Context, []float32, int) ([]oasis.ScoredChunk, error) {
	return nil, nil
}
func (s *mockStore) GetChunksByIDs(context.Context, []string) ([]oasis.Chunk, error) {
	return nil, nil
}
func (s *mockStore) CreateThread(context.Context, oasis.Thread) error          { return nil }
func (s *mockStore) GetThread(context.Context, string) (oasis.Thread, error)   { return oasis.Thread{}, nil }
func (s *mockStore) ListThreads(context.Context, string, int) ([]oasis.Thread, error) {
	return nil, nil
}
func (s *mockStore) UpdateThread(context.Context, oasis.Thread) error { return nil }
func (s *mockStore) DeleteThread(context.Context, string) error       { return nil }
func (s *mockStore) GetConfig(context.Context, string) (string, error) { return "", nil }
func (s *mockStore) SetConfig(context.Context, string, string) error   { return nil }
func (s *mockStore) Init(context.Context) error                        { return nil }
func (s *mockStore) Close() error                                      { return nil }
func (s *mockStore) CreateScheduledAction(context.Context, oasis.ScheduledAction) error { return nil }
func (s *mockStore) ListScheduledActions(context.Context) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (s *mockStore) GetDueScheduledActions(context.Context, int64) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (s *mockStore) UpdateScheduledAction(context.Context, oasis.ScheduledAction) error { return nil }
func (s *mockStore) UpdateScheduledActionEnabled(context.Context, string, bool) error   { return nil }
func (s *mockStore) DeleteScheduledAction(context.Context, string) error                { return nil }
func (s *mockStore) DeleteAllScheduledActions(context.Context) (int, error)             { return 0, nil }
func (s *mockStore) FindScheduledActionsByDescription(context.Context, string) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (s *mockStore) CreateSkill(context.Context, oasis.Skill) error           { return nil }
func (s *mockStore) GetSkill(context.Context, string) (oasis.Skill, error)    { return oasis.Skill{}, nil }
func (s *mockStore) ListSkills(context.Context) ([]oasis.Skill, error)        { return nil, nil }
func (s *mockStore) UpdateSkill(context.Context, oasis.Skill) error           { return nil }
func (s *mockStore) DeleteSkill(context.Context, string) error                { return nil }
func (s *mockStore) SearchSkills(context.Context, []float32, int) ([]oasis.ScoredSkill, error) {
	return nil, nil
}

// --- tests ---

func TestIngestorIngestText(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}
	ing := NewIngestor(store, emb)

	r, err := ing.IngestText(context.Background(), "Hello, world!", "test", "Test Doc")
	if err != nil {
		t.Fatal(err)
	}
	if r.DocumentID == "" {
		t.Error("expected document ID")
	}
	if r.Document.Title != "Test Doc" {
		t.Errorf("wrong title: %s", r.Document.Title)
	}
	if r.ChunkCount != 1 {
		t.Errorf("expected 1 chunk, got %d", r.ChunkCount)
	}
	if len(store.documents) != 1 {
		t.Error("document not stored")
	}
	if len(store.chunks) != 1 {
		t.Error("chunk not stored")
	}
	// Chunk should have embedding.
	if len(store.chunks[0].Embedding) == 0 {
		t.Error("chunk missing embedding")
	}
}

func TestIngestorIngestFile(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}
	ing := NewIngestor(store, emb)

	r, err := ing.IngestFile(context.Background(), []byte("<p>Hello</p>"), "page.html")
	if err != nil {
		t.Fatal(err)
	}
	if r.Document.Title != "page.html" {
		t.Errorf("wrong title: %s", r.Document.Title)
	}
	if r.ChunkCount == 0 {
		t.Error("expected chunks")
	}
}

func TestIngestorIngestReader(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}
	ing := NewIngestor(store, emb)

	r, err := ing.IngestReader(context.Background(), io.NopCloser(strings.NewReader("test content")), "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if r.ChunkCount != 1 {
		t.Errorf("expected 1 chunk, got %d", r.ChunkCount)
	}
}

func TestIngestorBatchEmbedding(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}
	ing := NewIngestor(store, emb,
		WithBatchSize(2),
		WithChunker(NewRecursiveChunker(WithMaxTokens(25), WithOverlapTokens(0))),
	)

	// Create text with many paragraphs to produce >2 chunks.
	var parts []string
	for i := 0; i < 20; i++ {
		parts = append(parts, "This is paragraph number one with several words.")
	}
	text := strings.Join(parts, "\n\n")

	r, err := ing.IngestText(context.Background(), text, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ChunkCount <= 2 {
		t.Fatalf("expected >2 chunks for batching test, got %d", r.ChunkCount)
	}
	// With batch size 2, we should have multiple embed calls.
	if emb.callCount < 2 {
		t.Errorf("expected multiple embed batches, got %d calls", emb.callCount)
	}
}

func TestIngestorParentChildStrategy(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}
	ing := NewIngestor(store, emb,
		WithStrategy(StrategyParentChild),
		WithParentTokens(50),  // 200 chars
		WithChildTokens(25),   // 100 chars
	)

	text := strings.Repeat("This is a sentence. ", 50)
	r, err := ing.IngestText(context.Background(), text, "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ChunkCount == 0 {
		t.Fatal("expected chunks")
	}

	// Check that we have both parent chunks (no embedding, no parent_id)
	// and child chunks (with embedding and parent_id).
	hasParent := false
	hasChild := false
	for _, c := range store.chunks {
		if c.ParentID == "" && len(c.Embedding) == 0 {
			hasParent = true
		}
		if c.ParentID != "" && len(c.Embedding) > 0 {
			hasChild = true
		}
	}
	if !hasParent {
		t.Error("expected parent chunks (no embedding, no parent_id)")
	}
	if !hasChild {
		t.Error("expected child chunks (with embedding and parent_id)")
	}
}

func TestIngestorCustomExtractor(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}

	customType := ContentType("text/custom")
	custom := PlainTextExtractor{} // just reuse plain text for testing

	ing := NewIngestor(store, emb, WithExtractor(customType, custom))

	// Verify the extractor was registered.
	if _, ok := ing.extractors[customType]; !ok {
		t.Error("custom extractor not registered")
	}
}

func TestIngestorWithChunker(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}
	rc := NewRecursiveChunker(WithMaxTokens(100))

	ing := NewIngestor(store, emb, WithChunker(rc))
	r, err := ing.IngestText(context.Background(), "Hello", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ChunkCount != 1 {
		t.Errorf("expected 1 chunk, got %d", r.ChunkCount)
	}
}

// --- integration tests: metadata flow ---

// metadataExtractorMock implements both Extractor and MetadataExtractor.
type metadataExtractorMock struct {
	result ExtractResult
}

func (m *metadataExtractorMock) Extract(content []byte) (string, error) {
	return m.result.Text, nil
}

func (m *metadataExtractorMock) ExtractWithMeta(content []byte) (ExtractResult, error) {
	return m.result, nil
}

func TestIngestorMetadataExtractor(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}

	ext := &metadataExtractorMock{
		result: ExtractResult{
			Text: "Page one content.\n\nPage two content.",
			Meta: []PageMeta{
				{PageNumber: 1, Heading: "Intro", StartByte: 0, EndByte: 17},
				{PageNumber: 2, Heading: "Details", StartByte: 19, EndByte: 36},
			},
		},
	}

	// Register under TypeCSV so IngestFile finds it via extension mapping.
	ing := NewIngestor(store, emb, WithExtractor(TypeCSV, ext))

	r, err := ing.IngestFile(context.Background(), []byte("dummy"), "doc.csv")
	if err != nil {
		t.Fatal(err)
	}
	if r.ChunkCount == 0 {
		t.Fatal("expected chunks")
	}

	// Verify metadata was assigned to chunks.
	for _, c := range store.chunks {
		if c.Metadata == nil {
			t.Fatal("expected metadata on chunk")
		}
		if c.Metadata.SourceURL != "doc.csv" {
			t.Errorf("expected source URL doc.csv, got %q", c.Metadata.SourceURL)
		}
		// At least one chunk should have page metadata.
		if c.Metadata.PageNumber > 0 || c.Metadata.SectionHeading != "" {
			return // success — at least one chunk got page-level metadata
		}
	}
	t.Error("no chunk received page-level metadata")
}

func TestIngestorCSVEndToEnd(t *testing.T) {
	store := &mockStore{}
	emb := &mockEmbedding{}

	// Register plain text extractor for CSV type (CSV extractor is in a
	// sub-package we can't import due to cycle, but the metadata wiring
	// is the same regardless of extractor).
	ing := NewIngestor(store, emb,
		WithExtractor(TypeCSV, PlainTextExtractor{}),
	)

	csvData := []byte("Name,Age\nJohn,30\nJane,25\n")
	r, err := ing.IngestFile(context.Background(), csvData, "data.csv")
	if err != nil {
		t.Fatal(err)
	}
	if r.ChunkCount == 0 {
		t.Fatal("expected chunks")
	}

	// Verify source URL is set on chunks via metadata.
	for _, c := range store.chunks {
		if c.Metadata == nil || c.Metadata.SourceURL != "data.csv" {
			t.Errorf("expected source URL data.csv on chunk metadata, got %+v", c.Metadata)
		}
	}
}
