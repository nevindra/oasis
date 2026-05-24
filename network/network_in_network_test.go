package network

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

// TestNetwork_NestedNetwork verifies a Network can be used as a child of
// another Network. The outer router calls agent_research (the inner
// Network's tool name); the inner Network routes to one of its children
// and returns the result up to the outer Network's router.
func TestNetwork_NestedNetwork(t *testing.T) {
	// Inner network: two researchers
	r1 := &fixedAgent{name: "researcher-1", desc: "Researcher 1", out: "fact A"}
	r2 := &fixedAgent{name: "researcher-2", desc: "Researcher 2", out: "fact B"}

	innerArgs, _ := json.Marshal(map[string]string{"task": "find a fact"})
	innerRouter := &mockProvider{
		name: "inner-router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_researcher-1", Args: innerArgs}}},
			{Content: "fact A"},
		},
	}
	inner := New("research", "Research subnet", innerRouter, WithChildren(r1, r2))

	// Outer network: the inner network + a writer
	writer := &fixedAgent{name: "writer", desc: "Writes reports", out: "report based on facts"}

	outerArgs, _ := json.Marshal(map[string]string{"task": "do the research"})
	outerRouter := &mockProvider{
		name: "outer-router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_research", Args: outerArgs}}},
			{Content: "outer done"},
		},
	}
	outer := New("paper", "Paper writing team", outerRouter, WithChildren(inner, writer))

	res, err := outer.Execute(context.Background(), core.AgentTask{Input: "write a report"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output == "" {
		t.Fatal("expected non-empty Output from outer network")
	}

	// Topology should reflect the nesting:
	// outer Root = "paper"; outer Nodes = [inner Network "research", writer LLMAgent]
	top := outer.Topology()
	if top.Root != "paper" {
		t.Fatalf("outer Root: got %q want paper", top.Root)
	}
	if len(top.Nodes) != 2 {
		t.Fatalf("outer Nodes: want 2, got %d", len(top.Nodes))
	}
	foundResearch, foundWriter := false, false
	for _, n := range top.Nodes {
		switch n.Name {
		case "research":
			foundResearch = true
			if n.Kind != KindNetwork {
				t.Errorf("research node Kind: got %q want %q", n.Kind, KindNetwork)
			}
		case "writer":
			foundWriter = true
			if n.Kind != KindLLMAgent {
				t.Errorf("writer node Kind: got %q want %q", n.Kind, KindLLMAgent)
			}
		}
	}
	if !foundResearch {
		t.Error("expected 'research' node in outer topology")
	}
	if !foundWriter {
		t.Error("expected 'writer' node in outer topology")
	}

	// Inner network topology snapshots its own two children.
	innerTop := inner.Topology()
	if innerTop.Root != "research" {
		t.Fatalf("inner Root: got %q want research", innerTop.Root)
	}
	if len(innerTop.Nodes) != 2 {
		t.Fatalf("inner Nodes: want 2, got %d", len(innerTop.Nodes))
	}
}
