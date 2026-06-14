// memory/ingest_processors_test.go
package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestEnsureThread_CreatesNewThread(t *testing.T) {
	store := newConformanceStore(t)
	defer store.Close()
	in := &IngestContext{
		AgentName: "test",
		Task:      core.AgentTask{ThreadID: "t1", ChatID: "c1"},
		Store:     store,
		Logger:    discardLogger(),
	}
	if err := (EnsureThread{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !in.ThreadCreated {
		t.Fatal("ThreadCreated not set")
	}
	got, err := store.GetThread(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "t1" || got.ChatID != "c1" {
		t.Fatalf("thread = %+v", got)
	}
}

func TestPersistMessages_StoresBoth(t *testing.T) {
	store := newConformanceStore(t)
	defer store.Close()
	in := &IngestContext{
		Task:     core.AgentTask{ThreadID: "t1"},
		UserText: "u",
		AsstText: "a",
		Store:    store,
		Logger:   discardLogger(),
	}
	if err := (PersistMessages{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(store.messages["t1"]) != 2 {
		t.Fatalf("messages = %d", len(store.messages["t1"]))
	}
}

func TestEmbedder_BackfillsEmbeddings(t *testing.T) {
	emb := &fakeEmbedder{out: [][]float32{{1, 0, 0}, {0, 1, 0}}}
	in := &IngestContext{
		Candidates: []core.MemoryItem{
			{ID: "a", Content: "first"},
			{ID: "b", Content: "second"},
		},
		Embedding: emb,
		Logger:    discardLogger(),
	}
	if err := (Embedder{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(in.Candidates[0].Embedding) != 3 || len(in.Candidates[1].Embedding) != 3 {
		t.Fatalf("not backfilled: %+v", in.Candidates)
	}
}

type fakeEmbedder struct {
	out [][]float32
	err error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.out) != len(texts) {
		return nil, errors.New("size mismatch")
	}
	return f.out, nil
}

func (f *fakeEmbedder) Dimensions() int { return len(f.out[0]) }
func (f *fakeEmbedder) Name() string    { return "fake" }

func TestUpserter_WritesAllCandidates(t *testing.T) {
	store := newConformanceStore(t)
	in := &IngestContext{
		Candidates: []core.MemoryItem{
			{ID: "a", Kind: KindFact, Content: "x"},
			{ID: "b", Kind: KindFact, Content: "y"},
		},
		ItemStore: store,
		Logger:    discardLogger(),
	}
	if err := (Upserter{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(context.Background(), "a"); got.Content != "x" {
		t.Fatal("a not stored")
	}
	if got, _ := store.Get(context.Background(), "b"); got.Content != "y" {
		t.Fatal("b not stored")
	}
}

// --- fakeProvider implements core.Provider for LLM-driven processor tests ---

type fakeProvider struct {
	response string
	called   bool
}

func (f *fakeProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	f.called = true
	if ch != nil {
		close(ch)
	}
	return core.ChatResponse{Content: f.response}, nil
}

func (f *fakeProvider) Name() string { return "fake" }

// --- FactExtractor tests ---

func TestFactExtractor_EmitsCandidatesWithProvenance(t *testing.T) {
	provider := &fakeProvider{
		response: `[{"fact": "User's name is Nev", "category": "personal"}]`,
	}
	in := &IngestContext{
		AgentName: "alpha",
		Task:      core.AgentTask{ThreadID: "t1"},
		UserText:  "Hi, I'm Nev.",
		AsstText:  "Hi Nev!",
		Provider:  provider,
		Logger:    discardLogger(),
	}
	if err := (FactExtractor{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(in.Candidates) != 1 {
		t.Fatalf("candidates = %d", len(in.Candidates))
	}
	c := in.Candidates[0]
	if c.Kind != KindFact {
		t.Fatalf("kind = %v", c.Kind)
	}
	if c.Source.Kind != "extraction" {
		t.Fatalf("source.kind = %v", c.Source.Kind)
	}
	if c.Source.AgentID != "alpha" {
		t.Fatalf("source.agent = %v", c.Source.AgentID)
	}
}

func TestFactExtractor_SkipsTrivial(t *testing.T) {
	provider := &fakeProvider{}
	in := &IngestContext{UserText: "ok", Provider: provider, Logger: discardLogger()}
	_ = (FactExtractor{}).Process(context.Background(), in)
	if len(in.Candidates) != 0 {
		t.Fatal("trivial message extracted")
	}
	if provider.called {
		t.Fatal("provider called for trivial message")
	}
}

// --- Deduper tests ---

// panicEmbedder fails the test if Embed is ever called.
type panicEmbedder struct{ t *testing.T }

func (p *panicEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	p.t.Fatal("embed called unexpectedly")
	return nil, nil
}
func (p *panicEmbedder) Dimensions() int { return 3 }
func (p *panicEmbedder) Name() string    { return "panic" }

func TestDeduper_SkipsWhenNoSupersedes(t *testing.T) {
	store := newConformanceStore(t)
	defer store.Close()
	in := &IngestContext{
		Candidates: []core.MemoryItem{
			{ID: "a", Kind: KindFact, Content: "fact", Tags: []string{"category:personal"}},
		},
		ItemStore: store,
		Embedding: &panicEmbedder{t: t},
		Logger:    discardLogger(),
	}
	if err := (Deduper{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	// Test passes if panicEmbedder.Embed was never called.
}

// --- TitleGenerator tests ---

func TestTitleGenerator_SkipsWhenThreadNotCreated(t *testing.T) {
	provider := &fakeProvider{response: "A title"}
	store := newConformanceStore(t)
	defer store.Close()
	in := &IngestContext{
		ThreadCreated: false, // not a new thread
		Provider:      provider,
		Store:         store,
		Task:          core.AgentTask{ThreadID: "t1"},
		UserText:      "Hello world, this is a test message",
		Logger:        discardLogger(),
	}
	if err := (TitleGenerator{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if provider.called {
		t.Fatal("provider called when thread was not newly created")
	}
}

func TestTitleGenerator_SetsTitle(t *testing.T) {
	provider := &fakeProvider{response: "My first conversation"}
	store := newConformanceStore(t)
	defer store.Close()
	// Seed the thread so GetThread succeeds.
	_ = store.CreateThread(context.Background(), core.Thread{ID: "t1", ChatID: "c1"})
	in := &IngestContext{
		ThreadCreated: true,
		Provider:      provider,
		Store:         store,
		Task:          core.AgentTask{ThreadID: "t1"},
		UserText:      "Hello world, this is a test message",
		Logger:        discardLogger(),
	}
	if err := (TitleGenerator{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if !provider.called {
		t.Fatal("provider not called")
	}
	got, err := store.GetThread(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "My first conversation" {
		t.Fatalf("title = %q", got.Title)
	}
}

// --- EventRecorder tests ---

func TestEventRecorder_AppendsCandidate(t *testing.T) {
	in := &IngestContext{
		AgentName: "bot",
		Task:      core.AgentTask{ThreadID: "t1"},
		AsstText:  "Here is my response.",
		Logger:    discardLogger(),
	}
	if err := (EventRecorder{}).Process(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(in.Candidates) != 1 {
		t.Fatalf("candidates = %d", len(in.Candidates))
	}
	c := in.Candidates[0]
	if c.Kind != KindEvent {
		t.Fatalf("kind = %v", c.Kind)
	}
	if c.Content != "Here is my response." {
		t.Fatalf("content = %q", c.Content)
	}
}

func TestEventRecorder_SkipsEmptyAsstText(t *testing.T) {
	in := &IngestContext{AsstText: "", Logger: discardLogger()}
	_ = (EventRecorder{}).Process(context.Background(), in)
	if len(in.Candidates) != 0 {
		t.Fatal("candidate added for empty asst text")
	}
}
