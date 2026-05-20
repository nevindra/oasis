package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

func TestToolResultStoreDefaultAutoWired(t *testing.T) {
	// An agent constructed without WithToolResultStore should have a non-nil store.
	a := agent.NewLLMAgent("auto", "", &nopProvider{})
	store := agent.AgentToolResultStore(a)
	if store == nil {
		t.Fatal("expected non-nil default ToolResultStore; got nil")
	}
	// Verify the store is functional (Put/Get round-trip).
	id, err := store.Put(context.Background(), core.TextContent("test"))
	if err != nil {
		t.Fatalf("default store Put failed: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty id from default store")
	}
	// read_full_result should be in the tool list.
	defs := a.Tools().AllDefinitions()
	found := false
	for _, d := range defs {
		if d.Name == "read_full_result" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected read_full_result to be registered when default store is auto-wired")
	}
}

func TestToolResultStoreExplicitNilDisables(t *testing.T) {
	// Passing nil explicitly should disable the store and skip read_full_result.
	a := agent.NewLLMAgent("disabled", "", &nopProvider{},
		agent.WithToolResultStore(nil),
	)
	store := agent.AgentToolResultStore(a)
	if store != nil {
		t.Fatalf("expected nil store when WithToolResultStore(nil) is passed; got %T", store)
	}
	// read_full_result must not be registered.
	defs := a.Tools().AllDefinitions()
	for _, d := range defs {
		if d.Name == "read_full_result" {
			t.Error("read_full_result must not be registered when store is explicitly disabled")
			break
		}
	}
}

func TestToolResultStoreCustomImplementationUsed(t *testing.T) {
	custom := &trackingStore{}
	a := agent.NewLLMAgent("custom", "", &nopProvider{},
		agent.WithToolResultStore(custom),
	)
	store := agent.AgentToolResultStore(a)
	if store != custom {
		t.Fatalf("expected custom store to be used; got %T", store)
	}
}

// --- helpers ---

// nopProvider satisfies core.Provider with no-op implementations.
type nopProvider struct{}

func (nopProvider) Name() string { return "nop" }
func (nopProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	return core.ChatResponse{Content: "done"}, nil
}

// trackingStore satisfies core.ToolResultStore for testing.
type trackingStore struct {
	data map[string]json.RawMessage
}

func (s *trackingStore) Put(_ context.Context, content json.RawMessage) (string, error) {
	if s.data == nil {
		s.data = map[string]json.RawMessage{}
	}
	id := "test-id-" + strings.Repeat("x", len(s.data))
	s.data[id] = content
	return id, nil
}

func (s *trackingStore) Get(_ context.Context, id string, offset, length int) (json.RawMessage, int, error) {
	c, ok := s.data[id]
	if !ok {
		return nil, 0, core.ErrToolResultNotFound
	}
	total := len(c)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + length
	if end > total {
		end = total
	}
	return json.RawMessage(c[offset:end]), total, nil
}
