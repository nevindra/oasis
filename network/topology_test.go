package network

import (
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestTopology_BasicGraph(t *testing.T) {
	a := &fixedAgent{name: "search", desc: "Searches"}
	b := &fixedAgent{name: "summary", desc: "Summarizes"}
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("research", "Research team", router, WithChildren(a, b))
	top := net.Topology()
	if top.Root != "research" {
		t.Fatalf("Root: got %q want research", top.Root)
	}
	if len(top.Nodes) != 2 {
		t.Fatalf("Nodes: want 2, got %d", len(top.Nodes))
	}
	if top.Nodes[0].Name != "search" || top.Nodes[1].Name != "summary" {
		t.Fatalf("Nodes order: %v", top.Nodes)
	}
	if top.Nodes[0].Kind != KindLLMAgent {
		t.Fatalf("Nodes[0].Kind: got %q want %q", top.Nodes[0].Kind, KindLLMAgent)
	}
	if len(top.Edges) != 2 {
		t.Fatalf("Edges: want 2, got %d", len(top.Edges))
	}
	if top.Edges[0].From != "research" || top.Edges[0].To != "search" {
		t.Fatalf("Edges[0]: got %+v want {research, search}", top.Edges[0])
	}
}

func TestTopology_RecordsSupervisor(t *testing.T) {
	a := &fixedAgent{name: "x", desc: "x"}
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("team", "team", router,
		WithChildren(a),
		WithSupervisor(RestartOnFail(3)),
	)
	top := net.Topology()
	if len(top.Nodes) != 1 {
		t.Fatalf("Nodes: %d", len(top.Nodes))
	}
	if len(top.Nodes[0].Supervisors) != 1 {
		t.Fatalf("Supervisors: want 1, got %d", len(top.Nodes[0].Supervisors))
	}
	sup := top.Nodes[0].Supervisors[0]
	if sup.Kind != "restart" {
		t.Fatalf("Supervisor.Kind: got %q want restart", sup.Kind)
	}
	if sup.Params["max"] != "3" {
		t.Fatalf("Supervisor.Params[max]: got %q want 3", sup.Params["max"])
	}
}

func TestTopology_ClassifiesNetworkKind(t *testing.T) {
	inner := New("inner", "Inner net",
		&mockProvider{name: "inner-router", responses: []core.ChatResponse{{Content: "ok"}}},
	)
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	outer := New("outer", "Outer net", router, WithChildren(inner))
	top := outer.Topology()
	if len(top.Nodes) != 1 {
		t.Fatalf("Nodes: %d", len(top.Nodes))
	}
	if top.Nodes[0].Kind != KindNetwork {
		t.Fatalf("Nodes[0].Kind: got %q want %q", top.Nodes[0].Kind, KindNetwork)
	}
}

func TestTopology_PerChildSupervisorComposes(t *testing.T) {
	a := &fixedAgent{name: "x", desc: "x"}
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("team", "team", router,
		WithChildren(a),
		WithSupervisor(RestartOnFail(2)),
		WithSupervisorFor("x", CircuitBreaker(5, 0)),
	)
	top := net.Topology()
	sups := top.Nodes[0].Supervisors
	if len(sups) != 2 {
		t.Fatalf("Supervisors: want 2 (network-wide + per-child), got %d", len(sups))
	}
	if sups[0].Kind != "restart" || sups[1].Kind != "circuit-breaker" {
		t.Fatalf("Supervisor kinds: %v", sups)
	}
}
