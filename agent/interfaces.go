package agent

import (
	"context"

	"github.com/nevindra/oasis/core"
)

// AgentWithOptions is the extended Agent interface supporting per-call
// RunOptions overrides via ExecuteWith. Implemented by LLMAgent, Network,
// and Workflow.
//
// Type-assert to use:
//
//	if a, ok := ag.(agent.AgentWithOptions); ok {
//	    result, err := a.ExecuteWith(ctx, task, opts)
//	}
type AgentWithOptions interface {
	core.Agent
	// ExecuteWith runs the agent like Execute, but with per-call overrides
	// supplied via opts. A nil opts is equivalent to Execute.
	ExecuteWith(ctx context.Context, task core.AgentTask, opts *RunOptions) (core.AgentResult, error)
}

// StreamingAgentWithOptions is the extended StreamingAgent interface
// supporting per-call RunOptions overrides via ExecuteStreamWith.
// Implemented by LLMAgent, Network, and Workflow.
type StreamingAgentWithOptions interface {
	core.StreamingAgent
	// ExecuteWith runs the agent like Execute, but with per-call overrides.
	// A nil opts is equivalent to Execute.
	ExecuteWith(ctx context.Context, task core.AgentTask, opts *RunOptions) (core.AgentResult, error)
	// ExecuteStreamWith runs the agent like ExecuteStream, but with per-call
	// overrides. A nil opts is equivalent to ExecuteStream.
	ExecuteStreamWith(ctx context.Context, task core.AgentTask, ch chan<- core.StreamEvent, opts *RunOptions) (core.AgentResult, error)
}
