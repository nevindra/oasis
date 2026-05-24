package network

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestWithDynamicSpawning_RegistersChild(t *testing.T) {
	built := false
	policy := SpawnPolicy{
		MaxChildren: 5,
		ChildBuilder: func(req SpawnRequest) (core.Agent, error) {
			built = true
			if req.Name != "researcher" {
				return nil, fmt.Errorf("unexpected name: %q", req.Name)
			}
			return &fixedAgent{name: req.Name, desc: req.Description, out: "spawned"}, nil
		},
	}
	spawnArgs, _ := json.Marshal(SpawnRequest{Name: "researcher", Description: "Researches", Prompt: "You research."})
	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "spawn_agent", Args: spawnArgs}}},
			{Content: "done"},
		},
	}
	net := New("team", "team", router, WithDynamicSpawning(policy))
	if _, err := net.Execute(context.Background(), core.AgentTask{Input: "go"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !built {
		t.Fatal("ChildBuilder should have been called")
	}
	if _, ok := net.agents["researcher"]; !ok {
		t.Fatal("spawned agent should be registered in network")
	}
}

func TestWithDynamicSpawning_MaxChildrenEnforced(t *testing.T) {
	policy := SpawnPolicy{
		MaxChildren: 1,
		ChildBuilder: func(req SpawnRequest) (core.Agent, error) {
			return &fixedAgent{name: req.Name, desc: req.Description}, nil
		},
	}
	args1, _ := json.Marshal(SpawnRequest{Name: "a", Description: "first", Prompt: "p"})
	args2, _ := json.Marshal(SpawnRequest{Name: "b", Description: "second", Prompt: "p"})
	router := &syncMockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{
				{ID: "1", Name: "spawn_agent", Args: args1},
				{ID: "2", Name: "spawn_agent", Args: args2},
			}},
			{Content: "done"},
		},
	}
	net := New("team", "team", router, WithDynamicSpawning(policy))
	_, err := net.Execute(context.Background(), core.AgentTask{Input: "go"})
	if err != nil {
		// Acceptable if Execute returns the limit error directly.
		t.Logf("Execute returned error (acceptable): %v", err)
	}
	n := 0
	for name := range net.agents {
		if name == "a" || name == "b" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 spawned agent (MaxChildren=1), got %d", n)
	}
}

func TestWithDynamicSpawning_PanicsWithoutChildBuilder(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when ChildBuilder is nil")
		}
	}()
	_ = WithDynamicSpawning(SpawnPolicy{})
}

func TestWithDynamicSpawning_ToolDefInjected(t *testing.T) {
	policy := SpawnPolicy{
		ChildBuilder: func(req SpawnRequest) (core.Agent, error) { return nil, fmt.Errorf("noop") },
	}
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("team", "team", router, WithDynamicSpawning(policy))
	defs := net.buildToolDefs(nil)
	found := false
	for _, d := range defs {
		if d.Name == "spawn_agent" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("spawn_agent tool def should be injected when WithDynamicSpawning is set")
	}
}

func TestWithoutDynamicSpawning_NoToolDef(t *testing.T) {
	router := &mockProvider{name: "router", responses: []core.ChatResponse{{Content: "ok"}}}
	net := New("team", "team", router)
	defs := net.buildToolDefs(nil)
	for _, d := range defs {
		if d.Name == "spawn_agent" {
			t.Fatal("spawn_agent tool def should NOT be injected without WithDynamicSpawning")
		}
	}
}
