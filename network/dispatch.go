// Package network — parallel-dispatch knob.
//
// The framework already dispatches tool calls in parallel by default
// (see agent.dispatchParallel + Limits.MaxParallelDispatch=10 default).
// This file exposes a Network-level switch so users can disable parallelism
// or document intent without reaching into agent.Limits directly.
package network

// ParallelDispatch controls how a Network executes multiple tool calls
// emitted by the router in the same iteration. The framework dispatches
// tool calls in parallel by default (up to 10 concurrent calls per
// iteration); pass ParallelDisabled to force strict sequential dispatch.
type ParallelDispatch int

const (
	// ParallelDefault uses the framework's default parallel-dispatch limit
	// (currently 10 concurrent tool calls per iteration). This is the
	// existing behavior and remains the default for any Network that does
	// not specify WithParallelDispatch.
	ParallelDefault ParallelDispatch = iota

	// ParallelDisabled forces sequential dispatch within an iteration. Use
	// this when tools have ordering dependencies that the LLM's response
	// ordering must respect.
	ParallelDisabled
)

// ParallelByDefault is an alias for ParallelDefault matching the Plan B
// design-spec name. The framework's default is already parallel; this name
// makes that explicit at the call site.
const ParallelByDefault = ParallelDefault

// WithParallelDispatch controls per-iteration tool-call parallelism in the
// router loop.
//
// The framework dispatches tool calls in parallel by default (up to 10
// concurrent calls per iteration). Pass ParallelDisabled to force strict
// sequential dispatch — useful when tools have ordering dependencies that
// can't be expressed in the LLM's response order.
//
// To set a specific parallel limit (not just enable/disable), use
// WithRouter(agent.WithLimits(agent.Limits{MaxParallelDispatch: N})).
//
// When children run in parallel and emit stream events, the events from
// different children interleave in the parent's channel. This is expected;
// each event carries the child's name so consumers can demultiplex.
func WithParallelDispatch(mode ParallelDispatch) Option {
	return func(n *Network) {
		n.parallelDispatch = mode
	}
}
