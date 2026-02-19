package oasis

import (
	"context"
	"sync"
	"testing"
	"time"
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
func (s *stubStore) SearchMessages(_ context.Context, _ []float32, _ int) ([]ScoredMessage, error) { return nil, nil }
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
func (s *stubStore) CreateSkill(_ context.Context, _ Skill) error { return nil }
func (s *stubStore) GetSkill(_ context.Context, _ string) (Skill, error) { return Skill{}, nil }
func (s *stubStore) ListSkills(_ context.Context) ([]Skill, error) { return nil, nil }
func (s *stubStore) UpdateSkill(_ context.Context, _ Skill) error { return nil }
func (s *stubStore) DeleteSkill(_ context.Context, _ string) error { return nil }
func (s *stubStore) SearchSkills(_ context.Context, _ []float32, _ int) ([]ScoredSkill, error) { return nil, nil }

// recordingStore tracks calls to StoreMessage and returns canned history.
type recordingStore struct {
	stubStore
	mu       sync.Mutex
	history  []Message         // returned by GetMessages
	related  []ScoredMessage   // returned by SearchMessages
	stored   []Message         // recorded by StoreMessage
}

func (s *recordingStore) GetMessages(_ context.Context, _ string, limit int) ([]Message, error) {
	if limit > 0 && limit < len(s.history) {
		return s.history[len(s.history)-limit:], nil
	}
	return s.history, nil
}

func (s *recordingStore) SearchMessages(_ context.Context, _ []float32, _ int) ([]ScoredMessage, error) {
	return s.related, nil
}

func (s *recordingStore) StoreMessage(_ context.Context, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored = append(s.stored, msg)
	return nil
}

func (s *recordingStore) storedMessages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]Message, len(s.stored))
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
		WithConversationMemory(store),
		WithPrompt("You are helpful"),
	)

	task := AgentTask{
		Input:   "new question",
		Context: map[string]any{ContextThreadID: "thread-1"},
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
		opts      []ConversationOption
		wantLimit int
	}{
		{"default", nil, defaultMaxHistory},
		{"custom", []ConversationOption{MaxHistory(50)}, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &limitCapturingStore{}
			provider := &mockProvider{
				name:      "test",
				responses: []ChatResponse{{Content: "ok"}},
			}

			opts := []AgentOption{WithConversationMemory(store, tt.opts...)}
			agent := NewLLMAgent("test", "test", provider, opts...)

			_, err := agent.Execute(context.Background(), AgentTask{
				Input:   "hi",
				Context: map[string]any{ContextThreadID: "t1"},
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
		WithConversationMemory(store),
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
		WithUserMemory(mem, emb),
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
			{Message: Message{Role: "user", Content: "old relevant msg"}},
			{Message: Message{Role: "assistant", Content: "old relevant answer"}},
		},
	}
	emb := &stubEmbedding{}

	provider := &capturingProvider{resp: ChatResponse{Content: "combined answer"}}

	agent := NewLLMAgent("test", "test", provider,
		WithConversationMemory(store, CrossThreadSearch(emb)),
	)

	task := AgentTask{
		Input:   "question",
		Context: map[string]any{ContextThreadID: "t1"},
	}
	_, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}

	// Should have: history messages + cross-thread recall system message + user input
	foundRecall := false
	for _, msg := range provider.firstCall().Messages {
		if msg.Role == "system" && contains(msg.Content, "Relevant context from past conversations") {
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
		related: []ScoredMessage{{Message: Message{Role: "assistant", Content: "related context"}}},
	}
	mem := &stubMemoryStore{context: "## User facts\n- Name: Test"}
	emb := &stubEmbedding{}

	provider := &capturingProvider{resp: ChatResponse{Content: "full memory response"}}

	agent := NewLLMAgent("test", "test", provider,
		WithConversationMemory(store, CrossThreadSearch(emb)),
		WithUserMemory(mem, emb),
		WithPrompt("base"),
	)

	task := AgentTask{
		Input:   "hi",
		Context: map[string]any{ContextThreadID: "t1"},
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

func TestNetworkConversationMemory(t *testing.T) {
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: "earlier"},
			{Role: "assistant", Content: "earlier reply"},
		},
	}

	router := &mockProvider{
		name:      "router",
		responses: []ChatResponse{{Content: "network response"}},
	}

	network := NewNetwork("net", "test", router,
		WithConversationMemory(store),
	)

	task := AgentTask{
		Input:   "new input",
		Context: map[string]any{ContextThreadID: "t1"},
	}
	result, err := network.Execute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "network response" {
		t.Errorf("Output = %q, want %q", result.Output, "network response")
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
		WithConversationMemory(store, CrossThreadSearch(emb)),
	)

	task := AgentTask{
		Input:   "embed me",
		Context: map[string]any{ContextThreadID: "t1"},
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
		{MimeType: "image/jpeg", Base64: "abc123"},
		{MimeType: "application/pdf", Base64: "pdfdata"},
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
	if last.Attachments[0].MimeType != "image/jpeg" || last.Attachments[0].Base64 != "abc123" {
		t.Errorf("first image = %+v, want {image/jpeg, abc123}", last.Attachments[0])
	}
	if last.Attachments[1].MimeType != "application/pdf" || last.Attachments[1].Base64 != "pdfdata" {
		t.Errorf("second image = %+v, want {application/pdf, pdfdata}", last.Attachments[1])
	}
}

// --- Extraction pipeline tests ---

// recordingMemoryStore tracks calls for extraction pipeline verification.
type recordingMemoryStore struct {
	stubMemoryStore
	mu            sync.Mutex
	upserted      []ExtractedFact
	deletedIDs    []string
	decayCalled   bool
	searchResults []ScoredFact
}

func (m *recordingMemoryStore) UpsertFact(_ context.Context, fact, category string, _ []float32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upserted = append(m.upserted, ExtractedFact{Fact: fact, Category: category})
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

func (m *recordingMemoryStore) getUpserted() []ExtractedFact {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]ExtractedFact, len(m.upserted))
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

func TestExtractionSkipsTrivialMessages(t *testing.T) {
	if shouldExtractFacts("ok") {
		t.Error("should skip 'ok'")
	}
	if shouldExtractFacts("thanks") {
		t.Error("should skip 'thanks'")
	}
	if shouldExtractFacts("wkwk") {
		t.Error("should skip 'wkwk'")
	}
	if shouldExtractFacts("short") {
		t.Error("should skip messages < 10 chars")
	}
	if !shouldExtractFacts("I work as a software engineer") {
		t.Error("should extract real content")
	}
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
		WithConversationMemory(store),
		WithUserMemory(mem, emb),
	)

	task := AgentTask{
		Input:   "I am a Go developer and love building frameworks",
		Context: map[string]any{ContextThreadID: "t1"},
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
		WithConversationMemory(store),
		WithUserMemory(mem, emb),
	)

	task := AgentTask{
		Input:   "thanks",
		Context: map[string]any{ContextThreadID: "t1"},
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
		WithConversationMemory(store),
		WithUserMemory(mem, emb),
	)

	task := AgentTask{
		Input:   "By the way, I just moved to Bali last month",
		Context: map[string]any{ContextThreadID: "t1"},
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

func TestParseExtractedFactsMarkdownFence(t *testing.T) {
	r := "```json\n[{\"fact\":\"User likes Go\",\"category\":\"preference\"}]\n```"
	facts := parseExtractedFacts(r)
	if len(facts) != 1 {
		t.Fatalf("expected 1, got %d", len(facts))
	}
	if facts[0].Fact != "User likes Go" {
		t.Errorf("fact = %q, want %q", facts[0].Fact, "User likes Go")
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
		WithConversationMemory(store, MaxTokens(30)),
		WithPrompt("system"),
	)

	task := AgentTask{
		Input:   "hi",
		Context: map[string]any{ContextThreadID: "t1"},
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
		WithConversationMemory(store, MaxHistory(2), MaxTokens(10000)),
	)

	_, err := agent.Execute(context.Background(), AgentTask{
		Input:   "hi",
		Context: map[string]any{ContextThreadID: "t1"},
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
		WithConversationMemory(store),
	)

	_, err := agent.Execute(context.Background(), AgentTask{
		Input:   "hi",
		Context: map[string]any{ContextThreadID: "t1"},
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

func TestNetworkMaxTokens(t *testing.T) {
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: string(make([]byte, 400))},     // ~104 tokens
			{Role: "assistant", Content: string(make([]byte, 400))}, // ~104 tokens
			{Role: "user", Content: "recent"},                      // ~5 tokens
		},
	}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	network := NewNetwork("net", "test", provider,
		WithConversationMemory(store, MaxTokens(20)),
	)

	_, err := network.Execute(context.Background(), AgentTask{
		Input:   "hi",
		Context: map[string]any{ContextThreadID: "t1"},
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

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		msg  ChatMessage
		want int
	}{
		{"empty", ChatMessage{}, 4},
		{"short", ChatMessage{Content: "hello"}, 5}, // 5/4=1 + 4 = 5
		{"medium", ChatMessage{Content: "hello world, this is a test message"}, 12}, // 35/4=8 + 4 = 12
		{"long", ChatMessage{Content: string(make([]byte, 400))}, 104}, // 400/4 + 4 = 104
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.msg)
			if got != tt.want {
				t.Errorf("estimateTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

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

func (p *capturingProvider) Chat(_ context.Context, req ChatRequest) (ChatResponse, error) {
	p.record(req)
	if p.extractionResp != nil {
		p.mu.Lock()
		n := len(p.reqs)
		p.mu.Unlock()
		if n > 1 {
			return *p.extractionResp, nil
		}
	}
	return p.resp, nil
}
func (p *capturingProvider) ChatWithTools(_ context.Context, req ChatRequest, _ []ToolDefinition) (ChatResponse, error) {
	p.record(req)
	return p.resp, nil
}
func (p *capturingProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	p.record(req)
	ch <- StreamEvent{Type: EventTextDelta, Content: p.resp.Content}
	return p.resp, nil
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
