package network_test

import (
	"context"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/network"
)

// ExampleNewNetwork shows how to create a Network that routes tasks
// to multiple subagents via an LLM router provider.
func ExampleNewNetwork() {
	// Create specialized subagents
	var routerProvider core.Provider // typically: gemini.NewProvider(...) or similar
	searchAgent := agent.NewLLMAgent("search", "Searches the web", routerProvider)
	summarizeAgent := agent.NewLLMAgent("summarize", "Summarizes text", routerProvider)

	// Create the network — routerProvider drives routing decisions
	net := network.NewNetwork("coordinator", "Coordinates search and summarization",
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
