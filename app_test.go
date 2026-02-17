package oasis

import (
	"context"
	"strings"
	"testing"
)

func TestNewApp(t *testing.T) {
	a := New(
		WithSystemPrompt("You are a test bot."),
		WithMaxToolIterations(5),
	)
	if a.systemPrompt != "You are a test bot." {
		t.Error("system prompt not set")
	}
	if a.maxIter != 5 {
		t.Error("max iterations not set")
	}
	if a.tools == nil {
		t.Error("tool registry should be initialized")
	}
}

func TestAppRunRequiresComponents(t *testing.T) {
	a := New()
	err := a.Run(t.Context())
	if err == nil {
		t.Error("expected error without required components")
	}
}

func TestAppRunRequiresAllThree(t *testing.T) {
	// Only frontend — still missing provider and store
	a := New(WithFrontend(&stubFrontend{}))
	err := a.Run(t.Context())
	if err == nil {
		t.Error("expected error without provider and store")
	}

	// Frontend + provider — still missing store
	a = New(WithFrontend(&stubFrontend{}), WithProvider(&mockProvider{name: "p"}))
	err = a.Run(t.Context())
	if err == nil {
		t.Error("expected error without store")
	}
}

func TestAppRunErrorMessage(t *testing.T) {
	a := New()
	err := a.Run(t.Context())
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Frontend") || !strings.Contains(msg, "Provider") || !strings.Contains(msg, "Store") {
		t.Errorf("error should mention missing components, got: %q", msg)
	}
}

func TestAppDefaults(t *testing.T) {
	a := New()
	if a.maxIter != 10 {
		t.Errorf("default maxIter = %d, want 10", a.maxIter)
	}
	if a.systemPrompt != "" {
		t.Errorf("default systemPrompt should be empty, got %q", a.systemPrompt)
	}
	if a.frontend != nil {
		t.Error("default frontend should be nil")
	}
	if a.provider != nil {
		t.Error("default provider should be nil")
	}
	if a.store != nil {
		t.Error("default store should be nil")
	}
}

func TestAppAddTool(t *testing.T) {
	a := New()
	a.AddTool(mockTool{})

	defs := a.tools.AllDefinitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(defs))
	}
	if defs[0].Name != "greet" {
		t.Errorf("expected 'greet', got %q", defs[0].Name)
	}
}

func TestAppStoreAccessor(t *testing.T) {
	s := &stubStore{}
	a := New(WithStore(s))
	if a.Store() != s {
		t.Error("Store() should return the configured store")
	}
}

func TestAppEmbeddingAccessor(t *testing.T) {
	a := New()
	if a.Embedding() != nil {
		t.Error("Embedding() should be nil when not configured")
	}
}

func TestAppRunContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	fe := &stubFrontend{ch: make(chan IncomingMessage)}
	s := &stubStore{}

	a := New(
		WithFrontend(fe),
		WithProvider(&mockProvider{name: "test"}),
		WithStore(s),
	)

	done := make(chan error, 1)
	go func() {
		done <- a.Run(ctx)
	}()

	cancel()
	err := <-done
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// --- Test doubles for App tests ---

// stubFrontend is a minimal Frontend for testing App.
type stubFrontend struct {
	ch       chan IncomingMessage
	sent     []string
	edited   []string
}

func (f *stubFrontend) Poll(_ context.Context) (<-chan IncomingMessage, error) {
	if f.ch == nil {
		f.ch = make(chan IncomingMessage)
	}
	return f.ch, nil
}
func (f *stubFrontend) Send(_ context.Context, _ string, text string) (string, error) {
	f.sent = append(f.sent, text)
	return "msg-1", nil
}
func (f *stubFrontend) Edit(_ context.Context, _, _ string, text string) error {
	f.edited = append(f.edited, text)
	return nil
}
func (f *stubFrontend) EditFormatted(_ context.Context, _, _ string, text string) error {
	f.edited = append(f.edited, text)
	return nil
}
func (f *stubFrontend) SendTyping(_ context.Context, _ string) error { return nil }
func (f *stubFrontend) DownloadFile(_ context.Context, _ string) ([]byte, string, error) {
	return nil, "", nil
}

// stubStore is a minimal Store for testing App.
type stubStore struct {
	initCalled bool
}

func (s *stubStore) Init(_ context.Context) error { s.initCalled = true; return nil }
func (s *stubStore) Close() error                 { return nil }

// Thread stubs
func (s *stubStore) CreateThread(_ context.Context, _ Thread) error          { return nil }
func (s *stubStore) GetThread(_ context.Context, _ string) (Thread, error)   { return Thread{}, nil }
func (s *stubStore) ListThreads(_ context.Context, _ string, _ int) ([]Thread, error) {
	return nil, nil
}
func (s *stubStore) UpdateThread(_ context.Context, _ Thread) error { return nil }
func (s *stubStore) DeleteThread(_ context.Context, _ string) error { return nil }

// Message stubs
func (s *stubStore) StoreMessage(_ context.Context, _ Message) error { return nil }
func (s *stubStore) GetMessages(_ context.Context, _ string, _ int) ([]Message, error) {
	return nil, nil
}
func (s *stubStore) SearchMessages(_ context.Context, _ []float32, _ int) ([]Message, error) {
	return nil, nil
}

// Document stubs
func (s *stubStore) StoreDocument(_ context.Context, _ Document, _ []Chunk) error { return nil }
func (s *stubStore) SearchChunks(_ context.Context, _ []float32, _ int) ([]Chunk, error) {
	return nil, nil
}
func (s *stubStore) GetChunksByIDs(_ context.Context, _ []string) ([]Chunk, error) {
	return nil, nil
}

// Config stubs
func (s *stubStore) GetConfig(_ context.Context, _ string) (string, error) { return "", nil }
func (s *stubStore) SetConfig(_ context.Context, _, _ string) error        { return nil }

// ScheduledAction stubs
func (s *stubStore) CreateScheduledAction(_ context.Context, _ ScheduledAction) error { return nil }
func (s *stubStore) ListScheduledActions(_ context.Context) ([]ScheduledAction, error) {
	return nil, nil
}
func (s *stubStore) GetDueScheduledActions(_ context.Context, _ int64) ([]ScheduledAction, error) {
	return nil, nil
}
func (s *stubStore) UpdateScheduledAction(_ context.Context, _ ScheduledAction) error { return nil }
func (s *stubStore) UpdateScheduledActionEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}
func (s *stubStore) DeleteScheduledAction(_ context.Context, _ string) error    { return nil }
func (s *stubStore) DeleteAllScheduledActions(_ context.Context) (int, error)   { return 0, nil }
func (s *stubStore) FindScheduledActionsByDescription(_ context.Context, _ string) ([]ScheduledAction, error) {
	return nil, nil
}

// Skill stubs
func (s *stubStore) CreateSkill(_ context.Context, _ Skill) error           { return nil }
func (s *stubStore) GetSkill(_ context.Context, _ string) (Skill, error)    { return Skill{}, nil }
func (s *stubStore) ListSkills(_ context.Context) ([]Skill, error)          { return nil, nil }
func (s *stubStore) UpdateSkill(_ context.Context, _ Skill) error           { return nil }
func (s *stubStore) DeleteSkill(_ context.Context, _ string) error          { return nil }
func (s *stubStore) SearchSkills(_ context.Context, _ []float32, _ int) ([]Skill, error) {
	return nil, nil
}
