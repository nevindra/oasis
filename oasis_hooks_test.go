package oasis_test

import (
	"testing"

	"github.com/nevindra/oasis"
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

func TestReExports_Interfaces(t *testing.T) {
	var _ oasis.AgentWithOptions
	var _ oasis.StreamingAgentWithOptions
}
