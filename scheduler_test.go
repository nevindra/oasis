package oasis

import (
	"context"
	"encoding/json"
	"testing"
)

// --- parseScheduledToolCalls tests ---

func TestParseScheduledToolCallsDirect(t *testing.T) {
	raw := `[{"tool":"search","params":{"q":"test"}}]`
	calls, ok := parseScheduledToolCalls(raw)
	if !ok {
		t.Fatal("expected ok for direct format")
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Tool != "search" {
		t.Errorf("Tool = %q, want %q", calls[0].Tool, "search")
	}
	var params map[string]string
	if err := json.Unmarshal(calls[0].Params, &params); err != nil {
		t.Fatalf("failed to unmarshal params: %v", err)
	}
	if params["q"] != "test" {
		t.Errorf("params[q] = %q, want %q", params["q"], "test")
	}
}

func TestParseScheduledToolCallsLegacy(t *testing.T) {
	// Array of JSON-encoded strings (legacy format).
	raw := `["{\"tool\":\"search\",\"params\":{\"q\":\"hello\"}}"]`
	calls, ok := parseScheduledToolCalls(raw)
	if !ok {
		t.Fatal("expected ok for legacy format")
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Tool != "search" {
		t.Errorf("Tool = %q, want %q", calls[0].Tool, "search")
	}
	var params map[string]string
	if err := json.Unmarshal(calls[0].Params, &params); err != nil {
		t.Fatalf("failed to unmarshal params: %v", err)
	}
	if params["q"] != "hello" {
		t.Errorf("params[q] = %q, want %q", params["q"], "hello")
	}
}

func TestParseScheduledToolCallsInvalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"garbage text", "not json at all", false},
		{"json object not array", `{"tool":"search"}`, false},
		{"array of numbers", `[123, 456]`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ok := parseScheduledToolCalls(c.raw)
			if ok != c.ok {
				t.Errorf("parseScheduledToolCalls(%q) ok = %v, want %v", c.raw, ok, c.ok)
			}
		})
	}
}

func TestParseScheduledToolCallsEmpty(t *testing.T) {
	_, ok := parseScheduledToolCalls("[]")
	if ok {
		t.Error("expected not ok for empty array")
	}
}

func TestParseScheduledToolCallsMultiple(t *testing.T) {
	raw := `[
		{"tool":"search","params":{"q":"news"}},
		{"tool":"weather","params":{"city":"Jakarta"}},
		{"tool":"stocks","params":{"symbol":"GOOG"}}
	]`
	calls, ok := parseScheduledToolCalls(raw)
	if !ok {
		t.Fatal("expected ok")
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}

	expected := []string{"search", "weather", "stocks"}
	for i, want := range expected {
		if calls[i].Tool != want {
			t.Errorf("calls[%d].Tool = %q, want %q", i, calls[i].Tool, want)
		}
	}
}

// --- Stubs for scheduler tests ---

// schedStore is a stub Store that records scheduler-relevant method calls.
type schedStore struct {
	stubStore // embed base stubs for all Store methods

	dueActions       []ScheduledAction
	ownerID          string
	enabledCalls     []schedEnabledCall
	updatedActions   []ScheduledAction
}

type schedEnabledCall struct {
	ID      string
	Enabled bool
}

func (s *schedStore) GetDueScheduledActions(_ context.Context, _ int64) ([]ScheduledAction, error) {
	return s.dueActions, nil
}

func (s *schedStore) GetConfig(_ context.Context, key string) (string, error) {
	if key == "owner_user_id" {
		return s.ownerID, nil
	}
	return "", nil
}

func (s *schedStore) UpdateScheduledActionEnabled(_ context.Context, id string, enabled bool) error {
	s.enabledCalls = append(s.enabledCalls, schedEnabledCall{ID: id, Enabled: enabled})
	return nil
}

func (s *schedStore) UpdateScheduledAction(_ context.Context, action ScheduledAction) error {
	s.updatedActions = append(s.updatedActions, action)
	return nil
}

// --- scheduler.execute tests ---

func TestSchedulerExecuteOnce(t *testing.T) {
	store := &schedStore{ownerID: "user123"}
	fe := &stubFrontend{}
	reg := NewToolRegistry()
	reg.Add(mockTool{}) // provides "greet"

	s := &scheduler{
		store:    store,
		tools:    reg,
		frontend: fe,
		provider: &mockProvider{name: "test"},
		tzOffset: 7,
	}

	action := ScheduledAction{
		ID:          "act-1",
		Description: "One-time greet",
		Schedule:    "08:00 once",
		ToolCalls:   `[{"tool":"greet","params":{}}]`,
		NextRun:     1000,
		Enabled:     true,
	}

	s.execute(context.Background(), action, "user123", 2000)

	// Verify the frontend received a message.
	if len(fe.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(fe.sent))
	}

	// Verify UpdateScheduledActionEnabled was called with enabled=false (one-shot).
	if len(store.enabledCalls) != 1 {
		t.Fatalf("expected 1 enabledCall, got %d", len(store.enabledCalls))
	}
	if store.enabledCalls[0].ID != "act-1" {
		t.Errorf("enabledCall ID = %q, want %q", store.enabledCalls[0].ID, "act-1")
	}
	if store.enabledCalls[0].Enabled != false {
		t.Error("expected Enabled=false for once schedule")
	}

	// Verify UpdateScheduledAction was NOT called (once schedules disable, not update).
	if len(store.updatedActions) != 0 {
		t.Errorf("expected 0 updatedActions for once schedule, got %d", len(store.updatedActions))
	}
}

func TestSchedulerExecuteRecurring(t *testing.T) {
	store := &schedStore{ownerID: "user123"}
	fe := &stubFrontend{}
	reg := NewToolRegistry()
	reg.Add(mockTool{})

	s := &scheduler{
		store:    store,
		tools:    reg,
		frontend: fe,
		provider: &mockProvider{name: "test"},
		tzOffset: 7,
	}

	now := int64(1771322400) // 2026-02-17 10:00 UTC
	action := ScheduledAction{
		ID:          "act-2",
		Description: "Daily greet",
		Schedule:    "08:00 daily",
		ToolCalls:   `[{"tool":"greet","params":{}}]`,
		NextRun:     now - 60,
		Enabled:     true,
	}

	s.execute(context.Background(), action, "user123", now)

	// Verify the frontend received a message.
	if len(fe.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(fe.sent))
	}

	// Verify UpdateScheduledAction was called with an advanced NextRun.
	if len(store.updatedActions) != 1 {
		t.Fatalf("expected 1 updatedAction, got %d", len(store.updatedActions))
	}
	updated := store.updatedActions[0]
	if updated.NextRun <= now {
		t.Errorf("NextRun (%d) should be after now (%d)", updated.NextRun, now)
	}

	// Verify UpdateScheduledActionEnabled was NOT called (recurring actions stay enabled).
	if len(store.enabledCalls) != 0 {
		t.Errorf("expected 0 enabledCalls for recurring schedule, got %d", len(store.enabledCalls))
	}
}

func TestSchedulerExecuteWithSynthesis(t *testing.T) {
	store := &schedStore{ownerID: "user123"}
	fe := &stubFrontend{}
	reg := NewToolRegistry()
	reg.Add(mockTool{})

	provider := &mockProvider{
		name: "synth",
		responses: []ChatResponse{
			{Content: "Synthesized report: all good"},
		},
	}

	s := &scheduler{
		store:    store,
		tools:    reg,
		frontend: fe,
		provider: provider,
		tzOffset: 7,
	}

	action := ScheduledAction{
		ID:              "act-3",
		Description:     "Daily report",
		Schedule:        "08:00 once",
		ToolCalls:       `[{"tool":"greet","params":{}}]`,
		SynthesisPrompt: "Summarize concisely",
		NextRun:         1000,
		Enabled:         true,
	}

	s.execute(context.Background(), action, "user123", 2000)

	// Verify provider.Chat was called (mockProvider index advanced).
	if provider.idx != 1 {
		t.Errorf("provider.idx = %d, want 1 (Chat should have been called)", provider.idx)
	}

	// Verify the synthesized content was sent to the frontend.
	if len(fe.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(fe.sent))
	}
	if fe.sent[0] != "Synthesized report: all good" {
		t.Errorf("sent = %q, want %q", fe.sent[0], "Synthesized report: all good")
	}
}

func TestSchedulerExecuteInvalidToolCalls(t *testing.T) {
	store := &schedStore{ownerID: "user123"}
	fe := &stubFrontend{}
	reg := NewToolRegistry()

	s := &scheduler{
		store:    store,
		tools:    reg,
		frontend: fe,
		provider: &mockProvider{name: "test"},
		tzOffset: 7,
	}

	action := ScheduledAction{
		ID:          "act-4",
		Description: "Broken action",
		Schedule:    "08:00 daily",
		ToolCalls:   "not valid json!!!",
		NextRun:     1000,
		Enabled:     true,
	}

	// Should not panic.
	s.execute(context.Background(), action, "user123", 2000)

	// Nothing should be sent — the action bails out early on invalid tool calls.
	if len(fe.sent) != 0 {
		t.Errorf("expected 0 sent messages for invalid tool_calls, got %d", len(fe.sent))
	}

	// No schedule update should happen either.
	if len(store.enabledCalls) != 0 {
		t.Errorf("expected 0 enabledCalls, got %d", len(store.enabledCalls))
	}
	if len(store.updatedActions) != 0 {
		t.Errorf("expected 0 updatedActions, got %d", len(store.updatedActions))
	}
}

// --- checkAndRun tests ---

func TestSchedulerCheckAndRunNoDue(t *testing.T) {
	store := &schedStore{
		ownerID:    "user123",
		dueActions: nil, // no due actions
	}
	fe := &stubFrontend{}

	s := &scheduler{
		store:    store,
		tools:    NewToolRegistry(),
		frontend: fe,
		provider: &mockProvider{name: "test"},
		tzOffset: 7,
	}

	err := s.checkAndRun(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nothing should be sent.
	if len(fe.sent) != 0 {
		t.Errorf("expected 0 sent messages, got %d", len(fe.sent))
	}
}

func TestSchedulerCheckAndRunNoOwner(t *testing.T) {
	store := &schedStore{
		ownerID: "", // no owner configured
		dueActions: []ScheduledAction{
			{
				ID:          "act-5",
				Description: "Should be skipped",
				Schedule:    "08:00 daily",
				ToolCalls:   `[{"tool":"greet","params":{}}]`,
				NextRun:     1000,
				Enabled:     true,
			},
		},
	}
	fe := &stubFrontend{}
	reg := NewToolRegistry()
	reg.Add(mockTool{})

	s := &scheduler{
		store:    store,
		tools:    reg,
		frontend: fe,
		provider: &mockProvider{name: "test"},
		tzOffset: 7,
	}

	err := s.checkAndRun(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Nothing should be sent — no owner means skip silently.
	if len(fe.sent) != 0 {
		t.Errorf("expected 0 sent messages when no owner configured, got %d", len(fe.sent))
	}
}
