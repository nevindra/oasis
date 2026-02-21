package oasis

import (
	"context"
	"sync/atomic"
)

// AgentState represents the execution state of a spawned agent.
type AgentState int32

const (
	// StatePending indicates the agent has been spawned but Execute has not started.
	StatePending AgentState = iota
	// StateRunning indicates Execute is in progress.
	StateRunning
	// StateCompleted indicates Execute finished successfully.
	StateCompleted
	// StateFailed indicates Execute returned an error.
	StateFailed
	// StateCancelled indicates the agent was cancelled via Cancel() or parent context.
	StateCancelled
)

// String returns the state name.
func (s AgentState) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateRunning:
		return "running"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	case StateCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// IsTerminal reports whether the state is a final state
// (completed, failed, or cancelled).
func (s AgentState) IsTerminal() bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

// AgentHandle tracks a background agent execution.
// All methods are safe for concurrent use.
type AgentHandle struct {
	id     string
	agent  Agent
	state  atomic.Int32
	result AgentResult
	err    error
	done   chan struct{}
	cancel context.CancelFunc
}

// Spawn launches agent.Execute(ctx, task) in a background goroutine.
// Returns immediately with a handle for tracking, awaiting, and cancelling.
// The parent ctx controls the agent's lifetime — cancelling it cancels the agent.
func Spawn(ctx context.Context, agent Agent, task AgentTask) *AgentHandle {
	ctx, cancel := context.WithCancel(ctx)
	h := &AgentHandle{
		id:     NewID(),
		agent:  agent,
		done:   make(chan struct{}),
		cancel: cancel,
	}
	h.state.Store(int32(StatePending))

	go func() {
		defer cancel() // release context resources on completion
		h.state.Store(int32(StateRunning))
		result, err := agent.Execute(ctx, task)

		// Write result/err before close(done). The channel close is the
		// happens-before barrier: all readers (<-h.done in Await, State,
		// Result) are guaranteed to see these writes after the close.
		h.result = result
		h.err = err
		if ctx.Err() != nil && err != nil {
			h.state.Store(int32(StateCancelled))
		} else if err != nil {
			h.state.Store(int32(StateFailed))
		} else {
			h.state.Store(int32(StateCompleted))
		}
		close(h.done)
	}()

	return h
}

// ID returns the unique execution identifier (xid-based, time-sortable).
func (h *AgentHandle) ID() string { return h.id }

// Agent returns the agent being executed.
func (h *AgentHandle) Agent() Agent { return h.agent }

// State returns the current execution state.
// If the state is terminal, State blocks until Done() is closed (nanoseconds)
// to guarantee that Result() returns valid data when State().IsTerminal() is true.
func (h *AgentHandle) State() AgentState {
	s := AgentState(h.state.Load())
	if s.IsTerminal() {
		<-h.done
	}
	return s
}

// Done returns a channel closed when execution finishes (any terminal state).
// Composable with select for multiplexing multiple handles.
func (h *AgentHandle) Done() <-chan struct{} { return h.done }

// Await blocks until the agent completes or ctx is cancelled.
// Returns the agent's result and error on completion.
// Returns zero AgentResult and ctx.Err() if ctx is cancelled before completion.
func (h *AgentHandle) Await(ctx context.Context) (AgentResult, error) {
	select {
	case <-h.done:
		return h.result, h.err
	case <-ctx.Done():
		return AgentResult{}, ctx.Err()
	}
}

// Result returns the result and error. Only meaningful after Done() is closed.
// Before completion, returns zero AgentResult and nil error.
func (h *AgentHandle) Result() (AgentResult, error) {
	select {
	case <-h.done:
		return h.result, h.err
	default:
		return AgentResult{}, nil
	}
}

// Cancel requests cancellation. Non-blocking.
// The agent receives a cancelled context. State transitions to StateCancelled
// once Execute returns.
func (h *AgentHandle) Cancel() { h.cancel() }
