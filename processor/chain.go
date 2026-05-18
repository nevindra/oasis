package processor

import (
	"context"

	"github.com/nevindra/oasis/core"
)

// Chain holds an ordered list of processors and runs them at each hook point.
// Processors are pre-bucketed by interface at registration time, eliminating
// per-call type assertions in the hot path.
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

// AddPre registers a PreProcessor. The processor runs before each LLM call.
func (c *Chain) AddPre(p core.PreProcessor) {
	c.processors = append(c.processors, p)
	c.pre = append(c.pre, p)
}

// AddPost registers a PostProcessor. The processor runs after each LLM response.
func (c *Chain) AddPost(p core.PostProcessor) {
	c.processors = append(c.processors, p)
	c.post = append(c.post, p)
}

// AddPostTool registers a PostToolProcessor. The processor runs after each tool result.
func (c *Chain) AddPostTool(p core.PostToolProcessor) {
	c.processors = append(c.processors, p)
	c.postTool = append(c.postTool, p)
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

// Len returns the count of registrations across all stages. A processor
// registered to multiple stages counts once per registration.
func (c *Chain) Len() int { return len(c.processors) }
