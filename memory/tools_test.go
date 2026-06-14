// memory/tools_test.go
package memory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestRememberTool_WritesItem(t *testing.T) {
	store := newConformanceStore(t)
	var m AgentMemory
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})
	tool := m.RememberTool()
	if tool.Name() == "" {
		t.Fatal("tool name empty")
	}

	args, _ := json.Marshal(map[string]any{"content": "User likes Tailwind", "kind": "fact"})
	res, err := tool.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("ExecuteRaw error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	// At least one item should now be in the store with that content.
	items, _ := store.List(context.Background(), core.MemoryFilter{Kinds: []core.MemoryKind{KindFact}})
	if len(items) != 1 || items[0].Content != "User likes Tailwind" {
		t.Fatalf("expected 1 item, got %+v", items)
	}
}

func TestRecallTool_ReturnsItems(t *testing.T) {
	store := newConformanceStore(t)
	must(t, store.Upsert(context.Background(), core.MemoryItem{
		ID: "f1", Kind: KindFact, Content: "User likes dark mode",
		Scope: Scoped(ScopeResource, "c1"), Embedding: []float32{1, 0, 0},
	}))
	emb := &fakeEmbedder{out: [][]float32{{1, 0, 0}}}
	var m AgentMemory
	m.Init(AgentMemoryConfig{Store: store, Embedding: emb, Logger: discardLogger()})
	tool := m.RecallTool()
	args, _ := json.Marshal(map[string]any{"query": "what color"})
	res, err := tool.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("ExecuteRaw error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if len(res.Content) == 0 {
		t.Fatal("empty content")
	}
}
