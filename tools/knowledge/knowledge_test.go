package knowledge

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis"
)

type mockRetriever struct {
	results []oasis.RetrievalResult
	query   string
}

func (m *mockRetriever) Retrieve(_ context.Context, query string, _ int) ([]oasis.RetrievalResult, error) {
	m.query = query
	return m.results, nil
}

type mockEmb struct{}

func (m *mockEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1}
	}
	return out, nil
}
func (m *mockEmb) Dimensions() int { return 1 }
func (m *mockEmb) Name() string    { return "mock" }

// nopStore satisfies oasis.Store with no-ops for testing.
type nopStore struct{}

func (nopStore) CreateThread(_ context.Context, _ oasis.Thread) error              { return nil }
func (nopStore) GetThread(_ context.Context, _ string) (oasis.Thread, error)       { return oasis.Thread{}, nil }
func (nopStore) ListThreads(_ context.Context, _ string, _ int) ([]oasis.Thread, error) {
	return nil, nil
}
func (nopStore) UpdateThread(_ context.Context, _ oasis.Thread) error { return nil }
func (nopStore) DeleteThread(_ context.Context, _ string) error       { return nil }
func (nopStore) StoreMessage(_ context.Context, _ oasis.Message) error {
	return nil
}
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
func (nopStore) GetConfig(_ context.Context, _ string) (string, error) { return "", nil }
func (nopStore) SetConfig(_ context.Context, _, _ string) error        { return nil }
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
func (nopStore) DeleteScheduledAction(_ context.Context, _ string) error { return nil }
func (nopStore) DeleteAllScheduledActions(_ context.Context) (int, error) {
	return 0, nil
}
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

func TestKnowledgeTool_DelegatesToRetriever(t *testing.T) {
	ret := &mockRetriever{
		results: []oasis.RetrievalResult{
			{Content: "found something", Score: 0.9, ChunkID: "c1"},
		},
	}
	store := &nopStore{}
	emb := &mockEmb{}

	tool := New(store, emb, WithRetriever(ret))
	args, _ := json.Marshal(map[string]string{"query": "test query"})
	result, err := tool.Execute(context.Background(), "knowledge_search", args)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if ret.query != "test query" {
		t.Errorf("retriever.query = %q, want %q", ret.query, "test query")
	}
	if !strings.Contains(result.Content, "found something") {
		t.Errorf("result missing retriever content: %s", result.Content)
	}
}

func TestKnowledgeTool_BackwardCompatible(t *testing.T) {
	store := &nopStore{}
	emb := &mockEmb{}
	tool := New(store, emb)
	if tool.retriever == nil {
		t.Error("retriever should be auto-created when not provided")
	}
}

func TestKnowledgeTool_WithTopK(t *testing.T) {
	store := &nopStore{}
	emb := &mockEmb{}
	tool := New(store, emb, WithTopK(10))
	if tool.topK != 10 {
		t.Errorf("topK = %d, want 10", tool.topK)
	}
}

func TestKnowledgeTool_GraphContextInOutput(t *testing.T) {
	ret := &mockRetriever{
		results: []oasis.RetrievalResult{
			{
				Content: "OAuth setup flow",
				Score:   0.9,
				ChunkID: "c1",
				GraphContext: []oasis.EdgeContext{
					{FromChunkID: "c2", Relation: oasis.RelDependsOn, Description: "requires auth configuration"},
					{FromChunkID: "c3", Relation: oasis.RelElaborates, Description: "expands on setup steps"},
				},
			},
			{
				Content: "Token refresh mechanism",
				Score:   0.7,
				ChunkID: "c4",
				// No GraphContext — seed chunk, should not show edge lines.
			},
		},
	}
	store := &nopStore{}
	emb := &mockEmb{}

	tool := New(store, emb, WithRetriever(ret))
	args, _ := json.Marshal(map[string]string{"query": "OAuth"})
	result, err := tool.Execute(context.Background(), "knowledge_search", args)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Chunk content should be present.
	if !strings.Contains(result.Content, "OAuth setup flow") {
		t.Error("missing chunk content")
	}
	if !strings.Contains(result.Content, "Token refresh mechanism") {
		t.Error("missing second chunk content")
	}

	// GraphContext edges should be formatted.
	if !strings.Contains(result.Content, "requires auth configuration") {
		t.Errorf("missing edge description: %s", result.Content)
	}
	if !strings.Contains(result.Content, "depends_on") {
		t.Errorf("missing relation type in output: %s", result.Content)
	}
	if !strings.Contains(result.Content, "expands on setup steps") {
		t.Errorf("missing second edge description: %s", result.Content)
	}

	// Second chunk should NOT have edge lines.
	lines := strings.Split(result.Content, "\n")
	inSecondChunk := false
	for _, line := range lines {
		if strings.Contains(line, "Token refresh mechanism") {
			inSecondChunk = true
		}
		if inSecondChunk && strings.Contains(line, "↳") {
			t.Errorf("second chunk should have no edge context lines, found: %s", line)
		}
	}
}
