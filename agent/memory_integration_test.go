package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
)

// --- Test doubles for memory wiring ---

// stubStore is a no-op implementation of core.Store + core.MemoryItemStore
// for embedding in test doubles.
type stubStore struct{}

func (s *stubStore) Init(_ context.Context) error                        { return nil }
func (s *stubStore) Close() error                                        { return nil }
func (s *stubStore) CreateThread(_ context.Context, _ core.Thread) error { return nil }
func (s *stubStore) GetThread(_ context.Context, _ string) (core.Thread, error) {
	return core.Thread{}, nil
}
func (s *stubStore) ListThreads(_ context.Context, _ string, _ int) ([]core.Thread, error) {
	return nil, nil
}
func (s *stubStore) UpdateThread(_ context.Context, _ core.Thread) error  { return nil }
func (s *stubStore) DeleteThread(_ context.Context, _ string) error       { return nil }
func (s *stubStore) StoreMessage(_ context.Context, _ core.Message) error { return nil }
func (s *stubStore) GetMessages(_ context.Context, _ string, _ int) ([]core.Message, error) {
	return nil, nil
}
func (s *stubStore) SearchMessages(_ context.Context, _ []float32, _ int, _ string) ([]core.ScoredMessage, error) {
	return nil, nil
}
func (s *stubStore) StoreDocument(_ context.Context, _ core.Document, _ []core.Chunk) error {
	return nil
}
func (s *stubStore) ListDocuments(_ context.Context, _ int) ([]core.Document, error) { return nil, nil }
func (s *stubStore) DeleteDocument(_ context.Context, _ string) error                { return nil }
func (s *stubStore) SearchChunks(_ context.Context, _ []float32, _ int, _ ...core.ChunkFilter) ([]core.ScoredChunk, error) {
	return nil, nil
}
func (s *stubStore) GetChunksByIDs(_ context.Context, _ []string) ([]core.Chunk, error) {
	return nil, nil
}
func (s *stubStore) GetConfig(_ context.Context, _ string) (string, error) { return "", nil }
func (s *stubStore) SetConfig(_ context.Context, _, _ string) error        { return nil }
func (s *stubStore) CreateScheduledAction(_ context.Context, _ core.ScheduledAction) error {
	return nil
}
func (s *stubStore) ListScheduledActions(_ context.Context) ([]core.ScheduledAction, error) {
	return nil, nil
}
func (s *stubStore) GetDueScheduledActions(_ context.Context, _ int64) ([]core.ScheduledAction, error) {
	return nil, nil
}
func (s *stubStore) UpdateScheduledAction(_ context.Context, _ core.ScheduledAction) error {
	return nil
}
func (s *stubStore) UpdateScheduledActionEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}
func (s *stubStore) DeleteScheduledAction(_ context.Context, _ string) error  { return nil }
func (s *stubStore) DeleteAllScheduledActions(_ context.Context) (int, error) { return 0, nil }
func (s *stubStore) ListScheduledActionsByDescription(_ context.Context, _ string) ([]core.ScheduledAction, error) {
	return nil, nil
}

// core.MemoryItemStore methods (zero-valued no-ops).
func (s *stubStore) Upsert(_ context.Context, _ core.MemoryItem) error               { return nil }
func (s *stubStore) UpsertBatch(_ context.Context, _ []core.MemoryItem) error        { return nil }
func (s *stubStore) Delete(_ context.Context, _ string) error                        { return nil }
func (s *stubStore) DeleteWhere(_ context.Context, _ core.MemoryFilter) (int, error) { return 0, nil }
func (s *stubStore) Get(_ context.Context, _ string) (core.MemoryItem, error) {
	return core.MemoryItem{}, nil
}
func (s *stubStore) List(_ context.Context, _ core.MemoryFilter) ([]core.MemoryItem, error) {
	return nil, nil
}
func (s *stubStore) SearchSemantic(_ context.Context, _ []float32, _ core.MemoryFilter, _ int) ([]core.ScoredMemoryItem, error) {
	return nil, nil
}

// Verify stubStore satisfies core.Store + core.MemoryItemStore at compile time.
var (
	_ core.Store           = (*stubStore)(nil)
	_ core.MemoryItemStore = (*stubStore)(nil)
)

// recordingStore tracks calls to StoreMessage, CreateThread, UpdateThread
// and returns canned history.
type recordingStore struct {
	stubStore
	mu             sync.Mutex
	history        []core.Message         // returned by GetMessages
	related        []core.ScoredMessage   // returned by SearchMessages
	stored         []core.Message         // recorded by StoreMessage
	threads        map[string]core.Thread // tracked threads (for GetThread)
	createdThreads []core.Thread          // recorded by CreateThread
	updatedThreads []core.Thread          // recorded by UpdateThread
}

func (s *recordingStore) GetMessages(_ context.Context, _ string, limit int) ([]core.Message, error) {
	if limit > 0 && limit < len(s.history) {
		return s.history[len(s.history)-limit:], nil
	}
	return s.history, nil
}

func (s *recordingStore) SearchMessages(_ context.Context, _ []float32, _ int, chatID string) ([]core.ScoredMessage, error) {
	if chatID == "" {
		return s.related, nil
	}
	// Mirror real-store JOIN semantics: only return messages whose thread
	// belongs to the given chat.
	out := make([]core.ScoredMessage, 0, len(s.related))
	for _, r := range s.related {
		t, ok := s.threads[r.ThreadID]
		if !ok || t.ChatID != chatID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *recordingStore) StoreMessage(_ context.Context, msg core.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored = append(s.stored, msg)
	return nil
}

func (s *recordingStore) CreateThread(_ context.Context, t core.Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads == nil {
		s.threads = make(map[string]core.Thread)
	}
	s.threads[t.ID] = t
	s.createdThreads = append(s.createdThreads, t)
	return nil
}

func (s *recordingStore) GetThread(_ context.Context, id string) (core.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads != nil {
		if t, ok := s.threads[id]; ok {
			return t, nil
		}
	}
	return core.Thread{}, fmt.Errorf("get thread: not found")
}

func (s *recordingStore) UpdateThread(_ context.Context, t core.Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updatedThreads = append(s.updatedThreads, t)
	if s.threads != nil {
		if existing, ok := s.threads[t.ID]; ok {
			if t.Title != "" {
				existing.Title = t.Title
			}
			existing.UpdatedAt = t.UpdatedAt
			s.threads[t.ID] = existing
		}
	}
	return nil
}

func (s *recordingStore) storedMessages() []core.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]core.Message, len(s.stored))
	copy(cp, s.stored)
	return cp
}

// stubEmbedding returns zero vectors.
type stubEmbedding struct{}

func (e *stubEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, 4)
	}
	return out, nil
}
func (e *stubEmbedding) Dimensions() int { return 4 }
func (e *stubEmbedding) Name() string    { return "stub" }

// --- Tests ---

func TestLLMAgentStatelessWithoutMemory(t *testing.T) {
	// Without memory options, agent should behave exactly as before
	provider := &mockProvider{
		name:      "test",
		responses: []core.ChatResponse{{Content: "hello"}},
	}
	agent := New("test", "test", provider)
	result, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "hello" {
		t.Errorf("Output = %q, want %q", result.Output, "hello")
	}
}

func TestLLMAgentConversationMemory(t *testing.T) {
	store := &recordingStore{
		history: []core.Message{
			{Role: "user", Content: "earlier question"},
			{Role: "assistant", Content: "earlier answer"},
		},
	}

	provider := &mockProvider{
		name:      "test",
		responses: []core.ChatResponse{{Content: "response with history"}},
	}

	agent := New("test", "test", provider,
		WithMemory(memory.WithStore(store)),
		WithPrompt("You are helpful"),
	)

	task := AgentTask{
		Input:    "new question",
		ThreadID: "thread-1",
	}
	result, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "response with history" {
		t.Errorf("Output = %q, want %q", result.Output, "response with history")
	}

	// Wait for background persist goroutine
	time.Sleep(50 * time.Millisecond)

	stored := store.storedMessages()
	if len(stored) < 2 {
		t.Fatalf("expected at least 2 stored messages, got %d", len(stored))
	}

	// Verify user message persisted
	if stored[0].Role != "user" || stored[0].Content != "new question" {
		t.Errorf("first stored = %q/%q, want user/new question", stored[0].Role, stored[0].Content)
	}
	// Verify assistant message persisted
	found := false
	for _, m := range stored {
		if m.Role == "assistant" && m.Content == "response with history" {
			found = true
			break
		}
	}
	if !found {
		t.Error("assistant message not persisted")
	}
}

// limitCapturingStore records the limit passed to GetMessages.
type limitCapturingStore struct {
	stubStore
	capturedLimit int
}

func (s *limitCapturingStore) GetMessages(_ context.Context, _ string, limit int) ([]core.Message, error) {
	s.capturedLimit = limit
	return nil, nil
}

func TestMaxHistoryOption(t *testing.T) {
	tests := []struct {
		name      string
		opt       memory.Option
		wantLimit int
	}{
		{"default", nil, 10},
		{"custom", memory.WithHistory(memory.HistoryConfig{MaxMessages: 50}), 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &limitCapturingStore{}
			provider := &mockProvider{
				name:      "test",
				responses: []core.ChatResponse{{Content: "ok"}},
			}

			memOpts := []memory.Option{memory.WithStore(store)}
			if tt.opt != nil {
				memOpts = append(memOpts, tt.opt)
			}
			agent := New("test", "test", provider, WithMemory(memOpts...))

			_, err := agent.Execute(context.Background(), AgentTask{
				Input:    "hi",
				ThreadID: "t1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if store.capturedLimit != tt.wantLimit {
				t.Errorf("GetMessages limit = %d, want %d", store.capturedLimit, tt.wantLimit)
			}
		})
	}
}

func TestLLMAgentNoThreadIDSkipsHistory(t *testing.T) {
	store := &recordingStore{
		history: []core.Message{{Role: "user", Content: "should not appear"}},
	}

	provider := &mockProvider{
		name:      "test",
		responses: []core.ChatResponse{{Content: "ok"}},
	}

	agent := New("test", "test", provider,
		WithMemory(memory.WithStore(store)),
	)

	// No ThreadID — history should be skipped.
	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for any background work that won't happen — no persist without thread.
	time.Sleep(50 * time.Millisecond)

	if len(store.storedMessages()) != 0 {
		t.Error("should not persist messages without thread_id")
	}
}

func TestAgentConversationMemoryPersists(t *testing.T) {
	store := &recordingStore{
		history: []core.Message{
			{Role: "user", Content: "earlier"},
			{Role: "assistant", Content: "earlier reply"},
		},
	}

	provider := &mockProvider{
		name:      "router",
		responses: []core.ChatResponse{{Content: "agent response"}},
	}

	agent := New("net", "test", provider,
		WithMemory(memory.WithStore(store)),
	)

	task := AgentTask{
		Input:    "new input",
		ThreadID: "t1",
	}
	result, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "agent response" {
		t.Errorf("Output = %q, want %q", result.Output, "agent response")
	}

	// Wait for background persist
	time.Sleep(50 * time.Millisecond)
	stored := store.storedMessages()
	if len(stored) < 2 {
		t.Fatalf("expected at least 2 stored messages, got %d", len(stored))
	}
}

func TestBuildMessagesImagesFromTask(t *testing.T) {
	images := []core.Attachment{
		mustAttachmentBase64(t, "image/jpeg", "YWJjMTIz"),
		mustAttachmentBase64(t, "application/pdf", "cGRmZGF0YQ=="),
	}

	provider := &capturingProvider{resp: core.ChatResponse{Content: "ok"}}

	agent := New("test", "test", provider)
	_, err := agent.Execute(context.Background(), AgentTask{
		Input:       "analyze this",
		Attachments: images,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Last message should be the user message with images attached.
	msgs := provider.firstCall().Messages
	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Fatalf("last message role = %q, want user", last.Role)
	}
	if last.Content != "analyze this" {
		t.Errorf("user message content = %q, want %q", last.Content, "analyze this")
	}
	if len(last.Attachments) != 2 {
		t.Fatalf("user message images count = %d, want 2", len(last.Attachments))
	}
	if last.Attachments[0].MimeType != "image/jpeg" || string(last.Attachments[0].Data) != "abc123" {
		t.Errorf("first image = %+v, want {image/jpeg, abc123}", last.Attachments[0])
	}
	if last.Attachments[1].MimeType != "application/pdf" || string(last.Attachments[1].Data) != "pdfdata" {
		t.Errorf("second image = %+v, want {application/pdf, pdfdata}", last.Attachments[1])
	}
}

// --- Helpers ---

// capturingProvider records all ChatRequests for inspection.
// core.Thread-safe: auto-extraction calls the provider from a background goroutine.
// If extractionResp is set, the second call returns it instead of resp.
type capturingProvider struct {
	resp           core.ChatResponse
	extractionResp *core.ChatResponse // returned on 2nd+ call if non-nil
	mu             sync.Mutex
	reqs           []core.ChatRequest
}

func (p *capturingProvider) Name() string { return "capturing" }

func (p *capturingProvider) record(req core.ChatRequest) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
}

// firstCall returns the first captured request (the main LLM call, not extraction).
func (p *capturingProvider) firstCall() core.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		return core.ChatRequest{}
	}
	return p.reqs[0]
}

func (p *capturingProvider) ChatStream(_ context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	if ch != nil {
		defer close(ch)
	}
	p.record(req)
	if p.extractionResp != nil {
		p.mu.Lock()
		n := len(p.reqs)
		p.mu.Unlock()
		if n > 1 {
			return *p.extractionResp, nil
		}
	}
	if ch != nil {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: p.resp.Content}
	}
	return p.resp, nil
}

// --- Semantic similarity tests (lift-and-shift from old file) ---

func TestCosineSimilarityIdentical(t *testing.T) {
	a := []float32{1, 2, 3, 4}
	sim := core.CosineSimilarity(a, a)
	if sim < 0.999 {
		t.Errorf("identical vectors should have similarity ~1.0, got %f", sim)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1, 0, 0, 0}
	b := []float32{0, 1, 0, 0}
	sim := core.CosineSimilarity(a, b)
	if sim > 0.001 {
		t.Errorf("orthogonal vectors should have similarity ~0.0, got %f", sim)
	}
}

func TestCosineSimilarityOpposite(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	sim := core.CosineSimilarity(a, b)
	if sim > -0.999 {
		t.Errorf("opposite vectors should have similarity ~-1.0, got %f", sim)
	}
}

func TestCosineSimilarityDifferentLength(t *testing.T) {
	a := []float32{1, 2}
	b := []float32{1, 2, 3}
	sim := core.CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("different length vectors should return 0, got %f", sim)
	}
}

func TestCosineSimilarityEmpty(t *testing.T) {
	sim := core.CosineSimilarity([]float32{}, []float32{})
	if sim != 0 {
		t.Errorf("empty vectors should return 0, got %f", sim)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	sim := core.CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("zero vector should return 0, got %f", sim)
	}
}

// vectorEmbedding is a test embedding provider that returns pre-configured vectors.
type vectorEmbedding struct {
	vectors map[string][]float32 // text -> embedding
	dims    int
}

func (v *vectorEmbedding) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if vec, ok := v.vectors[text]; ok {
			out[i] = vec
		} else {
			// Return zero vector for unknown texts.
			out[i] = make([]float32, v.dims)
		}
	}
	return out, nil
}
func (v *vectorEmbedding) Dimensions() int { return v.dims }
func (v *vectorEmbedding) Name() string    { return "vector-test" }

// Confirm vectorEmbedding satisfies core.EmbeddingProvider.
var _ core.EmbeddingProvider = (*vectorEmbedding)(nil)

// waitForStored polls the recording store until at least n messages are
// persisted or the deadline passes. PersistTurn runs on a background
// goroutine, so assertions must not race it.
func waitForStored(t *testing.T, store *recordingStore, n int) []core.Message {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		stored := store.storedMessages()
		if len(stored) >= n || time.Now().After(deadline) {
			return stored
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Regression: a hook-forced Stop (OnIterationComplete → Stop) must persist
// the turn to conversation memory exactly like a natural stop. Previously
// finalizeIterationStop skipped Mem.PersistTurn, so any turn ended by a stop
// hook — e.g. a terminal interaction tool that hands control back to the
// user — vanished from the thread store, and the next Execute on the same
// ThreadID replayed history with the whole exchange missing.
func TestConversationMemory_PersistsOnHookStop_ToolCallPath(t *testing.T) {
	store := &recordingStore{}
	provider := &twoIterProvider{}

	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		// Mirror a terminal-interaction guard: stop as soon as the tool ran.
		if len(snap.ToolCalls) > 0 {
			return Stop(core.AgentResult{Output: "asked the user"}), nil
		}
		return Continue(), nil
	}

	agent := New("test", "test", provider,
		WithMemory(memory.WithStore(store)),
		WithTools(mockTool{}),
		WithHooks(Hooks{OnIterationComplete: hook}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{
		Input:    "make me a deck",
		ThreadID: "thread-hook-stop-tools",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "asked the user" {
		t.Errorf("Output = %q, want %q", result.Output, "asked the user")
	}

	stored := waitForStored(t, store, 2)
	if len(stored) < 2 {
		t.Fatalf("expected at least 2 stored messages after hook stop, got %d", len(stored))
	}
	if stored[0].Role != "user" || stored[0].Content != "make me a deck" {
		t.Errorf("first stored = %q/%q, want user/make me a deck", stored[0].Role, stored[0].Content)
	}
	foundAsst := false
	for _, m := range stored {
		if m.Role == "assistant" && m.Content == "asked the user" {
			foundAsst = true
		}
	}
	if !foundAsst {
		t.Errorf("assistant message not persisted on hook stop: %+v", stored)
	}
	for _, m := range stored {
		if m.ThreadID != "thread-hook-stop-tools" {
			t.Errorf("stored message thread = %q, want thread-hook-stop-tools", m.ThreadID)
		}
	}
}

// Same regression on the no-tool-call (final response) path: the hook stops
// after a plain text iteration.
func TestConversationMemory_PersistsOnHookStop_FinalResponsePath(t *testing.T) {
	store := &recordingStore{}
	provider := &mockProvider{
		name:      "test",
		responses: []core.ChatResponse{{Content: "draft answer"}},
	}

	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		return Stop(core.AgentResult{Output: "final answer"}), nil
	}

	agent := New("test", "test", provider,
		WithMemory(memory.WithStore(store)),
		WithHooks(Hooks{OnIterationComplete: hook}),
	)

	result, err := agent.Execute(context.Background(), AgentTask{
		Input:    "quick question",
		ThreadID: "thread-hook-stop-final",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "final answer" {
		t.Errorf("Output = %q, want %q", result.Output, "final answer")
	}

	stored := waitForStored(t, store, 2)
	if len(stored) < 2 {
		t.Fatalf("expected at least 2 stored messages after hook stop, got %d", len(stored))
	}
	if stored[0].Role != "user" || stored[0].Content != "quick question" {
		t.Errorf("first stored = %q/%q, want user/quick question", stored[0].Role, stored[0].Content)
	}
	foundAsst := false
	for _, m := range stored {
		if m.Role == "assistant" && m.Content == "final answer" {
			foundAsst = true
		}
	}
	if !foundAsst {
		t.Errorf("assistant message not persisted on hook stop: %+v", stored)
	}
}

// End-to-end tool-exchange replay (opencode-style full-fidelity history):
// turn 1 calls a tool; with HistoryConfig.ReplayToolCalls the SECOND turn's
// provider payload must contain turn 1's tool_call/tool_result pair — not
// just the final answer text — so the model keeps cross-turn awareness of
// what ran (e.g. an activated skill's instructions).
func TestConversationMemory_ReplaysToolExchangeNextTurn(t *testing.T) {
	store := &recordingStore{}
	provider := &twoIterProvider{}

	agentOne := New("test", "test", provider,
		WithMemory(memory.WithStore(store), memory.WithHistory(memory.HistoryConfig{
			ReplayToolCalls: true,
			ProtectedTools:  []string{"greet"},
		})),
		WithTools(mockTool{}),
	)
	if _, err := agentOne.Execute(context.Background(), AgentTask{
		Input: "say hello", ThreadID: "thread-replay",
	}); err != nil {
		t.Fatal(err)
	}
	stored := waitForStored(t, store, 2)
	if len(stored) < 2 {
		t.Fatalf("turn 1 not persisted, got %d messages", len(stored))
	}
	// Feed what turn 1 persisted back as turn 2's history.
	store.mu.Lock()
	store.history = append([]core.Message(nil), store.stored...)
	store.mu.Unlock()

	provider2 := &twoIterProvider{}
	agentTwo := New("test", "test", provider2,
		WithMemory(memory.WithStore(store), memory.WithHistory(memory.HistoryConfig{
			ReplayToolCalls: true,
			ProtectedTools:  []string{"greet"},
		})),
		WithTools(mockTool{}),
	)
	if _, err := agentTwo.Execute(context.Background(), AgentTask{
		Input: "and again", ThreadID: "thread-replay",
	}); err != nil {
		t.Fatal(err)
	}

	// Inspect the FIRST provider request of turn 2: it must replay turn 1's
	// tool exchange as a paired call + result.
	store.mu.Lock()
	defer store.mu.Unlock()
	reqs := provider2.reqs
	if len(reqs) == 0 {
		t.Fatal("no provider requests captured on turn 2")
	}
	first := reqs[0]
	var callID string
	for _, m := range first.Messages {
		for _, tc := range m.ToolCalls {
			if tc.Name == "greet" {
				callID = tc.ID
			}
		}
	}
	if callID == "" {
		t.Fatalf("turn 2 payload has no replayed greet tool call; messages: %+v", first.Messages)
	}
	paired := false
	for _, m := range first.Messages {
		if m.ToolCallID == callID && m.Content == "hello from greet" {
			paired = true
		}
	}
	if !paired {
		t.Fatalf("replayed tool call %q has no paired full result; messages: %+v", callID, first.Messages)
	}
}
