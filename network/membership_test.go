package network

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestAddAgent_Succeeds(t *testing.T) {
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("team", "team", router)
	a := &fixedAgent{name: "newcomer", out: "hi"}
	if err := net.AddAgent(a); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, ok := net.agents["newcomer"]; !ok {
		t.Fatal("agent not registered")
	}
}

func TestAddAgent_DuplicateNameFails(t *testing.T) {
	a := &fixedAgent{name: "x"}
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("team", "team", router, a)
	dup := &fixedAgent{name: "x"}
	if err := net.AddAgent(dup); err == nil {
		t.Fatal("expected error on duplicate name")
	}
}

func TestRemoveAgent_Succeeds(t *testing.T) {
	a := &fixedAgent{name: "x"}
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("team", "team", router, a)
	if err := net.RemoveAgent("x"); err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	if _, ok := net.agents["x"]; ok {
		t.Fatal("agent not removed")
	}
}

func TestRemoveAgent_UnknownNameFails(t *testing.T) {
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("team", "team", router)
	if err := net.RemoveAgent("ghost"); err == nil {
		t.Fatal("expected error on unknown name")
	}
}

func TestAddAgent_AppliesSupervisor(t *testing.T) {
	router := &mockProvider{name: "router", responses: []core.ChatResponse{
		{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_flakey", Args: mustJSON(map[string]string{"task": "go"})}}},
		{Content: "done"},
	}}
	net := NewWithOptions("team", "team", router, nil,
		WithSupervisor(RestartOnFail(2)),
	)
	f := &flakeyAgent{failFor: 1}
	if err := net.AddAgent(f); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if _, err := net.Execute(context.Background(), core.AgentTask{Input: "go"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.calls.Load() < 2 {
		t.Fatalf("expected retry from supervisor; calls=%d", f.calls.Load())
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
