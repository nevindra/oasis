// Package network composes multiple agents into a peer network with
// configurable routing. A Network satisfies core.Agent so it can be used
// anywhere an Agent is expected.
//
// A Network delegates tasks to subagents via an LLM router. The router
// sees each subagent as a callable tool ("agent_<name>") and decides
// which agents to invoke, in what order, and with what data. This enables
// flexible, LLM-driven composition of complex multi-agent workflows.
//
// Create a Network with NewNetwork and pass subagents via WithAgents:
//
//	searchAgent := oasis.NewLLMAgent("search", "...", provider, ...)
//	summarizeAgent := oasis.NewLLMAgent("summarize", "...", provider, ...)
//
//	router := oasis.NewLLMAgent("router", "...", provider, ...)
//	network := network.NewNetwork("coordinator", "...", router,
//		oasis.WithAgents(searchAgent, summarizeAgent),
//	)
//
//	result, err := network.Execute(ctx, oasis.AgentTask{Input: "research this topic"})
package network
