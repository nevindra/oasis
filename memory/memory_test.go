// memory/memory_test.go
package memory

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestAgentMemory_InitClose(t *testing.T) {
	var m AgentMemory
	cfg := AgentMemoryConfig{}
	m.Init(cfg)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentMemory_CloseWaitsForGoroutines(t *testing.T) {
	var m AgentMemory
	m.Init(AgentMemoryConfig{})
	m.initSem()
	done := make(chan struct{})
	m.wg.Add(1)
	go func() { defer m.wg.Done(); <-done }()
	closed := make(chan struct{})
	go func() { _ = m.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned before goroutine finished")
	case <-context.Background().Done():
	default:
	}
	close(done)
	<-closed // Close should return now
}

func TestAgentMemory_PersistTurn_EndToEnd(t *testing.T) {
	store := newConformanceStore(t)
	emb := &fakeEmbedder{out: [][]float32{{1, 0, 0}}}
	provider := &fakeProvider{response: `[]`} // no facts
	var m AgentMemory
	m.Init(AgentMemoryConfig{
		Store: store, Embedding: emb, Provider: provider,
		Logger: discardLogger(),
	})
	task := core.AgentTask{ThreadID: "t1", ChatID: "c1", Input: "hi"}
	m.PersistTurn(context.Background(), "agent", task, "hi", "hello", nil)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if len(store.messages["t1"]) != 2 {
		t.Fatalf("messages = %d", len(store.messages["t1"]))
	}
}

func TestAgentMemory_Remember_DefaultScope(t *testing.T) {
	store := newConformanceStore(t)
	var m AgentMemory
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})
	err := m.Remember(context.Background(), core.MemoryItem{
		Kind: KindFact, Content: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := store.List(context.Background(), core.MemoryFilter{Kinds: []core.MemoryKind{KindFact}})
	if len(items) != 1 || items[0].Source.Kind == "" {
		t.Fatalf("source not defaulted: %+v", items)
	}
}

func TestAgentMemory_Forget_ByID(t *testing.T) {
	store := newConformanceStore(t)
	must(t, store.Upsert(context.Background(), core.MemoryItem{ID: "x", Kind: KindFact, Content: "y"}))
	var m AgentMemory
	m.Init(AgentMemoryConfig{Store: store, Logger: discardLogger()})
	n, err := m.Forget(context.Background(), ForgetByID("x"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("n = %d", n)
	}
}
