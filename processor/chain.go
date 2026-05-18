package processor

import (
	"context"
	"fmt"

	"github.com/nevindra/oasis/core"
)

// Chain holds an ordered list of processors and runs them at each hook point.
// Processors are pre-bucketed by interface at Add() time, eliminating per-call
// type assertions in the hot path.
type Chain struct {
	processors []any
	pre        []core.PreProcessor
	post       []core.PostProcessor
	postTool   []core.PostToolProcessor
}

// NewChain creates an empty chain.
func NewChain() *Chain {
	return &Chain{}
}

// Add appends a processor to the chain. The processor must implement at least
// one of core.PreProcessor, core.PostProcessor, or core.PostToolProcessor.
// Panics if p implements none of the three interfaces.
func (c *Chain) Add(p any) {
	pre, isPre := p.(core.PreProcessor)
	post, isPost := p.(core.PostProcessor)
	pt, isPostTool := p.(core.PostToolProcessor)
	if !isPre && !isPost && !isPostTool {
		panic(fmt.Sprintf("oasis/processor: processor %T implements none of core.PreProcessor, core.PostProcessor, core.PostToolProcessor", p))
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
func (c *Chain) RunPreLLM(ctx context.Context, req *core.ChatRequest) error {
	for _, p := range c.pre {
		if err := p.PreLLM(ctx, req); err != nil {
			return err
		}
	}
	return nil
}

// RunPostLLM runs all PostProcessor hooks in registration order.
// Stops and returns the first non-nil error.
func (c *Chain) RunPostLLM(ctx context.Context, resp *core.ChatResponse) error {
	for _, p := range c.post {
		if err := p.PostLLM(ctx, resp); err != nil {
			return err
		}
	}
	return nil
}

// RunPostTool runs all PostToolProcessor hooks in registration order.
// Stops and returns the first non-nil error.
func (c *Chain) RunPostTool(ctx context.Context, call core.ToolCall, result *core.ToolResult) error {
	for _, p := range c.postTool {
		if err := p.PostTool(ctx, call, result); err != nil {
			return err
		}
	}
	return nil
}

// Len returns the number of registered processors.
func (c *Chain) Len() int { return len(c.processors) }
