package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// --- mocks ---

type mockEmb struct{}

func (m *mockEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}
func (m *mockEmb) Dimensions() int { return 3 }
func (m *mockEmb) Name() string    { return "mock" }

// mockStore records skill operations for assertions.
type mockStore struct {
	nopStore
	created []oasis.Skill
	updated []oasis.Skill
	skills  map[string]oasis.Skill
	search  []oasis.ScoredSkill
}

func newMockStore() *mockStore {
	return &mockStore{skills: make(map[string]oasis.Skill)}
}

func (s *mockStore) CreateSkill(_ context.Context, skill oasis.Skill) error {
	s.created = append(s.created, skill)
	s.skills[skill.ID] = skill
	return nil
}

func (s *mockStore) GetSkill(_ context.Context, id string) (oasis.Skill, error) {
	sk, ok := s.skills[id]
	if !ok {
		return oasis.Skill{}, fmt.Errorf("not found: %s", id)
	}
	return sk, nil
}

func (s *mockStore) UpdateSkill(_ context.Context, skill oasis.Skill) error {
	s.updated = append(s.updated, skill)
	s.skills[skill.ID] = skill
	return nil
}

func (s *mockStore) SearchSkills(_ context.Context, _ []float32, _ int) ([]oasis.ScoredSkill, error) {
	return s.search, nil
}

// --- nopStore satisfies oasis.Store with no-ops ---

type nopStore struct{}

func (nopStore) CreateThread(_ context.Context, _ oasis.Thread) error              { return nil }
func (nopStore) GetThread(_ context.Context, _ string) (oasis.Thread, error)       { return oasis.Thread{}, nil }
func (nopStore) ListThreads(_ context.Context, _ string, _ int) ([]oasis.Thread, error) {
	return nil, nil
}
func (nopStore) UpdateThread(_ context.Context, _ oasis.Thread) error                  { return nil }
func (nopStore) DeleteThread(_ context.Context, _ string) error                        { return nil }
func (nopStore) StoreMessage(_ context.Context, _ oasis.Message) error                 { return nil }
func (nopStore) GetMessages(_ context.Context, _ string, _ int) ([]oasis.Message, error) {
	return nil, nil
}
func (nopStore) SearchMessages(_ context.Context, _ []float32, _ int) ([]oasis.ScoredMessage, error) {
	return nil, nil
}
func (nopStore) StoreDocument(_ context.Context, _ oasis.Document, _ []oasis.Chunk) error {
	return nil
}
func (nopStore) ListDocuments(_ context.Context, _ int) ([]oasis.Document, error) { return nil, nil }
func (nopStore) DeleteDocument(_ context.Context, _ string) error                 { return nil }
func (nopStore) SearchChunks(_ context.Context, _ []float32, _ int, _ ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	return nil, nil
}
func (nopStore) GetChunksByIDs(_ context.Context, _ []string) ([]oasis.Chunk, error) {
	return nil, nil
}
func (nopStore) GetConfig(_ context.Context, _ string) (string, error)    { return "", nil }
func (nopStore) SetConfig(_ context.Context, _, _ string) error           { return nil }
func (nopStore) CreateScheduledAction(_ context.Context, _ oasis.ScheduledAction) error {
	return nil
}
func (nopStore) ListScheduledActions(_ context.Context) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (nopStore) GetDueScheduledActions(_ context.Context, _ int64) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (nopStore) UpdateScheduledAction(_ context.Context, _ oasis.ScheduledAction) error { return nil }
func (nopStore) UpdateScheduledActionEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}
func (nopStore) DeleteScheduledAction(_ context.Context, _ string) error    { return nil }
func (nopStore) DeleteAllScheduledActions(_ context.Context) (int, error)   { return 0, nil }
func (nopStore) FindScheduledActionsByDescription(_ context.Context, _ string) ([]oasis.ScheduledAction, error) {
	return nil, nil
}
func (nopStore) CreateSkill(_ context.Context, _ oasis.Skill) error            { return nil }
func (nopStore) GetSkill(_ context.Context, _ string) (oasis.Skill, error)     { return oasis.Skill{}, nil }
func (nopStore) ListSkills(_ context.Context) ([]oasis.Skill, error)           { return nil, nil }
func (nopStore) UpdateSkill(_ context.Context, _ oasis.Skill) error            { return nil }
func (nopStore) DeleteSkill(_ context.Context, _ string) error                 { return nil }
func (nopStore) SearchSkills(_ context.Context, _ []float32, _ int) ([]oasis.ScoredSkill, error) {
	return nil, nil
}
func (nopStore) Init(_ context.Context) error { return nil }
func (nopStore) Close() error                 { return nil }

// --- tests ---

func TestDefinitions(t *testing.T) {
	tool := New(newMockStore(), &mockEmb{})
	defs := tool.Definitions()
	if len(defs) != 3 {
		t.Fatalf("expected 3 definitions, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"skill_search", "skill_create", "skill_update"} {
		if !names[want] {
			t.Errorf("missing definition %q", want)
		}
	}
}

func TestUnknownAction(t *testing.T) {
	tool := New(newMockStore(), &mockEmb{})
	result, err := tool.Execute(context.Background(), "skill_delete", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for unknown action")
	}
}

func TestSearch(t *testing.T) {
	store := newMockStore()
	store.search = []oasis.ScoredSkill{
		{Skill: oasis.Skill{ID: "s1", Name: "coding", Description: "Write code", Instructions: "Write clean code.", Tags: []string{"dev"}}, Score: 0.95},
		{Skill: oasis.Skill{ID: "s2", Name: "research", Description: "Research topics", Instructions: "Search the web."}, Score: 0.80},
	}
	tool := New(store, &mockEmb{})

	args, _ := json.Marshal(map[string]string{"query": "write some code"})
	result, err := tool.Execute(context.Background(), "skill_search", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "coding") {
		t.Errorf("expected 'coding' in results, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Tags: dev") {
		t.Errorf("expected tags in results, got: %s", result.Content)
	}
}

func TestSearchEmpty(t *testing.T) {
	tool := New(newMockStore(), &mockEmb{})
	args, _ := json.Marshal(map[string]string{"query": "something obscure"})
	result, err := tool.Execute(context.Background(), "skill_search", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "no skills found") {
		t.Errorf("expected 'no skills found', got: %s", result.Content)
	}
}

func TestSearchMissingQuery(t *testing.T) {
	tool := New(newMockStore(), &mockEmb{})
	result, err := tool.Execute(context.Background(), "skill_search", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for missing query")
	}
}

func TestCreate(t *testing.T) {
	store := newMockStore()
	tool := New(store, &mockEmb{})

	// Set up task context with user ID.
	ctx := oasis.WithTaskContext(context.Background(), oasis.AgentTask{
		Input: "test",
	}.WithUserID("user-42"))

	args, _ := json.Marshal(map[string]any{
		"name":         "code-reviewer",
		"description":  "Review code changes",
		"instructions": "Analyze code for correctness and style.",
		"tags":         []string{"dev", "review"},
		"references":   []string{"skill-base"},
	})
	result, err := tool.Execute(ctx, "skill_create", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "code-reviewer") {
		t.Errorf("expected skill name in result, got: %s", result.Content)
	}

	if len(store.created) != 1 {
		t.Fatalf("expected 1 created skill, got %d", len(store.created))
	}
	sk := store.created[0]
	if sk.Name != "code-reviewer" {
		t.Errorf("name = %q, want %q", sk.Name, "code-reviewer")
	}
	if sk.CreatedBy != "user-42" {
		t.Errorf("created_by = %q, want %q", sk.CreatedBy, "user-42")
	}
	if len(sk.Tags) != 2 || sk.Tags[0] != "dev" {
		t.Errorf("tags = %v, want [dev, review]", sk.Tags)
	}
	if len(sk.References) != 1 || sk.References[0] != "skill-base" {
		t.Errorf("references = %v, want [skill-base]", sk.References)
	}
	if len(sk.Embedding) == 0 {
		t.Error("expected embedding to be set")
	}
}

func TestCreateMissingFields(t *testing.T) {
	tool := New(newMockStore(), &mockEmb{})

	args, _ := json.Marshal(map[string]string{"name": "incomplete"})
	result, err := tool.Execute(context.Background(), "skill_create", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for missing required fields")
	}
}

func TestCreateDefaultCreatedBy(t *testing.T) {
	store := newMockStore()
	tool := New(store, &mockEmb{})

	// No task context → CreatedBy should be "unknown".
	args, _ := json.Marshal(map[string]any{
		"name":         "test-skill",
		"description":  "test",
		"instructions": "test",
	})
	tool.Execute(context.Background(), "skill_create", args)

	if len(store.created) != 1 {
		t.Fatalf("expected 1 created skill, got %d", len(store.created))
	}
	if store.created[0].CreatedBy != "unknown" {
		t.Errorf("created_by = %q, want %q", store.created[0].CreatedBy, "unknown")
	}
}

func TestUpdate(t *testing.T) {
	store := newMockStore()
	store.skills["sk-1"] = oasis.Skill{
		ID:           "sk-1",
		Name:         "old-name",
		Description:  "old desc",
		Instructions: "old instructions",
		Tags:         []string{"old"},
	}
	tool := New(store, &mockEmb{})

	newName := "new-name"
	newDesc := "new description"
	args, _ := json.Marshal(map[string]any{
		"id":          "sk-1",
		"name":        newName,
		"description": newDesc,
		"tags":        []string{"new", "updated"},
	})
	result, err := tool.Execute(context.Background(), "skill_update", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "name") || !strings.Contains(result.Content, "description") {
		t.Errorf("expected changed fields in result, got: %s", result.Content)
	}

	if len(store.updated) != 1 {
		t.Fatalf("expected 1 updated skill, got %d", len(store.updated))
	}
	sk := store.updated[0]
	if sk.Name != "new-name" {
		t.Errorf("name = %q, want %q", sk.Name, "new-name")
	}
	if sk.Description != "new description" {
		t.Errorf("description = %q, want %q", sk.Description, "new description")
	}
	if sk.Instructions != "old instructions" {
		t.Errorf("instructions should be unchanged, got %q", sk.Instructions)
	}
	if len(sk.Tags) != 2 || sk.Tags[0] != "new" {
		t.Errorf("tags = %v, want [new, updated]", sk.Tags)
	}
	// Description changed → should re-embed.
	if len(sk.Embedding) == 0 {
		t.Error("expected embedding to be refreshed after description change")
	}
}

func TestUpdateNoChanges(t *testing.T) {
	store := newMockStore()
	store.skills["sk-1"] = oasis.Skill{ID: "sk-1", Name: "test"}
	tool := New(store, &mockEmb{})

	args, _ := json.Marshal(map[string]string{"id": "sk-1"})
	result, err := tool.Execute(context.Background(), "skill_update", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "no changes") {
		t.Errorf("expected 'no changes', got: %s", result.Content)
	}
}

func TestUpdateNotFound(t *testing.T) {
	tool := New(newMockStore(), &mockEmb{})
	args, _ := json.Marshal(map[string]any{"id": "nonexistent", "name": "x"})
	result, err := tool.Execute(context.Background(), "skill_update", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for nonexistent skill")
	}
}
