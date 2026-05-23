package oasis_test

import (
	"context"
	"testing"

	"github.com/nevindra/oasis"
	"github.com/nevindra/oasis/core"
)

func TestReExports_HookTypes(t *testing.T) {
	var _ oasis.RunOptions
	var _ oasis.PrepareStep
	var _ oasis.OnIterationComplete
	var _ oasis.OnError
	var _ oasis.StepControl
	var _ oasis.IterationSnapshot
	var _ oasis.IterationDecision
	var _ oasis.ErrorDecision
	var _ *oasis.RunOptionsError
}

func TestReExports_DecisionConstructors(t *testing.T) {
	_ = oasis.Continue()
	_ = oasis.Stop(oasis.AgentResult{})
	_ = oasis.InjectFeedback("x")
	_ = oasis.Propagate()
	_ = oasis.Retry()
	_ = oasis.RetryWithFeedback("x")
	_ = oasis.HaltDecision(oasis.AgentResult{})
}

func TestReExports_AgentOptions(t *testing.T) {
	var _ oasis.AgentOption = oasis.WithPrepareStep(nil)
	var _ oasis.AgentOption = oasis.WithOnIterationComplete(nil)
	var _ oasis.AgentOption = oasis.WithOnError(nil)
	var _ oasis.AgentOption = oasis.WithMetadata(nil)
}

// nopAgent satisfies oasis.Agent with a fixed result.
type nopAgent struct{}

func (n *nopAgent) Name() string        { return "nop" }
func (n *nopAgent) Description() string { return "" }
func (n *nopAgent) Execute(_ context.Context, _ oasis.AgentTask, opts ...core.RunOption) (oasis.AgentResult, error) {
	cfg := core.ApplyRunOptions(opts...)
	if cfg.Stream != nil {
		close(cfg.Stream)
	}
	return oasis.AgentResult{Output: "hi"}, nil
}

func TestOasis_Subscribe(t *testing.T) {
	s := oasis.Subscribe(context.Background(), &nopAgent{}, oasis.AgentTask{})
	if got := s.Text(); got != "hi" {
		t.Errorf("Text() = %q, want %q", got, "hi")
	}
}

func TestOasis_WithToolApprovalCompiles(t *testing.T) {
	// The point of this test is purely to assert the curated re-exports
	// compose without import or type errors. No runtime behavior is checked
	// — that's covered by agent/tool_approval_test.go.
	_ = oasis.WithToolApproval("x")
	_ = oasis.WithToolApproval("x", oasis.OnDeny(oasis.DenyHalt))
	_ = oasis.WithToolApproval("x", oasis.ApprovalPrompt(func(c oasis.ToolCall) string { return "?" }))
}
