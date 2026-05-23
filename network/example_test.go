package network_test

import (
	"context"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/network"
)

// ExampleNew shows how to create a Network that routes tasks
// to multiple subagents via an LLM router provider.
func ExampleNew() {
	// Create specialized subagents
	var routerProvider core.Provider // typically: gemini.NewProvider(...) or similar
	searchAgent := agent.New("search", "Searches the web", routerProvider)
	summarizeAgent := agent.New("summarize", "Summarizes text", routerProvider)

	// Create the network — routerProvider drives routing decisions
	net := network.New("coordinator", "Coordinates search and summarization",
		routerProvider, agent.WithAgents(searchAgent, summarizeAgent))

	// Execute a task
	task := agent.AgentTask{
		Input: "Find and summarize recent AI news",
	}
	result, err := net.Execute(context.Background(), task)
	if err != nil {
		panic(err)
	}
	_ = result // Use the result
}
