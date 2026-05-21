package agent

import (
	"context"

	"github.com/nevindra/oasis/core"
)

// IterationDecision is the return value of an OnIterationComplete hook.
// Construct via Continue, Stop, InjectFeedback, or InjectMessages.
// The zero value is equivalent to Continue().
type IterationDecision struct {
	action decisionAction
	msgs   []core.ChatMessage
	result core.AgentResult
}

type decisionAction uint8

const (
	decisionContinue decisionAction = iota
	decisionStop
	decisionInject
)

// Continue advances the agent loop to the next iteration unchanged.
// Equivalent to the zero IterationDecision.
func Continue() IterationDecision {
	return IterationDecision{action: decisionContinue}
}

// Stop ends the agent run immediately and returns the given result from Execute.
func Stop(result core.AgentResult) IterationDecision {
	return IterationDecision{action: decisionStop, result: result}
}

// InjectFeedback appends a single user-role ChatMessage to the loop history
// and continues to the next iteration. The injected message counts as
// regular history for subsequent iterations.
func InjectFeedback(msg string) IterationDecision {
	return IterationDecision{
		action: decisionInject,
		msgs: []core.ChatMessage{{
			Role:    core.RoleUser,
			Content: msg,
		}},
	}
}

// InjectMessages appends one or more raw messages (any role) to the loop
// history and continues to the next iteration.
func InjectMessages(msgs ...core.ChatMessage) IterationDecision {
	return IterationDecision{action: decisionInject, msgs: msgs}
}

// ErrorDecision is the return value of an OnError hook.
// Construct via Propagate, Retry, RetryWithFeedback, or HaltDecision.
// The zero value is equivalent to Propagate().
type ErrorDecision struct {
	action   errAction
	feedback string
	result   core.AgentResult
}

type errAction uint8

const (
	errPropagate errAction = iota
	errRetry
	errHalt
)

// Propagate bubbles the original error up to Execute.
// Equivalent to the zero ErrorDecision.
func Propagate() ErrorDecision {
	return ErrorDecision{action: errPropagate}
}

// Retry re-runs the same iteration as-is. The retry counts against MaxIter.
func Retry() ErrorDecision {
	return ErrorDecision{action: errRetry}
}

// RetryWithFeedback appends a user-role message to history and re-runs the
// iteration. The injected message persists in history. The retry counts
// against MaxIter.
func RetryWithFeedback(msg string) ErrorDecision {
	return ErrorDecision{action: errRetry, feedback: msg}
}

// HaltDecision ends the agent run gracefully (no error) and returns the
// given result from Execute.
//
// Named HaltDecision rather than Halt to avoid collision with the
// processor *ErrHalt type, which is unrelated.
func HaltDecision(result core.AgentResult) ErrorDecision {
	return ErrorDecision{action: errHalt, result: result}
}

// StepControl is the mutable surface PrepareStep operates on.
// Set a field to a non-nil/non-zero value to override the agent default
// for this iteration only.
type StepControl struct {
	// Request is pre-populated with the current message history and
	// system prompt for this iteration. Mutate freely. Never nil.
	Request *core.ChatRequest
	// Model, if non-nil, swaps the LLM provider for this iteration only.
	// Next iteration sees the agent default again.
	Model core.Provider
	// Tools, if non-nil, replaces the tool set for this iteration only.
	// Empty slice is also a valid override ("no tools this iteration").
	// Use a nil slice (the zero value) to inherit.
	Tools []core.AnyTool
}

// PrepareStep runs before every LLM call inside the agent loop, including
// retries. Mutate StepControl in-place. A non-nil return error fails the run.
type PrepareStep func(ctx context.Context, iter int, ctrl *StepControl) error

// IterationSnapshot is a read-only view of one completed iteration.
type IterationSnapshot struct {
	// Response is the post-PostProcessor LLM response for this iteration.
	Response *core.ChatResponse
	// ToolCalls is the slice of tools the LLM invoked this iteration.
	ToolCalls []core.ToolCall
	// ToolResults is the slice of tool results returned to history
	// (after PostToolProcessor chain).
	ToolResults []core.ToolResult
	// Trace records timing, tokens, and model used for this iteration.
	Trace core.StepTrace
}

// OnIterationComplete runs after the LLM response, tool dispatch, and all
// post-tool processors finish. The returned IterationDecision controls
// what the loop does next. A non-nil return error fails the run.
type OnIterationComplete func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error)

// OnError runs when the LLM call or tool dispatch returns a non-graceful
// error in the loop. (*ErrHalt, *ErrSuspended, and context cancellation
// have their own paths and do not invoke this hook.)
// A non-nil return error fails the run with the hook's error, not the
// original.
type OnError func(ctx context.Context, iter int, err error) (ErrorDecision, error)
