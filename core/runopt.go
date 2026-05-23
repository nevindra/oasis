// core/runopt.go
package core

import "time"

// RunConfig holds per-call configuration accumulated from RunOption values.
// Implementations of [Agent] inspect this struct after calling
// [ApplyRunOptions] to decide which capabilities to enable for this Execute
// call (streaming, deadlines, tracing, overrides).
type RunConfig struct {
	// Stream is the optional channel to push StreamEvent values into during
	// execution. When non-nil, implementations MUST close it before
	// returning. Nil disables streaming.
	Stream chan<- StreamEvent
	// Deadline is the optional per-call wall-clock cap. Zero means no cap.
	Deadline time.Duration
	// Tracer is the optional per-call tracer. Nil falls back to the
	// agent's construction-time tracer.
	Tracer Tracer
	// Overrides is an opaque pointer to a *agent.RunOptions, populated by
	// agent.WithOverrides. The core package cannot type this directly
	// without importing agent (cycle), so implementations type-assert.
	Overrides any
}

// RunOption configures a single Execute call. Built-in options live in this
// package (WithStream, WithDeadline, WithTracer); package-specific options
// live in the package that owns the target type (e.g. agent.WithOverrides
// references *agent.RunOptions).
//
// Add new per-call capabilities by adding new RunOption constructors — never
// by changing the Agent interface signature.
type RunOption func(*RunConfig)

// ApplyRunOptions returns a RunConfig with every supplied option applied in
// order (later options override earlier ones).
func ApplyRunOptions(opts ...RunOption) RunConfig {
	var cfg RunConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// WithStream attaches a stream channel to the call. The agent will push
// StreamEvent values into ch and close it before Execute returns.
//
// Contract: callers must NOT close ch themselves. Consume via
// `for ev := range ch` — the range exits when the agent closes ch.
func WithStream(ch chan<- StreamEvent) RunOption {
	return func(c *RunConfig) { c.Stream = ch }
}

// WithDeadline sets a per-call wall-clock cap. The agent applies it as a
// context.WithTimeout layered onto the caller's ctx.
func WithDeadline(d time.Duration) RunOption {
	return func(c *RunConfig) { c.Deadline = d }
}

// WithTracer sets a per-call tracer that overrides the agent's
// construction-time tracer for this Execute only.
func WithTracer(t Tracer) RunOption {
	return func(c *RunConfig) { c.Tracer = t }
}
