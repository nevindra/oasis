package oasis

import (
	"context"
	"fmt"
)

// PreProcessor runs before messages are sent to the LLM.
// Implementations can modify the request (add/remove/transform messages)
// or return an error to halt execution.
// Return ErrHalt to short-circuit with a canned response.
// Must be safe for concurrent use.
type PreProcessor interface {
	PreLLM(ctx context.Context, req *ChatRequest) error
}

// PostProcessor runs after the LLM responds, before tool execution.
// Implementations can modify the response (transform content, filter tool calls)
// or return an error to halt execution.
// Return ErrHalt to short-circuit with a canned response.
// Must be safe for concurrent use.
type PostProcessor interface {
	PostLLM(ctx context.Context, resp *ChatResponse) error
}

// PostToolProcessor runs after each tool execution, before the result
// is appended to the message history.
// Implementations can modify the result (redact content, transform output)
// or return an error to halt execution.
// Return ErrHalt to short-circuit with a canned response.
// Must be safe for concurrent use.
type PostToolProcessor interface {
	PostTool(ctx context.Context, call ToolCall, result *ToolResult) error
}

// ErrHalt signals that a processor wants to stop agent execution
// and return a specific response to the caller. The agent loop catches
// ErrHalt and returns AgentResult{Output: Response} with a nil error.
type ErrHalt struct {
	Response string
}

func (e *ErrHalt) Error() string { return "processor halted: " + e.Response }

// ProcessorChain holds an ordered list of processors and runs them
// at each hook point. Processors are pre-bucketed by interface at Add()
// time, eliminating per-call type assertions in the hot path.
type ProcessorChain struct {
	processors []any
	pre        []PreProcessor
	post       []PostProcessor
	postTool   []PostToolProcessor
}

// NewProcessorChain creates an empty chain.
func NewProcessorChain() *ProcessorChain {
	return &ProcessorChain{}
}

// Add appends a processor to the chain. The processor must implement at least
// one of PreProcessor, PostProcessor, or PostToolProcessor.
// Panics if p implements none of the three interfaces.
func (c *ProcessorChain) Add(p any) {
	pre, isPre := p.(PreProcessor)
	post, isPost := p.(PostProcessor)
	pt, isPostTool := p.(PostToolProcessor)
	if !isPre && !isPost && !isPostTool {
		panic(fmt.Sprintf("oasis: processor %T implements none of PreProcessor, PostProcessor, PostToolProcessor", p))
	}
	c.processors = append(c.processors, p)
	if isPre {
		c.pre = append(c.pre, pre)
	}
	if isPost {
		c.post = append(c.post, post)
	}
	if isPostTool {
		c.postTool = append(c.postTool, pt)
	}
}

// RunPreLLM runs all PreProcessor hooks in registration order.
// Stops and returns the first non-nil error.
func (c *ProcessorChain) RunPreLLM(ctx context.Context, req *ChatRequest) error {
	for _, p := range c.pre {
		if err := p.PreLLM(ctx, req); err != nil {
			return err
		}
	}
	return nil
}

// RunPostLLM runs all PostProcessor hooks in registration order.
// Stops and returns the first non-nil error.
func (c *ProcessorChain) RunPostLLM(ctx context.Context, resp *ChatResponse) error {
	for _, p := range c.post {
		if err := p.PostLLM(ctx, resp); err != nil {
			return err
		}
	}
	return nil
}

// RunPostTool runs all PostToolProcessor hooks in registration order.
// Stops and returns the first non-nil error.
func (c *ProcessorChain) RunPostTool(ctx context.Context, call ToolCall, result *ToolResult) error {
	for _, p := range c.postTool {
		if err := p.PostTool(ctx, call, result); err != nil {
			return err
		}
	}
	return nil
}

// Len returns the number of registered processors.
func (c *ProcessorChain) Len() int { return len(c.processors) }
