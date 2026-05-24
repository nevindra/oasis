package runtime

import (
	"context"

	"github.com/nevindra/oasis/core"
)

// StepControl is the mutable surface PrepareStep operates on.
type StepControl struct {
	Request *core.ChatRequest
	Model   core.Provider
	Tools   []core.AnyTool
}

// PrepareStep runs before every LLM call inside the agent loop.
type PrepareStep func(ctx context.Context, iter int, ctrl *StepControl) error

// IterationSnapshot is a read-only view of one completed iteration.
type IterationSnapshot struct {
	Response    *core.ChatResponse
	ToolCalls   []core.ToolCall
	ToolResults []core.ToolResult
	Trace       core.StepTrace
}

// decisionAction controls what the agent loop does after an iteration.
type decisionAction uint8

const (
	decisionContinue decisionAction = iota
	decisionStop
	decisionInject
)

// IterationDecision is the return value of an OnIterationComplete hook.
type IterationDecision struct {
	action decisionAction
	msgs   []core.ChatMessage
	result core.AgentResult
}

// IsStop reports whether the decision is to stop the loop.
func (d IterationDecision) IsStop() bool { return d.action == decisionStop }

// IsInject reports whether the decision is to inject messages.
func (d IterationDecision) IsInject() bool { return d.action == decisionInject }

// Msgs returns the messages to inject.
func (d IterationDecision) Msgs() []core.ChatMessage { return d.msgs }

// Result returns the agent result for a Stop decision.
func (d IterationDecision) Result() core.AgentResult { return d.result }

// Continue advances the agent loop to the next iteration unchanged.
func Continue() IterationDecision { return IterationDecision{action: decisionContinue} }

// Stop ends the agent run and returns the given result.
func Stop(result core.AgentResult) IterationDecision {
	return IterationDecision{action: decisionStop, result: result}
}

// InjectFeedback appends a user message and continues.
func InjectFeedback(msg string) IterationDecision {
	return IterationDecision{
		action: decisionInject,
		msgs:   []core.ChatMessage{{Role: core.RoleUser, Content: msg}},
	}
}

// InjectMessages appends messages and continues.
func InjectMessages(msgs ...core.ChatMessage) IterationDecision {
	return IterationDecision{action: decisionInject, msgs: msgs}
}

// errAction controls what the agent loop does after an error.
type errAction uint8

const (
	errPropagate errAction = iota
	errRetry
	errHalt
)

// ErrorDecision is the return value of an OnError hook.
type ErrorDecision struct {
	action   errAction
	feedback string
	result   core.AgentResult
}

// IsPropagate reports whether the decision is to propagate the error.
func (d ErrorDecision) IsPropagate() bool { return d.action == errPropagate }

// IsRetry reports whether the decision is to retry.
func (d ErrorDecision) IsRetry() bool { return d.action == errRetry }

// IsHalt reports whether the decision is to halt.
func (d ErrorDecision) IsHalt() bool { return d.action == errHalt }

// Feedback returns the feedback message for retry decisions.
func (d ErrorDecision) Feedback() string { return d.feedback }

// Result returns the agent result for halt decisions.
func (d ErrorDecision) Result() core.AgentResult { return d.result }

// Propagate bubbles the original error up.
func Propagate() ErrorDecision { return ErrorDecision{action: errPropagate} }

// Retry re-runs the same iteration.
func Retry() ErrorDecision { return ErrorDecision{action: errRetry} }

// RetryWithFeedback appends a user message and re-runs.
func RetryWithFeedback(msg string) ErrorDecision {
	return ErrorDecision{action: errRetry, feedback: msg}
}

// HaltDecision ends the agent run gracefully.
func HaltDecision(result core.AgentResult) ErrorDecision {
	return ErrorDecision{action: errHalt, result: result}
}

// OnIterationComplete runs after each loop iteration.
type OnIterationComplete func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error)

// OnError runs when an error occurs in the loop.
type OnError func(ctx context.Context, iter int, err error) (ErrorDecision, error)
