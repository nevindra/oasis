package ingest

import (
	"context"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// fakeMultimodalEmb returns fixed vectors.
type fakeMultimodalEmb struct {
	calls [][]oasis.MultimodalInput
}

func (f *fakeMultimodalEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vecs[i] = []float32{0.1, 0.2, 0.3}
	}
	return vecs, nil
}

func (f *fakeMultimodalEmb) Dimensions() int { return 3 }
func (f *fakeMultimodalEmb) Name() string    { return "fake" }

func (f *fakeMultimodalEmb) EmbedMultimodal(_ context.Context, inputs []oasis.MultimodalInput) ([][]float32, error) {
	f.calls = append(f.calls, inputs)
	vecs := make([][]float32, len(inputs))
	for i := range inputs {
		vecs[i] = []float32{0.4, 0.5, 0.6}
	}
	return vecs, nil
}

type ingestFakeBlobStore struct{}

func (ingestFakeBlobStore) StoreBlob(_ context.Context, key string, _ []byte, _ string) (string, error) {
	return "blob://" + key, nil
}
func (ingestFakeBlobStore) GetBlob(_ context.Context, _ string) ([]byte, string, error) {
	return nil, "", nil
}
func (ingestFakeBlobStore) DeleteBlob(_ context.Context, _ string) error { return nil }

// fakeImageStore is a minimal store for image embedding tests.
type fakeImageStore struct {
	docs   []oasis.Document
	chunks []oasis.Chunk
}

func (f *fakeImageStore) StoreDocument(_ context.Context, doc oasis.Document, chunks []oasis.Chunk) error {
	f.docs = append(f.docs, doc)
	f.chunks = append(f.chunks, chunks...)
	return nil
}

func (f *fakeImageStore) CreateThread(context.Context, oasis.Thread) error                     { return nil }
func (f *fakeImageStore) GetThread(context.Context, string) (oasis.Thread, error)              { return oasis.Thread{}, nil }
func (f *fakeImageStore) ListThreads(context.Context, string, int) ([]oasis.Thread, error)     { return nil, nil }
func (f *fakeImageStore) UpdateThread(context.Context, oasis.Thread) error                     { return nil }
func (f *fakeImageStore) DeleteThread(context.Context, string) error                           { return nil }
func (f *fakeImageStore) StoreMessage(context.Context, oasis.Message) error                    { return nil }
func (f *fakeImageStore) GetMessages(context.Context, string, int) ([]oasis.Message, error)    { return nil, nil }
func (f *fakeImageStore) SearchMessages(context.Context, []float32, int) ([]oasis.ScoredMessage, error) {
	return nil, nil
}
func (f *fakeImageStore) ListDocuments(context.Context, int) ([]oasis.Document, error) { return nil, nil }
func (f *fakeImageStore) DeleteDocument(context.Context, string) error                 { return nil }
func (f *fakeImageStore) SearchChunks(context.Context, []float32, int, ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	return nil, nil
}
func (f *fakeImageStore) GetChunksByIDs(context.Context, []string) ([]oasis.Chunk, error)  { return nil, nil }
func (f *fakeImageStore) GetConfig(context.Context, string) (string, error)                { return "", nil }
func (f *fakeImageStore) SetConfig(context.Context, string, string) error                  { return nil }
func (f *fakeImageStore) CreateScheduledAction(context.Context, oasis.ScheduledAction) error { return nil }
func (f *fakeImageStore) ListScheduledActions(context.Context) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (f *fakeImageStore) GetDueScheduledActions(context.Context, int64) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (f *fakeImageStore) UpdateScheduledAction(context.Context, oasis.ScheduledAction) error { return nil }
func (f *fakeImageStore) UpdateScheduledActionEnabled(context.Context, string, bool) error   { return nil }
func (f *fakeImageStore) DeleteScheduledAction(context.Context, string) error                { return nil }
func (f *fakeImageStore) DeleteAllScheduledActions(context.Context) (int, error)             { return 0, nil }
func (f *fakeImageStore) FindScheduledActionsByDescription(context.Context, string) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (f *fakeImageStore) CreateSkill(context.Context, oasis.Skill) error                      { return nil }
func (f *fakeImageStore) GetSkill(context.Context, string) (oasis.Skill, error)               { return oasis.Skill{}, nil }
func (f *fakeImageStore) ListSkills(context.Context) ([]oasis.Skill, error)                   { return nil, nil }
func (f *fakeImageStore) UpdateSkill(context.Context, oasis.Skill) error                      { return nil }
func (f *fakeImageStore) DeleteSkill(context.Context, string) error                           { return nil }
func (f *fakeImageStore) SearchSkills(context.Context, []float32, int) ([]oasis.ScoredSkill, error) {
	return nil, nil
}
func (f *fakeImageStore) Init(context.Context) error { return nil }
func (f *fakeImageStore) Close() error               { return nil }

func TestWithImageEmbedding_Option(t *testing.T) {
	emb := &fakeMultimodalEmb{}
	ing := NewIngestor(nil, emb, WithImageEmbedding(emb))
	if ing.imageEmbedding == nil {
		t.Fatal("expected imageEmbedding to be set")
	}
}

func TestWithImageEmbedding_WithBlobStore(t *testing.T) {
	emb := &fakeMultimodalEmb{}
	bs := ingestFakeBlobStore{}
	ing := NewIngestor(nil, emb, WithImageEmbedding(emb), WithBlobStore(bs))
	if ing.blobStore == nil {
		t.Fatal("expected blobStore to be set")
	}
}

func TestImageChunkCreation_IngestFile(t *testing.T) {
	store := &fakeImageStore{}
	emb := &fakeMultimodalEmb{}

	ing := NewIngestor(store, emb, WithImageEmbedding(emb))

	// Ingest a DOCX with an embedded image.
	docx := buildTestDocxWithImage(t, "photo.png", []byte{0x89, 0x50, 0x4E, 0x47})
	result, err := ing.IngestFile(context.Background(), docx, "test.docx")
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if result.ChunkCount == 0 {
		t.Fatal("expected at least one chunk")
	}

	// Find image chunks.
	var imageChunks []oasis.Chunk
	for _, c := range store.chunks {
		if c.Metadata != nil && c.Metadata.ContentType == "image" {
			imageChunks = append(imageChunks, c)
		}
	}
	if len(imageChunks) == 0 {
		t.Fatal("expected at least one image chunk")
	}

	// Image chunk should have an embedding from the multimodal provider.
	for _, ic := range imageChunks {
		if len(ic.Embedding) == 0 {
			t.Error("image chunk missing embedding")
		}
		if ic.Metadata == nil {
			t.Fatal("image chunk missing metadata")
		}
		if ic.Metadata.ContentType != "image" {
			t.Errorf("expected content_type 'image', got %q", ic.Metadata.ContentType)
		}
		// Image data should be in metadata.
		if len(ic.Metadata.Images) == 0 {
			t.Error("image chunk missing inline image data")
		}
	}

	// Verify EmbedMultimodal was called.
	if len(emb.calls) == 0 {
		t.Error("expected EmbedMultimodal to be called for image chunks")
	}
}

func TestImageChunkCreation_WithBlobStore(t *testing.T) {
	store := &fakeImageStore{}
	emb := &fakeMultimodalEmb{}
	blobs := &trackingBlobStore{stored: make(map[string][]byte)}

	ing := NewIngestor(store, emb, WithImageEmbedding(emb), WithBlobStore(blobs))

	docx := buildTestDocxWithImage(t, "photo.png", []byte{0x89, 0x50, 0x4E, 0x47})
	_, err := ing.IngestFile(context.Background(), docx, "test.docx")
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	// Image chunks should use blob refs, not inline images.
	for _, c := range store.chunks {
		if c.Metadata != nil && c.Metadata.ContentType == "image" {
			if c.Metadata.BlobRef == "" {
				t.Error("expected blob_ref to be set when BlobStore is configured")
			}
			if len(c.Metadata.Images) > 0 {
				t.Error("expected no inline images when BlobStore is configured")
			}
		}
	}

	if len(blobs.stored) == 0 {
		t.Error("expected BlobStore.StoreBlob to be called")
	}
}

type trackingBlobStore struct {
	stored map[string][]byte
}

func (b *trackingBlobStore) StoreBlob(_ context.Context, key string, data []byte, _ string) (string, error) {
	b.stored[key] = data
	return "blob://" + key, nil
}
func (b *trackingBlobStore) GetBlob(_ context.Context, _ string) ([]byte, string, error) {
	return nil, "", nil
}
func (b *trackingBlobStore) DeleteBlob(_ context.Context, _ string) error { return nil }

func TestImageChunkCreation_NoImages(t *testing.T) {
	store := &fakeImageStore{}
	emb := &fakeMultimodalEmb{}

	ing := NewIngestor(store, emb, WithImageEmbedding(emb))

	// Plain text has no images — no image chunks should be created.
	_, err := ing.IngestText(context.Background(), "hello world", "test.txt", "test")
	if err != nil {
		t.Fatalf("IngestText: %v", err)
	}

	for _, c := range store.chunks {
		if c.Metadata != nil && c.Metadata.ContentType == "image" {
			t.Error("unexpected image chunk for plain text ingest")
		}
	}

	// EmbedMultimodal should not have been called.
	if len(emb.calls) > 0 {
		t.Error("EmbedMultimodal should not be called when no images exist")
	}
}

func TestImageChunkCreation_Disabled(t *testing.T) {
	store := &fakeImageStore{}
	emb := &fakeMultimodalEmb{}

	// No WithImageEmbedding — image chunks should not be created.
	ing := NewIngestor(store, emb)

	docx := buildTestDocxWithImage(t, "photo.png", []byte{0x89, 0x50, 0x4E, 0x47})
	_, err := ing.IngestFile(context.Background(), docx, "test.docx")
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	for _, c := range store.chunks {
		if c.Metadata != nil && c.Metadata.ContentType == "image" {
			t.Error("unexpected image chunk when imageEmbedding is not set")
		}
	}
}
