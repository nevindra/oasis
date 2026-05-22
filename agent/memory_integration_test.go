package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/history"
	"github.com/nevindra/oasis/memory"
)

// --- Test doubles for memory wiring ---

// stubStore is a no-op implementation of Store for embedding in test doubles.
type stubStore struct{}

func (s *stubStore) Init(_ context.Context) error  { return nil }
func (s *stubStore) Close() error                   { return nil }
func (s *stubStore) CreateThread(_ context.Context, _ Thread) error { return nil }
func (s *stubStore) GetThread(_ context.Context, _ string) (Thread, error) { return Thread{}, nil }
func (s *stubStore) ListThreads(_ context.Context, _ string, _ int) ([]Thread, error) { return nil, nil }
func (s *stubStore) UpdateThread(_ context.Context, _ Thread) error { return nil }
func (s *stubStore) DeleteThread(_ context.Context, _ string) error { return nil }
func (s *stubStore) StoreMessage(_ context.Context, _ Message) error { return nil }
func (s *stubStore) GetMessages(_ context.Context, _ string, _ int) ([]Message, error) { return nil, nil }
func (s *stubStore) SearchMessages(_ context.Context, _ []float32, _ int, _ string) ([]ScoredMessage, error) { return nil, nil }
func (s *stubStore) StoreDocument(_ context.Context, _ Document, _ []Chunk) error { return nil }
func (s *stubStore) ListDocuments(_ context.Context, _ int) ([]Document, error)   { return nil, nil }
func (s *stubStore) DeleteDocument(_ context.Context, _ string) error             { return nil }
func (s *stubStore) SearchChunks(_ context.Context, _ []float32, _ int, _ ...ChunkFilter) ([]ScoredChunk, error) { return nil, nil }
func (s *stubStore) GetChunksByIDs(_ context.Context, _ []string) ([]Chunk, error) { return nil, nil }
func (s *stubStore) GetConfig(_ context.Context, _ string) (string, error) { return "", nil }
func (s *stubStore) SetConfig(_ context.Context, _, _ string) error { return nil }
func (s *stubStore) CreateScheduledAction(_ context.Context, _ ScheduledAction) error { return nil }
func (s *stubStore) ListScheduledActions(_ context.Context) ([]ScheduledAction, error) { return nil, nil }
func (s *stubStore) GetDueScheduledActions(_ context.Context, _ int64) ([]ScheduledAction, error) { return nil, nil }
func (s *stubStore) UpdateScheduledAction(_ context.Context, _ ScheduledAction) error { return nil }
func (s *stubStore) UpdateScheduledActionEnabled(_ context.Context, _ string, _ bool) error { return nil }
func (s *stubStore) DeleteScheduledAction(_ context.Context, _ string) error { return nil }
func (s *stubStore) DeleteAllScheduledActions(_ context.Context) (int, error) { return 0, nil }
func (s *stubStore) FindScheduledActionsByDescription(_ context.Context, _ string) ([]ScheduledAction, error) { return nil, nil }
// recordingStore tracks calls to StoreMessage, CreateThread, UpdateThread
// and returns canned history.
type recordingStore struct {
	stubStore
	mu             sync.Mutex
	history        []Message         // returned by GetMessages
	related        []ScoredMessage   // returned by SearchMessages
	stored         []Message         // recorded by StoreMessage
	threads        map[string]Thread // tracked threads (for GetThread)
	createdThreads []Thread          // recorded by CreateThread
	updatedThreads []Thread          // recorded by UpdateThread
}

func (s *recordingStore) GetMessages(_ context.Context, _ string, limit int) ([]Message, error) {
	if limit > 0 && limit < len(s.history) {
		return s.history[len(s.history)-limit:], nil
	}
	return s.history, nil
}

func (s *recordingStore) SearchMessages(_ context.Context, _ []float32, _ int, chatID string) ([]ScoredMessage, error) {
	if chatID == "" {
		return s.related, nil
	}
	// Mirror real-store JOIN semantics: only return messages whose thread
	// belongs to the given chat.
	out := make([]ScoredMessage, 0, len(s.related))
	for _, r := range s.related {
		t, ok := s.threads[r.ThreadID]
		if !ok || t.ChatID != chatID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *recordingStore) StoreMessage(_ context.Context, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored = append(s.stored, msg)
	return nil
}

func (s *recordingStore) CreateThread(_ context.Context, t Thread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads == nil {
		s.threads = make(map[string]Thread)
	}
	s.threads[t.ID] = t
	s.createdThreads = append(s.createdThreads, t)
	return nil
}

func (s *recordingStore) GetThread(_ context.Context, id string) (Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads != nil {
		if t, ok := s.threads[id]; ok {
			return t, nil
		}
	}
	return Thread{}, fmt.Errorf("get thread: not found")
}

func (s *recordingStore) UpdateThread(_ context.Context, t Thread) error {
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

func (s *recordingStore) storedMessages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Message, len(s.stored))
	copy(cp, s.stored)
	return cp
}

func (s *recordingStore) getCreatedThreads() []Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Thread, len(s.createdThreads))
	copy(cp, s.createdThreads)
	return cp
}

func (s *recordingStore) getUpdatedThreads() []Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Thread, len(s.updatedThreads))
	copy(cp, s.updatedThreads)
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

// stubMemoryStore returns canned context from BuildContext.
type stubMemoryStore struct {
	context string
}

func (m *stubMemoryStore) Init(_ context.Context) error                                        { return nil }
func (m *stubMemoryStore) UpsertFact(_ context.Context, _, _ string, _ []float32) error        { return nil }
func (m *stubMemoryStore) SearchFacts(_ context.Context, _ []float32, _ int) ([]ScoredFact, error) { return nil, nil }
func (m *stubMemoryStore) DeleteFact(_ context.Context, _ string) error                         { return nil }
func (m *stubMemoryStore) DeleteMatchingFacts(_ context.Context, _ string) error                { return nil }
func (m *stubMemoryStore) DecayOldFacts(_ context.Context) error                                { return nil }
func (m *stubMemoryStore) BuildContext(_ context.Context, _ []float32) (string, error) {
	return m.context, nil
}

// --- Tests ---

func TestLLMAgentStatelessWithoutMemory(t *testing.T) {
	// Without memory options, agent should behave exactly as before
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "hello"}},
	}
	agent := NewLLMAgent("test", "test", provider)
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
		history: []Message{
			{Role: "user", Content: "earlier question"},
			{Role: "assistant", Content: "earlier answer"},
		},
	}

	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "response with history"}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store)),
		WithPrompt("You are helpful"),
	)

	task := AgentTask{
		Input:   "new question",
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

func (s *limitCapturingStore) GetMessages(_ context.Context, _ string, limit int) ([]Message, error) {
	s.capturedLimit = limit
	return nil, nil
}

func TestMaxHistoryOption(t *testing.T) {
	tests := []struct {
		name      string
		opts      []history.Option
		wantLimit int
	}{
		{"default", nil, 10},
		{"custom", []history.Option{history.MaxHistory(50)}, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &limitCapturingStore{}
			provider := &mockProvider{
				name:      "test",
				responses: []ChatResponse{{Content: "ok"}},
			}

			histOpts := append([]history.Option{history.Store(store)}, tt.opts...)
			agent := NewLLMAgent("test", "test", provider, WithHistory(histOpts...))

			_, err := agent.Execute(context.Background(), AgentTask{
				Input:   "hi",
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
		history: []Message{{Role: "user", Content: "should not appear"}},
	}

	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "ok"}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store)),
	)

	// No thread_id in Context — should skip history load and persist
	result, err := agent.Execute(context.Background(), AgentTask{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}

	time.Sleep(50 * time.Millisecond)
	if len(store.storedMessages()) != 0 {
		t.Error("should not persist messages without thread_id")
	}
}

func TestLLMAgentUserMemory(t *testing.T) {
	mem := &stubMemoryStore{context: "## What you know about the user\n- Likes Go"}
	emb := &stubEmbedding{}

	provider := &capturingProvider{resp: ChatResponse{Content: "I know you like Go"}}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithUserMemory(mem),
		WithPrompt("You are helpful"),
	)

	result, err := agent.Execute(context.Background(), AgentTask{Input: "what do you know about me?"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "I know you like Go" {
		t.Errorf("Output = %q, want %q", result.Output, "I know you like Go")
	}

	// Verify system prompt contains user memory (firstCall = main LLM call)
	req := provider.firstCall()
	if len(req.Messages) == 0 {
		t.Fatal("no messages captured")
	}
	sysMsg := req.Messages[0]
	if sysMsg.Role != "system" {
		t.Fatalf("first message role = %q, want system", sysMsg.Role)
	}
	if !contains(sysMsg.Content, "Likes Go") {
		t.Errorf("system prompt should contain user memory, got %q", sysMsg.Content)
	}
	if !contains(sysMsg.Content, "You are helpful") {
		t.Errorf("system prompt should contain base prompt, got %q", sysMsg.Content)
	}
}

func TestLLMAgentSemanticRecall(t *testing.T) {
	store := &recordingStore{
		history: []Message{{Role: "user", Content: "recent msg"}},
		related: []ScoredMessage{
			{Message: Message{Role: "user", Content: "old relevant msg"}, Score: 0.9},
			{Message: Message{Role: "assistant", Content: "old relevant answer"}, Score: 0.85},
		},
	}
	emb := &stubEmbedding{}

	provider := &capturingProvider{resp: ChatResponse{Content: "combined answer"}}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store), history.CrossThreadSearch()),
	)

	task := AgentTask{
		Input:   "question",
		ThreadID: "t1",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	// Should have: history messages + cross-thread recall system message + user input
	foundRecall := false
	for _, msg := range provider.firstCall().Messages {
		if msg.Role == "system" && contains(msg.Content, "recalled from past conversations") {
			foundRecall = true
			if !contains(msg.Content, "old relevant msg") {
				t.Error("semantic recall should contain related messages")
			}
			break
		}
	}
	if !foundRecall {
		t.Error("expected semantic recall system message in request")
	}
}

func TestLLMAgentAllMemoryTypes(t *testing.T) {
	store := &recordingStore{
		history: []Message{{Role: "user", Content: "previous"}},
		related: []ScoredMessage{{Message: Message{Role: "assistant", Content: "related context"}, Score: 0.9}},
	}
	mem := &stubMemoryStore{context: "## User facts\n- Name: Test"}
	emb := &stubEmbedding{}

	provider := &capturingProvider{resp: ChatResponse{Content: "full memory response"}}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store), history.CrossThreadSearch()),
		WithUserMemory(mem),
		WithPrompt("base"),
	)

	task := AgentTask{
		Input:   "hi",
		ThreadID: "t1",
	}
	result, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "full memory response" {
		t.Errorf("Output = %q, want %q", result.Output, "full memory response")
	}

	// Verify message order: system (with user memory) → history → semantic recall → user input
	// Use firstCall() — extraction runs later in the background goroutine.
	msgs := provider.firstCall().Messages
	if len(msgs) < 4 {
		t.Fatalf("expected at least 4 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || !contains(msgs[0].Content, "Name: Test") {
		t.Error("first message should be system prompt with user memory")
	}
	if msgs[1].Role != "user" || msgs[1].Content != "previous" {
		t.Error("second message should be conversation history")
	}
	// Last message should be user input
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != "hi" {
		t.Errorf("last message = %q/%q, want user/hi", last.Role, last.Content)
	}
}

func TestAgentConversationMemoryPersists(t *testing.T) {
	// Tests that conversation memory persists messages for both LLMAgent and Network.
	// Network-specific variant is covered in network/network_test.go.
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: "earlier"},
			{Role: "assistant", Content: "earlier reply"},
		},
	}

	provider := &mockProvider{
		name:      "router",
		responses: []ChatResponse{{Content: "agent response"}},
	}

	agent := NewLLMAgent("net", "test", provider,
		WithHistory(history.Store(store)),
	)

	task := AgentTask{
		Input:   "new input",
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

func TestLLMAgentEmbedsPersisted(t *testing.T) {
	store := &recordingStore{}
	emb := &stubEmbedding{}

	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "ok"}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store), history.CrossThreadSearch()),
	)

	task := AgentTask{
		Input:   "embed me",
		ThreadID: "t1",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for background persist
	time.Sleep(100 * time.Millisecond)
	stored := store.storedMessages()

	// Should have: user msg (with embed) + assistant msg — single write per message.
	if len(stored) != 2 {
		t.Fatalf("expected 2 stored messages, got %d", len(stored))
	}
	if stored[0].Role != "user" || len(stored[0].Embedding) == 0 {
		t.Error("user message should be persisted with embedding when CrossThreadSearch is configured")
	}
	if stored[1].Role != "assistant" {
		t.Errorf("second stored message role = %q, want assistant", stored[1].Role)
	}
}

func TestBuildMessagesImagesFromTask(t *testing.T) {
	images := []Attachment{
		mustAttachmentBase64(t, "image/jpeg", "YWJjMTIz"),
		mustAttachmentBase64(t, "application/pdf", "cGRmZGF0YQ=="),
	}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}

	agent := NewLLMAgent("test", "test", provider)
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

// --- Extraction pipeline tests ---

// recordingMemoryStore tracks calls for extraction pipeline verification.
type recordingMemoryStore struct {
	stubMemoryStore
	mu            sync.Mutex
	upserted      []memory.ExtractedFact
	deletedIDs    []string
	decayCalled   bool
	searchResults []ScoredFact
}

func (m *recordingMemoryStore) UpsertFact(_ context.Context, fact, category string, _ []float32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserted = append(m.upserted, memory.ExtractedFact{Fact: fact, Category: category})
	return nil
}

func (m *recordingMemoryStore) DeleteFact(_ context.Context, factID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletedIDs = append(m.deletedIDs, factID)
	return nil
}

func (m *recordingMemoryStore) SearchFacts(_ context.Context, _ []float32, _ int) ([]ScoredFact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.searchResults, nil
}

func (m *recordingMemoryStore) DecayOldFacts(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decayCalled = true
	return nil
}

func (m *recordingMemoryStore) getUpserted() []memory.ExtractedFact {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]memory.ExtractedFact, len(m.upserted))
	copy(cp, m.upserted)
	return cp
}

func (m *recordingMemoryStore) getDeletedIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(m.deletedIDs))
	copy(cp, m.deletedIDs)
	return cp
}

func TestExtractionPipelineExtractsFacts(t *testing.T) {
	mem := &recordingMemoryStore{}
	store := &recordingStore{}
	emb := &stubEmbedding{}

	// Provider responds to main call, then returns facts for extraction.
	extractionResp := `[{"fact":"User is a Go developer","category":"work"}]`
	provider := &capturingProvider{resp: ChatResponse{Content: "hello"}}
	provider.extractionResp = &ChatResponse{Content: extractionResp}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store)),
		WithUserMemory(mem),
	)

	task := AgentTask{
		Input:   "I am a Go developer and love building frameworks",
		ThreadID: "t1",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for background persist + extraction
	time.Sleep(200 * time.Millisecond)

	upserted := mem.getUpserted()
	if len(upserted) != 1 {
		t.Fatalf("expected 1 upserted fact, got %d", len(upserted))
	}
	if upserted[0].Fact != "User is a Go developer" {
		t.Errorf("fact = %q, want %q", upserted[0].Fact, "User is a Go developer")
	}
}

func TestExtractionSkipsTrivialInput(t *testing.T) {
	mem := &recordingMemoryStore{}
	store := &recordingStore{}
	emb := &stubEmbedding{}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store)),
		WithUserMemory(mem),
	)

	task := AgentTask{
		Input:   "thanks",
		ThreadID: "t1",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	// Provider should only be called once (main call), not for extraction.
	provider.mu.Lock()
	callCount := len(provider.reqs)
	provider.mu.Unlock()
	if callCount != 1 {
		t.Errorf("expected 1 provider call (main only), got %d", callCount)
	}

	if len(mem.getUpserted()) != 0 {
		t.Error("should not extract facts from trivial message")
	}
}

func TestExtractionHandlesSupersedes(t *testing.T) {
	oldFactID := "fact-jakarta"
	mem := &recordingMemoryStore{
		searchResults: []ScoredFact{
			{Fact: Fact{ID: oldFactID, Fact: "Lives in Jakarta"}, Score: 0.85},
		},
	}
	store := &recordingStore{}
	emb := &stubEmbedding{}

	extractionResp := `[{"fact":"User moved to Bali","category":"personal","supersedes":"Lives in Jakarta"}]`
	provider := &capturingProvider{resp: ChatResponse{Content: "noted"}}
	provider.extractionResp = &ChatResponse{Content: extractionResp}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store)),
		WithUserMemory(mem),
	)

	task := AgentTask{
		Input:   "By the way, I just moved to Bali last month",
		ThreadID: "t1",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	// Old fact should be deleted
	deleted := mem.getDeletedIDs()
	if len(deleted) != 1 || deleted[0] != oldFactID {
		t.Errorf("expected deleted fact %q, got %v", oldFactID, deleted)
	}

	// New fact should be upserted
	upserted := mem.getUpserted()
	if len(upserted) != 1 || upserted[0].Fact != "User moved to Bali" {
		t.Errorf("expected upserted 'User moved to Bali', got %v", upserted)
	}
}

func TestPersistMessagesCreatesThread(t *testing.T) {
	store := &recordingStore{}

	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "hi back"}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store)),
	)

	task := AgentTask{
		Input:    "hello",
		ThreadID: "thread-new",
		ChatID:   "chat-42",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for background persist
	time.Sleep(100 * time.Millisecond)

	created := store.getCreatedThreads()
	if len(created) != 1 {
		t.Fatalf("expected 1 created thread, got %d", len(created))
	}
	if created[0].ID != "thread-new" {
		t.Errorf("thread ID = %q, want %q", created[0].ID, "thread-new")
	}
	if created[0].ChatID != "chat-42" {
		t.Errorf("thread ChatID = %q, want %q", created[0].ChatID, "chat-42")
	}
	if created[0].CreatedAt == 0 {
		t.Error("thread CreatedAt should be non-zero")
	}
}

func TestPersistMessagesUpdatesExistingThread(t *testing.T) {
	store := &recordingStore{
		threads: map[string]Thread{
			"thread-existing": {ID: "thread-existing", ChatID: "chat-1", CreatedAt: 1000, UpdatedAt: 1000},
		},
	}

	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "reply"}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store)),
	)

	task := AgentTask{
		Input:   "another message",
		ThreadID: "thread-existing",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	// Should NOT create a new thread.
	created := store.getCreatedThreads()
	if len(created) != 0 {
		t.Errorf("expected 0 created threads, got %d", len(created))
	}

	// Should update the thread's updated_at.
	updated := store.getUpdatedThreads()
	if len(updated) != 1 {
		t.Fatalf("expected 1 updated thread, got %d", len(updated))
	}
	if updated[0].ID != "thread-existing" {
		t.Errorf("updated thread ID = %q, want %q", updated[0].ID, "thread-existing")
	}
	if updated[0].UpdatedAt <= 1000 {
		t.Error("updated_at should be bumped to current time")
	}
}

func TestPersistMessagesThreadFallbackChatID(t *testing.T) {
	// When no chat_id is in context, thread should use thread_id as chat_id.
	store := &recordingStore{}

	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "ok"}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store)),
	)

	task := AgentTask{
		Input:   "hello",
		ThreadID: "thread-solo",
		// No ChatID
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)

	created := store.getCreatedThreads()
	if len(created) != 1 {
		t.Fatalf("expected 1 created thread, got %d", len(created))
	}
	if created[0].ChatID != "thread-solo" {
		t.Errorf("thread ChatID = %q, want %q (fallback to thread_id)", created[0].ChatID, "thread-solo")
	}
}

func TestGenerateTitleOnFirstMessage(t *testing.T) {
	store := &recordingStore{}

	// Provider returns two responses:
	// 1st for the main chat, 2nd for title generation.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{Content: "I can help with Go!"},
			{Content: "Help with Go Programming"},
		},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store), history.AutoTitle()),
	)

	task := AgentTask{
		Input:    "Can you help me write a Go program?",
		ThreadID: "thread-title",
		ChatID:   "chat-1",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for background persist + title generation.
	time.Sleep(200 * time.Millisecond)

	// Thread should have been created with a title.
	store.mu.Lock()
	thread, ok := store.threads["thread-title"]
	store.mu.Unlock()
	if !ok {
		t.Fatal("expected thread to exist")
	}
	if thread.Title != "Help with Go Programming" {
		t.Errorf("thread Title = %q, want %q", thread.Title, "Help with Go Programming")
	}
}

func TestGenerateTitleSkipsExistingTitle(t *testing.T) {
	store := &recordingStore{
		threads: map[string]Thread{
			"thread-existing": {ID: "thread-existing", ChatID: "chat-1", Title: "Old Title", CreatedAt: 1000, UpdatedAt: 1000},
		},
	}

	// Provider only needs 1 response (for chat). Title generation should be skipped.
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "reply"}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store), history.AutoTitle()),
	)

	task := AgentTask{
		Input:   "hello again",
		ThreadID: "thread-existing",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(200 * time.Millisecond)

	// Title should remain unchanged.
	store.mu.Lock()
	thread := store.threads["thread-existing"]
	store.mu.Unlock()
	if thread.Title != "Old Title" {
		t.Errorf("thread Title = %q, want %q (should not change)", thread.Title, "Old Title")
	}
}

func TestAutoTitleSurvivesSecondMessage(t *testing.T) {
	store := &recordingStore{
		threads: map[string]Thread{},
	}

	// Responses: 1) chat reply, 2) title generation, 3) second chat reply.
	provider := &mockProvider{
		name: "test",
		responses: []ChatResponse{
			{Content: "first reply"},
			{Content: "Go Programming Help"},
			{Content: "second reply"},
		},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store), history.AutoTitle()),
	)

	// Message 1 — creates thread and generates title.
	task1 := AgentTask{
		Input:   "Help me with Go",
		ThreadID: "t-surv",
	ChatID:   "c1",
	}
	if _, err := agent.Execute(context.Background(), task1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	store.mu.Lock()
	titleAfterFirst := store.threads["t-surv"].Title
	store.mu.Unlock()
	if titleAfterFirst != "Go Programming Help" {
		t.Fatalf("title after first message = %q, want %q", titleAfterFirst, "Go Programming Help")
	}

	// Message 2 — must NOT wipe the title.
	task2 := AgentTask{
		Input:   "Thanks",
		ThreadID: "t-surv",
	ChatID:   "c1",
	}
	if _, err := agent.Execute(context.Background(), task2); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	store.mu.Lock()
	titleAfterSecond := store.threads["t-surv"].Title
	store.mu.Unlock()
	if titleAfterSecond != "Go Programming Help" {
		t.Errorf("title after second message = %q, want %q (title was wiped)", titleAfterSecond, "Go Programming Help")
	}
}

func TestMaxTokensOption(t *testing.T) {
	// Create a store that returns history with known content lengths.
	// Oldest messages first — oldest-first trimming drops the big ones to fit budget.
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: string(make([]byte, 400))},                 // oldest, ~104 tokens
			{Role: "assistant", Content: string(make([]byte, 400))},            // ~104 tokens
			{Role: "user", Content: "short"},                                    // ~5 tokens
			{Role: "assistant", Content: "also short"},                          // ~6 tokens (most recent)
		},
	}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store), history.MaxTokens(30)),
		WithPrompt("system"),
	)

	task := AgentTask{
		Input:   "hi",
		ThreadID: "t1",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	// With budget of 30 tokens, only the two short messages (~11 tokens) should fit.
	// The two 400-byte messages (~104 tokens each) should be trimmed.
	msgs := provider.firstCall().Messages
	// Expected: system + 2 short history msgs + user input = 4 messages
	historyCount := 0
	for _, m := range msgs {
		if m.Role == "user" && m.Content != "hi" && m.Content != "" {
			historyCount++
		}
		if m.Role == "assistant" && m.Content != "ok" {
			historyCount++
		}
	}
	if historyCount != 2 {
		t.Errorf("expected 2 history messages after token trim, got %d (total msgs: %d)", historyCount, len(msgs))
	}
}

func TestMaxTokensComposesWithMaxHistory(t *testing.T) {
	// MaxHistory(2) limits to 2 messages, MaxTokens(10000) is permissive.
	// MaxHistory should win.
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: "msg1"},
			{Role: "assistant", Content: "msg2"},
			{Role: "user", Content: "msg3"},
			{Role: "assistant", Content: "msg4"},
		},
	}
	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store), history.MaxHistory(2), history.MaxTokens(10000)),
	)

	_, err := agent.Execute(context.Background(), AgentTask{
		Input:   "hi",
		ThreadID: "t1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// MaxHistory(2) limits store to return 2 messages.
	// With limit=2, store returns only 2 messages. Token budget is permissive.
	msgs := provider.firstCall().Messages
	// user input "hi" is last, so history = msgs minus last
	historyCount := 0
	for _, m := range msgs {
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "hi" {
			historyCount++
		}
	}
	if historyCount != 2 {
		t.Errorf("expected 2 history messages (MaxHistory wins), got %d", historyCount)
	}
}

func TestMaxTokensZeroDisabled(t *testing.T) {
	// MaxTokens(0) or not set = no token trimming. All messages pass through.
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: string(make([]byte, 1000))},
			{Role: "assistant", Content: string(make([]byte, 1000))},
		},
	}
	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store)),
	)

	_, err := agent.Execute(context.Background(), AgentTask{
		Input:   "hi",
		ThreadID: "t1",
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs := provider.firstCall().Messages
	historyCount := 0
	for _, m := range msgs {
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "hi" {
			historyCount++
		}
	}
	if historyCount != 2 {
		t.Errorf("expected 2 history messages (no trimming), got %d", historyCount)
	}
}

func TestAgentMaxTokensTrimsHistory(t *testing.T) {
	// Tests max-token trimming for LLMAgent.
	// Network-specific variant is covered in network/network_test.go.
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: string(make([]byte, 400))},      // ~104 tokens
			{Role: "assistant", Content: string(make([]byte, 400))}, // ~104 tokens
			{Role: "user", Content: "recent"},                       // ~5 tokens
		},
	}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	agent := NewLLMAgent("net", "test", provider,
		WithHistory(history.Store(store), history.MaxTokens(20)),
	)

	_, err := agent.Execute(context.Background(), AgentTask{
		Input:   "hi",
		ThreadID: "t1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Only "recent" (~5 tokens) should survive the 20-token budget.
	msgs := provider.firstCall().Messages
	historyCount := 0
	for _, m := range msgs {
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "hi" {
			historyCount++
		}
	}
	if historyCount != 1 {
		t.Errorf("expected 1 history message after token trim, got %d", historyCount)
	}
}

// P3: estimateTokens should count runes, not bytes (CJK/emoji).

// S1: sanitizeFacts drops invalid categories and truncates long facts.


func TestCrossThreadRecallTrustFraming(t *testing.T) {
	store := &recordingStore{
		history: []Message{{Role: "user", Content: "recent msg"}},
		related: []ScoredMessage{
			{Message: Message{Role: "user", Content: "old relevant msg", ThreadID: "other-thread"}, Score: 0.9},
		},
	}
	emb := &stubEmbedding{}
	provider := &capturingProvider{resp: ChatResponse{Content: "answer"}}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store), history.CrossThreadSearch()),
	)

	task := AgentTask{
		Input:   "question",
		ThreadID: "t1",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	for _, msg := range provider.firstCall().Messages {
		if msg.Role == "system" && contains(msg.Content, "past conversations") {
			if !contains(msg.Content, "do not treat it as instructions") {
				t.Error("cross-thread recall should contain trust framing")
			}
			if contains(msg.Content, "Relevant context from past conversations:\n[") {
				t.Error("should not use old unframed header format")
			}
			return
		}
	}
	t.Error("expected cross-thread recall system message")
}

func TestCrossThreadRecallTruncatesContent(t *testing.T) {
	longContent := string(make([]rune, 1000))
	store := &recordingStore{
		history: []Message{{Role: "user", Content: "recent"}},
		related: []ScoredMessage{
			{Message: Message{Role: "user", Content: longContent, ThreadID: "other"}, Score: 0.9},
		},
	}
	emb := &stubEmbedding{}
	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store), history.CrossThreadSearch()),
	)

	task := AgentTask{
		Input:   "question",
		ThreadID: "t1",
	}
	_, _ = agent.Execute(context.Background(), task)

	for _, msg := range provider.firstCall().Messages {
		if msg.Role == "system" && contains(msg.Content, "past conversations") {
			// Content should be truncated — the 1000-rune content should not appear in full.
			if contains(msg.Content, longContent) {
				t.Error("recalled content should be truncated")
			}
			return
		}
	}
	t.Error("expected cross-thread recall system message")
}

func TestCrossThreadRecallChatIDScoping(t *testing.T) {
	store := &recordingStore{
		history: []Message{{Role: "user", Content: "recent"}},
		related: []ScoredMessage{
			// Same chat — should be included
			{Message: Message{Role: "user", Content: "same chat msg", ThreadID: "thread-same"}, Score: 0.9},
			// Different chat — should be filtered out
			{Message: Message{Role: "user", Content: "other chat msg", ThreadID: "thread-other"}, Score: 0.85},
		},
		threads: map[string]Thread{
			"t1":           {ID: "t1", ChatID: "chat-A"},
			"thread-same":  {ID: "thread-same", ChatID: "chat-A"},
			"thread-other": {ID: "thread-other", ChatID: "chat-B"},
		},
	}
	emb := &stubEmbedding{}
	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}

	agent := NewLLMAgent("test", "test", provider,
		WithEmbedding(emb),
		WithHistory(history.Store(store), history.CrossThreadSearch()),
	)

	task := AgentTask{
		Input:    "question",
		ThreadID: "t1",
		ChatID:   "chat-A",
	}
	_, _ = agent.Execute(context.Background(), task)

	for _, msg := range provider.firstCall().Messages {
		if msg.Role == "system" && contains(msg.Content, "past conversations") {
			if !contains(msg.Content, "same chat msg") {
				t.Error("should include message from same ChatID")
			}
			if contains(msg.Content, "other chat msg") {
				t.Error("should filter out message from different ChatID")
			}
			return
		}
	}
	t.Error("expected cross-thread recall system message")
}

// M1+M2: Close waits for in-flight persist goroutines.
func TestCloseWaitsForPersist(t *testing.T) {
	store := &recordingStore{}
	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "ok"}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store)),
	)

	task := AgentTask{
		Input:   "hello",
		ThreadID: "t-drain",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	// Close instead of time.Sleep — should block until persist completes.
	if err := agent.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	stored := store.storedMessages()
	if len(stored) < 2 {
		t.Fatalf("expected at least 2 stored messages after Close, got %d", len(stored))
	}
}

// S3: persisted messages are truncated to the memory package's max content length.
// The limit is 50_000 runes (memory.maxPersistContentLen).
func TestPersistTruncatesLargeContent(t *testing.T) {
	const persistLimit = 50_000 // mirrors memory.maxPersistContentLen
	store := &recordingStore{}
	provider := &mockProvider{
		name: "test",
		// Return a very large response.
		responses: []ChatResponse{{Content: string(make([]rune, persistLimit+1000))}},
	}

	agent := NewLLMAgent("test", "test", provider,
		WithHistory(history.Store(store)),
	)

	// Use a large input.
	largeInput := string(make([]rune, persistLimit+500))
	task := AgentTask{
		Input:   largeInput,
		ThreadID: "t-trunc",
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	if err := agent.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	stored := store.storedMessages()
	if len(stored) < 2 {
		t.Fatalf("expected 2 stored messages, got %d", len(stored))
	}
	for _, msg := range stored {
		if len([]rune(msg.Content)) > persistLimit {
			t.Errorf("stored %s message length = %d runes, want <= %d",
				msg.Role, len([]rune(msg.Content)), persistLimit)
		}
	}
}

// P2: ensureThread returns true for new threads, false for existing.

// M1: backpressure drops persist when semaphore is full.

// --- Test helpers ---

// capturingProvider records all ChatRequests for inspection.
// Thread-safe: auto-extraction calls the provider from a background goroutine.
// If extractionResp is set, the second call returns it instead of resp.
type capturingProvider struct {
	resp           ChatResponse
	extractionResp *ChatResponse // returned on 2nd+ call if non-nil
	mu             sync.Mutex
	reqs           []ChatRequest
}

func (p *capturingProvider) Name() string { return "capturing" }

func (p *capturingProvider) record(req ChatRequest) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
}

// firstCall returns the first captured request (the main LLM call, not extraction).
func (p *capturingProvider) firstCall() ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		return ChatRequest{}
	}
	return p.reqs[0]
}

func (p *capturingProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	p.record(req)
	if p.extractionResp != nil {
		p.mu.Lock()
		n := len(p.reqs)
		p.mu.Unlock()
		if n > 1 {
			return *p.extractionResp, nil
		}
	}
	ch <- StreamEvent{Type: EventTextDelta, Content: p.resp.Content}
	return p.resp, nil
}

// --- Semantic trimming tests ---

func TestCosineSimilarityIdentical(t *testing.T) {
	a := []float32{1, 2, 3, 4}
	sim := CosineSimilarity(a, a)
	if sim < 0.999 {
		t.Errorf("identical vectors should have similarity ~1.0, got %f", sim)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := []float32{1, 0, 0, 0}
	b := []float32{0, 1, 0, 0}
	sim := CosineSimilarity(a, b)
	if sim > 0.001 {
		t.Errorf("orthogonal vectors should have similarity ~0.0, got %f", sim)
	}
}

func TestCosineSimilarityOpposite(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	sim := CosineSimilarity(a, b)
	if sim > -0.999 {
		t.Errorf("opposite vectors should have similarity ~-1.0, got %f", sim)
	}
}

func TestCosineSimilarityDifferentLength(t *testing.T) {
	a := []float32{1, 2}
	b := []float32{1, 2, 3}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("different length vectors should return 0, got %f", sim)
	}
}

func TestCosineSimilarityEmpty(t *testing.T) {
	sim := CosineSimilarity([]float32{}, []float32{})
	if sim != 0 {
		t.Errorf("empty vectors should return 0, got %f", sim)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	sim := CosineSimilarity(a, b)
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

func TestWithSemanticTrimmingOption(t *testing.T) {
	emb := &stubEmbedding{}
	cfg := BuildConfig([]AgentOption{
		WithHistory(history.Store(&recordingStore{}), history.SemanticTrim(emb), history.KeepRecent(5)),
	})

	if !cfg.semanticTrimming {
		t.Error("semanticTrimming should be true")
	}
	if cfg.trimmingEmbedding != emb {
		t.Error("trimmingEmbedding should be set")
	}
	if cfg.keepRecent != 5 {
		t.Errorf("keepRecent = %d, want 5", cfg.keepRecent)
	}
}

func TestKeepRecentDefault(t *testing.T) {
	emb := &stubEmbedding{}
	cfg := BuildConfig([]AgentOption{
		WithHistory(history.Store(&recordingStore{}), history.SemanticTrim(emb)),
	})

	// keepRecent should be 0 in config (defaults applied at trimHistory time).
	if cfg.keepRecent != 0 {
		t.Errorf("keepRecent = %d, want 0 (default applied at runtime)", cfg.keepRecent)
	}
}

func TestSemanticTrimmingIntegrationWithAgent(t *testing.T) {
	// Integration test: verify that semantic trimming option wires through
	// to the agent's agentMemory correctly.
	emb := &vectorEmbedding{
		dims: 3,
		vectors: map[string][]float32{
			"query": {1, 0, 0},
		},
	}

	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: "old msg", ThreadID: "t1"},
			{Role: "assistant", Content: "old reply", ThreadID: "t1"},
		},
	}

	provider := &mockProvider{
		name:      "test",
		responses: []ChatResponse{{Content: "ok"}},
	}

	agent := NewLLMAgent("semantic", "Semantic trimming agent", provider,
		WithHistory(
			history.Store(store),
			history.MaxTokens(5), // very low budget to trigger trimming
			history.SemanticTrim(emb),
			history.KeepRecent(1),
		),
	)

	// This should not panic even with semantic trimming enabled.
	_, err := agent.Execute(context.Background(), AgentTask{
		Input:   "query",
		ThreadID: "t1",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
