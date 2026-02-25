package sqlite

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nevindra/oasis"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s := New(filepath.Join(t.TempDir(), "test.db"))
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return s
}

func TestInitIdempotent(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "init.db"))
	ctx := context.Background()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}

func TestStoreAndGetMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := oasis.NowUnix()
	thread := oasis.Thread{ID: oasis.NewID(), ChatID: "chat-1", CreatedAt: now, UpdatedAt: now}
	s.CreateThread(ctx, thread)

	msgs := []oasis.Message{
		{ID: oasis.NewID(), ThreadID: thread.ID, Role: "user", Content: "Hello", CreatedAt: 1000},
		{ID: oasis.NewID(), ThreadID: thread.ID, Role: "assistant", Content: "Hi!", CreatedAt: 1001},
		{ID: oasis.NewID(), ThreadID: thread.ID, Role: "user", Content: "Bye", CreatedAt: 1002},
	}
	for _, m := range msgs {
		if err := s.StoreMessage(ctx, m); err != nil {
			t.Fatalf("StoreMessage: %v", err)
		}
	}

	got, err := s.GetMessages(ctx, thread.ID, 10)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	if got[0].Content != "Hello" || got[2].Content != "Bye" {
		t.Error("messages not in chronological order")
	}

	// Test limit returns most recent
	got2, _ := s.GetMessages(ctx, thread.ID, 2)
	if len(got2) != 2 || got2[0].Content != "Hi!" {
		t.Errorf("limit 2: expected [Hi!, Bye], got %v", got2)
	}
}

func TestThreadCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := oasis.NowUnix()
	thread := oasis.Thread{ID: oasis.NewID(), ChatID: "chat-abc", Title: "Test Thread", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	// Get
	got, err := s.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.ChatID != "chat-abc" || got.Title != "Test Thread" {
		t.Errorf("unexpected thread: %+v", got)
	}

	// List
	threads, err := s.ListThreads(ctx, "chat-abc", 10)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(threads))
	}

	// Update
	thread.Title = "Updated"
	thread.UpdatedAt = oasis.NowUnix()
	if err := s.UpdateThread(ctx, thread); err != nil {
		t.Fatalf("UpdateThread: %v", err)
	}
	got, _ = s.GetThread(ctx, thread.ID)
	if got.Title != "Updated" {
		t.Errorf("expected title 'Updated', got %q", got.Title)
	}

	// Delete
	if err := s.DeleteThread(ctx, thread.ID); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	threads, _ = s.ListThreads(ctx, "chat-abc", 10)
	if len(threads) != 0 {
		t.Fatalf("expected 0 threads after delete, got %d", len(threads))
	}
}

func TestConfig(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	val, _ := s.GetConfig(ctx, "missing")
	if val != "" {
		t.Errorf("missing key should return empty, got %q", val)
	}

	s.SetConfig(ctx, "k", "v1")
	val, _ = s.GetConfig(ctx, "k")
	if val != "v1" {
		t.Errorf("expected v1, got %q", val)
	}

	s.SetConfig(ctx, "k", "v2")
	val, _ = s.GetConfig(ctx, "k")
	if val != "v2" {
		t.Errorf("expected v2, got %q", val)
	}
}

func TestStoreDocument(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	doc := oasis.Document{
		ID: oasis.NewID(), Title: "Test", Source: "test",
		Content: "full content", CreatedAt: oasis.NowUnix(),
	}
	chunks := []oasis.Chunk{
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "chunk 1", ChunkIndex: 0},
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "chunk 2", ChunkIndex: 1},
	}

	if err := s.StoreDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("StoreDocument: %v", err)
	}

	// Verify via raw query
	var count int
	s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chunks WHERE document_id = ?", doc.ID).Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 chunks, got %d", count)
	}
}

func TestSearchMessages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := oasis.NowUnix()
	thread := oasis.Thread{ID: oasis.NewID(), ChatID: "chat-vec", CreatedAt: now, UpdatedAt: now}
	s.CreateThread(ctx, thread)

	// Store messages with embeddings
	msgs := []oasis.Message{
		{ID: oasis.NewID(), ThreadID: thread.ID, Role: "user", Content: "about cats", Embedding: []float32{1, 0, 0}, CreatedAt: 1},
		{ID: oasis.NewID(), ThreadID: thread.ID, Role: "user", Content: "about dogs", Embedding: []float32{0, 1, 0}, CreatedAt: 2},
		{ID: oasis.NewID(), ThreadID: thread.ID, Role: "user", Content: "about birds", Embedding: []float32{0, 0, 1}, CreatedAt: 3},
	}
	for _, m := range msgs {
		s.StoreMessage(ctx, m)
	}

	// Search for cats-like vector
	results, err := s.SearchMessages(ctx, []float32{0.9, 0.1, 0}, 2)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Content != "about cats" {
		t.Errorf("top result should be 'about cats', got %q", results[0].Content)
	}
}

func TestSearchChunks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	doc := oasis.Document{ID: oasis.NewID(), Title: "Test", Source: "t", Content: "c", CreatedAt: 1}
	chunks := []oasis.Chunk{
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "rust", ChunkIndex: 0, Embedding: []float32{1, 0}},
		{ID: oasis.NewID(), DocumentID: doc.ID, Content: "go", ChunkIndex: 1, Embedding: []float32{0, 1}},
	}
	s.StoreDocument(ctx, doc, chunks)

	results, err := s.SearchChunks(ctx, []float32{0.8, 0.2}, 1)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(results) != 1 || results[0].Content != "rust" {
		t.Errorf("expected top result 'rust', got %v", results)
	}
}

func TestSearchChunks_ExcludeDocument(t *testing.T) {
	ctx := context.Background()
	s := New(":memory:")
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Store two documents with chunks.
	doc1 := oasis.Document{ID: "d1", Title: "doc1", CreatedAt: oasis.NowUnix()}
	doc2 := oasis.Document{ID: "d2", Title: "doc2", CreatedAt: oasis.NowUnix()}
	emb := []float32{0.1, 0.2, 0.3}
	c1 := oasis.Chunk{ID: "c1", DocumentID: "d1", Content: "hello", Embedding: emb}
	c2 := oasis.Chunk{ID: "c2", DocumentID: "d2", Content: "world", Embedding: emb}

	if err := s.StoreDocument(ctx, doc1, []oasis.Chunk{c1}); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreDocument(ctx, doc2, []oasis.Chunk{c2}); err != nil {
		t.Fatal(err)
	}

	// Search excluding d1 — should only find c2.
	results, err := s.SearchChunks(ctx, emb, 10, oasis.ByExcludeDocument("d1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("len = %d, want 1", len(results))
	}
	if results[0].ID != "c2" {
		t.Errorf("got chunk %q, want c2", results[0].ID)
	}
}

func TestScheduledActions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	action := oasis.ScheduledAction{
		ID: oasis.NewID(), Description: "daily briefing",
		Schedule: "08:00 daily", ToolCalls: `[{"tool":"web_search","params":{"query":"news"}}]`,
		NextRun: oasis.NowUnix() + 3600, Enabled: true, CreatedAt: oasis.NowUnix(),
	}
	if err := s.CreateScheduledAction(ctx, action); err != nil {
		t.Fatal(err)
	}

	// List
	actions, _ := s.ListScheduledActions(ctx)
	if len(actions) != 1 || actions[0].Description != "daily briefing" {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}

	// Find by description
	found, _ := s.FindScheduledActionsByDescription(ctx, "briefing")
	if len(found) != 1 {
		t.Fatal("expected 1 match")
	}

	// Get due (none should be due yet if next_run is in the future)
	due, _ := s.GetDueScheduledActions(ctx, oasis.NowUnix())
	if len(due) != 0 {
		t.Fatal("expected 0 due")
	}

	// Get due (with past next_run)
	action.NextRun = oasis.NowUnix() - 60
	s.UpdateScheduledAction(ctx, action)
	due, _ = s.GetDueScheduledActions(ctx, oasis.NowUnix())
	if len(due) != 1 {
		t.Fatal("expected 1 due")
	}

	// Disable
	s.UpdateScheduledActionEnabled(ctx, action.ID, false)
	due, _ = s.GetDueScheduledActions(ctx, oasis.NowUnix()+99999)
	if len(due) != 0 {
		t.Fatal("disabled action should not be due")
	}

	// Delete
	s.DeleteScheduledAction(ctx, action.ID)
	actions, _ = s.ListScheduledActions(ctx)
	if len(actions) != 0 {
		t.Fatal("expected 0 after delete")
	}
}

func TestSkillCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	skill := oasis.Skill{
		ID:           oasis.NewID(),
		Name:         "web-research",
		Description:  "Research topics on the web",
		Instructions: "Use web_search to find information, then summarize.",
		Tools:        []string{"web_search", "browse"},
		Model:        "gpt-4o",
		Tags:         []string{"research", "web"},
		CreatedBy:    "agent-001",
		References:   []string{"skill-abc"},
		CreatedAt:    oasis.NowUnix(),
		UpdatedAt:    oasis.NowUnix(),
	}

	// Create
	if err := s.CreateSkill(ctx, skill); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	// Get
	got, err := s.GetSkill(ctx, skill.ID)
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if got.Name != "web-research" {
		t.Errorf("expected name 'web-research', got %q", got.Name)
	}
	if got.Description != "Research topics on the web" {
		t.Errorf("expected description mismatch, got %q", got.Description)
	}
	if len(got.Tools) != 2 || got.Tools[0] != "web_search" {
		t.Errorf("expected tools [web_search, browse], got %v", got.Tools)
	}
	if got.Model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", got.Model)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "research" {
		t.Errorf("expected tags [research, web], got %v", got.Tags)
	}
	if got.CreatedBy != "agent-001" {
		t.Errorf("expected created_by 'agent-001', got %q", got.CreatedBy)
	}
	if len(got.References) != 1 || got.References[0] != "skill-abc" {
		t.Errorf("expected references [skill-abc], got %v", got.References)
	}

	// List
	skills, err := s.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	// Update
	skill.Name = "deep-research"
	skill.Instructions = "Updated instructions"
	skill.Tags = []string{"research", "deep"}
	skill.UpdatedAt = oasis.NowUnix()
	if err := s.UpdateSkill(ctx, skill); err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}
	got, _ = s.GetSkill(ctx, skill.ID)
	if got.Name != "deep-research" {
		t.Errorf("after update: expected name 'deep-research', got %q", got.Name)
	}
	if got.Instructions != "Updated instructions" {
		t.Errorf("after update: expected updated instructions, got %q", got.Instructions)
	}
	if len(got.Tags) != 2 || got.Tags[1] != "deep" {
		t.Errorf("after update: expected tags [research, deep], got %v", got.Tags)
	}

	// Create a second skill, then delete the first
	skill2 := oasis.Skill{
		ID:           oasis.NewID(),
		Name:         "task-manager",
		Description:  "Manage tasks",
		Instructions: "Create and manage tasks.",
		CreatedAt:    oasis.NowUnix(),
		UpdatedAt:    oasis.NowUnix(),
	}
	s.CreateSkill(ctx, skill2)

	skills, _ = s.ListSkills(ctx)
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	// Delete
	if err := s.DeleteSkill(ctx, skill.ID); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
	skills, _ = s.ListSkills(ctx)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill after delete, got %d", len(skills))
	}
	if skills[0].Name != "task-manager" {
		t.Errorf("remaining skill should be 'task-manager', got %q", skills[0].Name)
	}
}

func TestSearchSkills(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	skills := []oasis.Skill{
		{
			ID: oasis.NewID(), Name: "coding", Description: "Write code",
			Instructions: "Write clean code.", Embedding: []float32{1, 0, 0},
			CreatedAt: oasis.NowUnix(), UpdatedAt: oasis.NowUnix(),
		},
		{
			ID: oasis.NewID(), Name: "research", Description: "Research topics",
			Instructions: "Search the web.", Embedding: []float32{0, 1, 0},
			CreatedAt: oasis.NowUnix(), UpdatedAt: oasis.NowUnix(),
		},
		{
			ID: oasis.NewID(), Name: "writing", Description: "Write content",
			Instructions: "Write articles.", Embedding: []float32{0, 0, 1},
			CreatedAt: oasis.NowUnix(), UpdatedAt: oasis.NowUnix(),
		},
	}
	for _, sk := range skills {
		if err := s.CreateSkill(ctx, sk); err != nil {
			t.Fatalf("CreateSkill: %v", err)
		}
	}

	// Search for coding-like vector
	results, err := s.SearchSkills(ctx, []float32{0.9, 0.1, 0}, 2)
	if err != nil {
		t.Fatalf("SearchSkills: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Name != "coding" {
		t.Errorf("top result should be 'coding', got %q", results[0].Name)
	}
	if results[1].Name != "research" {
		t.Errorf("second result should be 'research', got %q", results[1].Name)
	}

	// Search for writing-like vector
	results, err = s.SearchSkills(ctx, []float32{0, 0.1, 0.9}, 1)
	if err != nil {
		t.Fatalf("SearchSkills: %v", err)
	}
	if len(results) != 1 || results[0].Name != "writing" {
		t.Errorf("expected top result 'writing', got %v", results)
	}
}

func TestConcurrentWrites_NoBusyError(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	now := oasis.NowUnix()
	thread := oasis.Thread{ID: oasis.NewID(), ChatID: "concurrent-test", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatal(err)
	}

	const n = 20
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := oasis.Message{
				ID:        oasis.NewID(),
				ThreadID:  thread.ID,
				Role:      "user",
				Content:   fmt.Sprintf("message %d", i),
				CreatedAt: oasis.NowUnix(),
			}
			errs <- s.StoreMessage(ctx, msg)
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent write failed: %v", err)
		}
	}

	msgs, err := s.GetMessages(ctx, thread.ID, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != n {
		t.Errorf("expected %d messages stored, got %d", n, len(msgs))
	}
}

func TestGraphStore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Store a document with chunks first.
	doc := oasis.Document{ID: "d1", Title: "Test", Source: "test.txt", Content: "test", CreatedAt: 1}
	chunks := []oasis.Chunk{
		{ID: "c1", DocumentID: "d1", Content: "chunk one", ChunkIndex: 0},
		{ID: "c2", DocumentID: "d1", Content: "chunk two", ChunkIndex: 1},
		{ID: "c3", DocumentID: "d1", Content: "chunk three", ChunkIndex: 2},
	}
	if err := s.StoreDocument(ctx, doc, chunks); err != nil {
		t.Fatalf("StoreDocument: %v", err)
	}

	// Store edges.
	edges := []oasis.ChunkEdge{
		{ID: "e1", SourceID: "c1", TargetID: "c2", Relation: oasis.RelReferences, Weight: 0.9},
		{ID: "e2", SourceID: "c1", TargetID: "c3", Relation: oasis.RelElaborates, Weight: 0.7},
		{ID: "e3", SourceID: "c2", TargetID: "c3", Relation: oasis.RelSequence, Weight: 0.5},
	}
	if err := s.StoreEdges(ctx, edges); err != nil {
		t.Fatalf("StoreEdges: %v", err)
	}

	// GetEdges (outgoing from c1).
	got, err := s.GetEdges(ctx, []string{"c1"})
	if err != nil {
		t.Fatalf("GetEdges: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetEdges(c1): got %d edges, want 2", len(got))
	}

	// GetIncomingEdges (incoming to c3).
	got, err = s.GetIncomingEdges(ctx, []string{"c3"})
	if err != nil {
		t.Fatalf("GetIncomingEdges: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetIncomingEdges(c3): got %d edges, want 2", len(got))
	}

	// Delete document should cascade delete edges.
	if err := s.DeleteDocument(ctx, "d1"); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	got, err = s.GetEdges(ctx, []string{"c1"})
	if err != nil {
		t.Fatalf("GetEdges after delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("GetEdges after delete: got %d edges, want 0", len(got))
	}
}

func TestGraphStorePruneOrphan(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Insert orphan edges (no corresponding chunks).
	edges := []oasis.ChunkEdge{
		{ID: "e1", SourceID: "orphan1", TargetID: "orphan2", Relation: oasis.RelReferences, Weight: 0.9},
	}
	if err := s.StoreEdges(ctx, edges); err != nil {
		t.Fatalf("StoreEdges: %v", err)
	}

	pruned, err := s.PruneOrphanEdges(ctx)
	if err != nil {
		t.Fatalf("PruneOrphanEdges: %v", err)
	}
	if pruned != 1 {
		t.Fatalf("PruneOrphanEdges: pruned %d, want 1", pruned)
	}
}

func TestStoreEdges_Description(t *testing.T) {
	ctx := context.Background()
	s := New(":memory:")
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Store a document + chunk so edges have valid references.
	doc := oasis.Document{ID: "d1", Title: "test", CreatedAt: oasis.NowUnix()}
	chunk := oasis.Chunk{ID: "c1", DocumentID: "d1", Content: "hello", Embedding: []float32{0.1}}
	chunk2 := oasis.Chunk{ID: "c2", DocumentID: "d1", Content: "world", Embedding: []float32{0.2}}
	if err := s.StoreDocument(ctx, doc, []oasis.Chunk{chunk, chunk2}); err != nil {
		t.Fatal(err)
	}

	edges := []oasis.ChunkEdge{
		{ID: "e1", SourceID: "c1", TargetID: "c2", Relation: oasis.RelElaborates, Weight: 0.8, Description: "expands on greeting"},
		{ID: "e2", SourceID: "c2", TargetID: "c1", Relation: oasis.RelReferences, Weight: 0.7},
	}
	if err := s.StoreEdges(ctx, edges); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetEdges(ctx, []string{"c1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Description != "expands on greeting" {
		t.Errorf("Description = %q, want %q", got[0].Description, "expands on greeting")
	}

	// Edge without description should have empty string.
	got2, err := s.GetEdges(ctx, []string{"c2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 {
		t.Fatalf("len = %d, want 1", len(got2))
	}
	if got2[0].Description != "" {
		t.Errorf("Description = %q, want empty", got2[0].Description)
	}
}

func TestCosineSimilarity(t *testing.T) {
	// Identical vectors = 1.0
	s := cosineSimilarity([]float32{1, 2, 3}, []float32{1, 2, 3})
	if math.Abs(float64(s)-1.0) > 1e-6 {
		t.Errorf("identical vectors: expected ~1.0, got %f", s)
	}

	// Orthogonal vectors = 0.0
	s = cosineSimilarity([]float32{1, 0}, []float32{0, 1})
	if math.Abs(float64(s)) > 1e-6 {
		t.Errorf("orthogonal vectors: expected ~0.0, got %f", s)
	}

	// Opposite vectors = -1.0
	s = cosineSimilarity([]float32{1, 0}, []float32{-1, 0})
	if math.Abs(float64(s)+1.0) > 1e-6 {
		t.Errorf("opposite vectors: expected ~-1.0, got %f", s)
	}
}
