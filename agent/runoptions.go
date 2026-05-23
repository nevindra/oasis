package agent

import (
	"github.com/nevindra/oasis/internal/runtime"
	"github.com/nevindra/oasis/core"
)

// RunOptions, Generation, Limits, Unbounded, and the HasOverrides/Validate methods
// are defined in internal/runtime and re-exported as type aliases in agent/agent.go.

// RunOptionsError reports a RunOptions validation failure.
// Re-exported from runtime so callers can use agent.RunOptionsError directly.
type RunOptionsError = runtime.RunOptionsError

// applyRunOptions returns a Config with RunOptions overrides applied on top of base.
// Delegates to the runtime package implementation.
func applyRunOptions(base *Config, opts *RunOptions) *Config {
	return runtime.ApplyRunOptionsToConfig(base, opts)
}

// WithOverrides returns a core.RunOption that packs opts into the per-call
// RunConfig so the agent's Execute can apply them.
func WithOverrides(opts *RunOptions) core.RunOption {
	return func(c *core.RunConfig) {
		if opts == nil {
			return
		}
		c.Overrides = opts
	}
}

// Re-exports of the core built-in RunOptions so users may write
// agent.WithStream(ch) instead of core.WithStream(ch).
//
// NOTE: WithTracer is not re-exported here because agent.WithTracer already
// exists as an AgentOption (construction-time configuration).
var (
	WithStream   = core.WithStream
	WithDeadline = core.WithDeadline
)
