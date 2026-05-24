package workflow_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/network"
	"github.com/nevindra/oasis/workflow"
)

// compositionStubAgent is a minimal core.Agent implementation shared by both
// composition tests. Uses core.AgentTask / core.AgentResult so it satisfies
// core.Agent without relying on package-internal aliases.
type compositionStubAgent struct {
	name string
	desc string
	fn   func(core.AgentTask) (core.AgentResult, error)
}

func (s *compositionStubAgent) Name() string        { return s.name }
func (s *compositionStubAgent) Description() string { return s.desc }
func (s *compositionStubAgent) Execute(_ context.Context, task core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	rcfg := core.ApplyRunOptions(opts...)
	if rcfg.Stream != nil {
		close(rcfg.Stream)
	}
	return s.fn(task)
}

// compositionMockProvider is a minimal core.Provider for composition tests.
// It returns responses in order and cancels once the list is exhausted.
type compositionMockProvider struct {
	name      string
	responses []core.ChatResponse
	idx       int
}

func (m *compositionMockProvider) Name() string { return m.name }
func (m *compositionMockProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	if m.idx >= len(m.responses) {
		return core.ChatResponse{}, context.Canceled
	}
	resp := m.responses[m.idx]
	m.idx++
	return resp, nil
}

// TestWorkflowInNetwork verifies that a Network can dispatch to a Workflow as
// if it were any other core.Agent child (Workflow-in-Network).
func TestWorkflowInNetwork(t *testing.T) {
	// Build a 2-step workflow.
	step1Agent := &compositionStubAgent{
		name: "step1",
		desc: "first step",
		fn: func(task core.AgentTask) (core.AgentResult, error) {
			return core.AgentResult{Output: "step1: " + task.Input}, nil
		},
	}
	step2Agent := &compositionStubAgent{
		name: "step2",
		desc: "second step",
		fn: func(task core.AgentTask) (core.AgentResult, error) {
			return core.AgentResult{Output: "final"}, nil
		},
	}

	wf, err := workflow.New("research", "Two-step research",
		workflow.AgentStep("step1", step1Agent),
		workflow.AgentStep("step2", step2Agent, workflow.After("step1")),
	)
	if err != nil {
		t.Fatalf("workflow.New: %v", err)
	}

	// Compile-time check: *Workflow satisfies core.Agent.
	var _ core.Agent = wf

	// Router emits one tool call to agent_research, then a final content reply.
	routerArgs, _ := json.Marshal(map[string]string{"task": "investigate X"})
	router := &compositionMockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_research", Args: routerArgs}}},
			{Content: "all done"},
		},
	}

	net := network.New("paper", "Paper team", router, network.WithChildren(wf))

	res, err := net.Execute(context.Background(), agent.AgentTask{Input: "investigate X"})
	if err != nil {
		t.Fatalf("network.Execute: %v", err)
	}
	if res.Output == "" {
		t.Fatal("expected non-empty Output from network running a workflow child")
	}
}

// TestNetworkInWorkflow verifies that a Network can be used as the agent in an
// AgentStep of a Workflow (Network-in-Workflow).
func TestNetworkInWorkflow(t *testing.T) {
	// Build a small sub-network with two stub children.
	a := &compositionStubAgent{
		name: "fact-a",
		desc: "Fact A",
		fn: func(task core.AgentTask) (core.AgentResult, error) {
			return core.AgentResult{Output: "fact A"}, nil
		},
	}
	b := &compositionStubAgent{
		name: "fact-b",
		desc: "Fact B",
		fn: func(task core.AgentTask) (core.AgentResult, error) {
			return core.AgentResult{Output: "fact B"}, nil
		},
	}

	// Router calls one child then stops.
	routerArgs, _ := json.Marshal(map[string]string{"task": "go"})
	router := &compositionMockProvider{
		name: "subnet-router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_fact-a", Args: routerArgs}}},
			{Content: "subnet done"},
		},
	}
	subnet := network.New("research-subnet", "Mini research", router, network.WithChildren(a, b))

	// Compile-time check: *Network satisfies core.Agent.
	var _ core.Agent = subnet

	writeup := &compositionStubAgent{
		name: "writeup",
		desc: "writes the paper",
		fn: func(task core.AgentTask) (core.AgentResult, error) {
			return core.AgentResult{Output: "final report"}, nil
		},
	}

	wf, err := workflow.New("paper", "Paper workflow",
		workflow.AgentStep("research", subnet),
		workflow.AgentStep("writeup", writeup, workflow.After("research")),
	)
	if err != nil {
		t.Fatalf("workflow.New: %v", err)
	}

	res, err := wf.Execute(context.Background(), core.AgentTask{Input: "write a paper"})
	if err != nil {
		t.Fatalf("workflow.Execute: %v", err)
	}
	if res.Output == "" {
		t.Fatal("expected non-empty Output from workflow with a network child step")
	}
}
