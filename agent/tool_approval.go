package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nevindra/oasis/core"
)

// DenyAction controls behavior when a human denies a tool approval request.
type DenyAction int

const (
	// DenyAskLLMToRevise returns a ToolResult with Error set, allowing the LLM
	// to adapt and try a different approach. This is the default.
	DenyAskLLMToRevise DenyAction = iota
	// DenyHalt halts the agent loop with *core.ErrHalt. Use when denying must
	// terminate the run cleanly (e.g. compliance-mandated stops).
	DenyHalt
)

// ApprovalOption is a functional option for WithToolApproval.
type ApprovalOption func(*approvalConfig)

type approvalConfig struct {
	toolName string
	prompt   func(call core.ToolCall) string
	onDeny   DenyAction
}

// ApprovalPrompt sets a custom prompt builder. The function receives the
// pending ToolCall (including args) and returns the question shown to the
// human via the InputHandler.
func ApprovalPrompt(fn func(call core.ToolCall) string) ApprovalOption {
	return func(c *approvalConfig) { c.prompt = fn }
}

// OnDeny sets the action taken when the human denies approval.
// Default is DenyAskLLMToRevise.
func OnDeny(action DenyAction) ApprovalOption {
	return func(c *approvalConfig) { c.onDeny = action }
}

// WithToolApproval requires explicit human approval before the named tool
// runs. The approval flow uses the agent's InputHandler — agents using
// WithToolApproval must also configure WithInputHandler. Apply this option
// once per tool name you want to gate.
//
// The middleware sits outermost in the chain so retries do not re-prompt
// the human.
//
// The InputHandler receives an InputRequest with Question (the prompt),
// Options=["approve","deny"], and Metadata carrying the tool name and args.
// Approve runs the tool with the original args; deny behavior depends on
// OnDeny (default: return ToolResult.Error so the LLM can adapt).
func WithToolApproval(toolName string, opts ...ApprovalOption) AgentOption {
	cfg := &approvalConfig{
		toolName: toolName,
		prompt: func(call core.ToolCall) string {
			return fmt.Sprintf("Approve call to %s?", call.Name)
		},
		onDeny: DenyAskLLMToRevise,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(c *Config) {
		c.toolApprovals = append(c.toolApprovals, *cfg)
	}
}

// approvalMiddleware returns a middleware that gates the named tool with an
// approval request. Tools whose name does not match cfg.toolName pass
// through unchanged.
func approvalMiddleware(cfg approvalConfig, ih InputHandler) core.ToolMiddleware {
	return func(inner core.AnyTool) core.AnyTool {
		if inner.Name() != cfg.toolName {
			return inner
		}
		return &approvalWrapper{inner: inner, cfg: cfg, handler: ih}
	}
}

type approvalWrapper struct {
	inner   core.AnyTool
	cfg     approvalConfig
	handler InputHandler
}

func (a *approvalWrapper) Name() string                     { return a.inner.Name() }
func (a *approvalWrapper) Definition() core.ToolDefinition  { return a.inner.Definition() }
func (a *approvalWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	if a.handler == nil {
		return core.ToolResult{}, errors.New("approval required but no InputHandler configured")
	}

	// Emit pending event on the stream if a sink is configured.
	if ch := streamSinkFromContext(ctx); ch != nil {
		ev := core.StreamEvent{
			Type: core.EventToolApprovalPending,
			Name: a.inner.Name(),
			Args: args,
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return core.ToolResult{}, ctx.Err()
		}
	}

	call := core.ToolCall{Name: a.inner.Name(), Args: args}
	question := a.cfg.prompt(call)

	resp, err := a.handler.RequestInput(ctx, InputRequest{
		Question: question,
		Options:  []string{"approve", "deny"},
		Metadata: map[string]string{
			"kind": "tool-approval",
			"tool": a.inner.Name(),
			"args": string(args),
		},
	})
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("approval request: %w", err)
	}

	switch resp.Value {
	case "approve":
		return a.inner.ExecuteRaw(ctx, args)
	case "deny":
		if a.cfg.onDeny == DenyHalt {
			return core.ToolResult{}, &core.ErrHalt{Response: fmt.Sprintf("user denied call to %s", a.inner.Name())}
		}
		return core.ToolResult{Error: fmt.Sprintf("user denied call to %s", a.inner.Name())}, nil
	default:
		// Unknown values fall through as deny + ask-LLM-to-revise. Forward-compatible.
		return core.ToolResult{Error: fmt.Sprintf("approval response %q not recognized; treating as deny", resp.Value)}, nil
	}
}
