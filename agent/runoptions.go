package agent

import (
	"fmt"
	"log/slog"

	"github.com/nevindra/oasis/memory"
)

// RunOptions overrides agent-level defaults for a single Execute call.
// nil fields keep the agent default; non-nil fields override.
//
// Pass via ExecuteWith / ExecuteStreamWith. nil and &RunOptions{} are
// equivalent and both mean "use all agent defaults" — both produce
// identical behavior to plain Execute.
type RunOptions struct {
	// Behavior overrides
	Prompt         *string
	Generation     *Generation
	ResponseSchema *ResponseSchema

	// Limits overrides the agent's resource budgets for this call. Zero
	// fields on Limits keep the agent's value (partial override). nil
	// means no override at all (same as omitting the field).
	Limits *Limits

	// Capability overrides — non-nil replaces fully; empty slice clears all.
	Tools        []AnyTool
	Agents       []Agent
	ActiveSkills []Skill

	// Pipeline overrides — non-nil replaces; nil keeps agent-level.
	PreProcessors      []PreProcessor
	PostProcessors     []PostProcessor
	PostToolProcessors []PostToolProcessor

	// Hook overrides — per-call hook customization.
	// Non-nil replaces the agent-level hook entirely (no chaining).
	PrepareStep         PrepareStep
	OnIterationComplete OnIterationComplete
	OnError             OnError

	// Infrastructure overrides — per-tenant / per-request swap.
	Memory       *memory.AgentMemory // swap the entire memory orchestrator per call
	InputHandler InputHandler        // per-call human-in-the-loop strategy
	Tracer       Tracer              // per-call tracer override
	Logger       *slog.Logger        // per-call logger override

	// Caller-supplied passthrough. Shallow-merged with agent-level metadata
	// (call-site keys win on conflict). Surfaces in StepTrace and hook args.
	Metadata map[string]any

	// Streaming overrides — apply only to ExecuteStream / StartStream paths.

	// StreamReplayLimit caps the per-stream replay ring buffer when using
	// the Stream wrapper (see StartStream). Zero means use the default (256).
	// Negative is invalid.
	StreamReplayLimit int
}

// Validate checks RunOptions for invalid field values. Called once at the
// top of ExecuteWith before any provider or hook is invoked. nil is valid.
func (o *RunOptions) Validate() error {
	if o == nil {
		return nil
	}
	if o.StreamReplayLimit < 0 {
		return &RunOptionsError{Field: "StreamReplayLimit", Message: "must be >= 0"}
	}
	if o.Limits != nil {
		lim := o.Limits
		if lim.MaxIter < 0 {
			return &RunOptionsError{Field: "Limits.MaxIter", Message: "must be >= 0"}
		}
		if lim.MaxSteps < 0 && lim.MaxSteps != Unbounded {
			return &RunOptionsError{Field: "Limits.MaxSteps", Message: "must be >= 0 or Unbounded"}
		}
		if lim.MaxPlanSteps < 0 {
			return &RunOptionsError{Field: "Limits.MaxPlanSteps", Message: "must be >= 0"}
		}
		if lim.MaxParallelDispatch < 0 {
			return &RunOptionsError{Field: "Limits.MaxParallelDispatch", Message: "must be >= 0"}
		}
		if lim.MaxAttachmentBytes < 0 {
			return &RunOptionsError{Field: "Limits.MaxAttachmentBytes", Message: "must be >= 0"}
		}
		if lim.MaxToolResultLen < 0 {
			return &RunOptionsError{Field: "Limits.MaxToolResultLen", Message: "must be >= 0"}
		}
		if lim.MaxSuspendSnapshots < 0 {
			return &RunOptionsError{Field: "Limits.MaxSuspendSnapshots", Message: "must be >= 0"}
		}
		if lim.MaxSuspendBytes < 0 {
			return &RunOptionsError{Field: "Limits.MaxSuspendBytes", Message: "must be >= 0"}
		}
	}
	return nil
}

// HasOverrides reports whether any field is set. Used by Network and
// Workflow to detect non-empty options on agents that do not yet
// support RunOptions propagation.
func (o *RunOptions) HasOverrides() bool {
	if o == nil {
		return false
	}
	return o.Prompt != nil ||
		o.Generation != nil ||
		o.ResponseSchema != nil ||
		o.Limits != nil ||
		o.Tools != nil ||
		o.Agents != nil ||
		o.ActiveSkills != nil ||
		o.PreProcessors != nil ||
		o.PostProcessors != nil ||
		o.PostToolProcessors != nil ||
		o.PrepareStep != nil ||
		o.OnIterationComplete != nil ||
		o.OnError != nil ||
		o.Memory != nil ||
		o.InputHandler != nil ||
		o.Tracer != nil ||
		o.Logger != nil ||
		len(o.Metadata) > 0 ||
		o.StreamReplayLimit != 0
}

// RunOptionsError reports a RunOptions validation failure.
type RunOptionsError struct {
	Field   string
	Message string
}

func (e *RunOptionsError) Error() string {
	return fmt.Sprintf("RunOptions.%s: %s", e.Field, e.Message)
}

// Compile-time interface check.
var _ error = (*RunOptionsError)(nil)

// applyRunOptions returns a Config with RunOptions overrides applied on
// top of base. base is never mutated. Returns base unchanged if opts is
// nil or has no overrides.
func applyRunOptions(base *Config, opts *RunOptions) *Config {
	if opts == nil || !opts.HasOverrides() {
		return base
	}
	// Shallow-copy Config. Pointer fields and slices that we override
	// are replaced; ones we leave alone keep base's references.
	out := *base
	c := &out

	// Behavior overrides
	if opts.Prompt != nil {
		c.systemPrompt = *opts.Prompt
	}
	if opts.Generation != nil {
		c.genParams = mergeGenerationParams(base.genParams, opts.Generation)
	}
	if opts.ResponseSchema != nil {
		c.responseSchema = opts.ResponseSchema
	}

	// Capability overrides — non-nil replaces; empty clears.
	if opts.Tools != nil {
		c.tools = opts.Tools
	}
	if opts.Agents != nil {
		c.agents = opts.Agents
	}
	if opts.ActiveSkills != nil {
		c.activeSkills = opts.ActiveSkills
	}

	// Limits block: runs after per-field handling so Limits wins if both are set.
	if opts.Limits != nil {
		lim := opts.Limits
		if lim.MaxIter != 0 {
			c.maxIter = lim.MaxIter
		}
		if lim.MaxSteps != 0 {
			if lim.MaxSteps == Unbounded {
				v := 0
				c.maxSteps = &v
			} else {
				v := lim.MaxSteps
				c.maxSteps = &v
			}
		}
		if lim.MaxPlanSteps != 0 {
			c.maxPlanSteps = lim.MaxPlanSteps
		}
		if lim.MaxParallelDispatch != 0 {
			c.maxParallelDispatch = lim.MaxParallelDispatch
		}
		if lim.MaxAttachmentBytes != 0 {
			c.maxAttachmentBytes = lim.MaxAttachmentBytes
		}
		if lim.MaxToolResultLen != 0 {
			c.maxToolResultLen = lim.MaxToolResultLen
		}
		if lim.MaxSuspendSnapshots != 0 {
			c.maxSuspendSnapshots = lim.MaxSuspendSnapshots
		}
		if lim.MaxSuspendBytes != 0 {
			c.maxSuspendBytes = lim.MaxSuspendBytes
		}
	}

	// Pipelines — non-nil replaces.
	if opts.PreProcessors != nil {
		c.preProcessors = opts.PreProcessors
	}
	if opts.PostProcessors != nil {
		c.postProcessors = opts.PostProcessors
	}
	if opts.PostToolProcessors != nil {
		c.postToolProcessors = opts.PostToolProcessors
	}

	// Hooks
	if opts.PrepareStep != nil {
		c.prepareStep = opts.PrepareStep
	}
	if opts.OnIterationComplete != nil {
		c.onIterationComplete = opts.OnIterationComplete
	}
	if opts.OnError != nil {
		c.onError = opts.OnError
	}

	// Infrastructure
	if opts.InputHandler != nil {
		c.inputHandler = opts.InputHandler
	}
	if opts.Tracer != nil {
		c.tracer = opts.Tracer
	}
	if opts.Logger != nil {
		c.logger = opts.Logger
	}
	// Memory swap is wired into the loop separately — see later task.

	// Metadata shallow merge
	if len(opts.Metadata) > 0 {
		merged := make(map[string]any, len(base.metadata)+len(opts.Metadata))
		for k, v := range base.metadata {
			merged[k] = v
		}
		for k, v := range opts.Metadata {
			merged[k] = v
		}
		c.metadata = merged
	}

	return c
}

// mergeGenerationParams returns a new GenerationParams where each non-nil
// field of override overrides the corresponding field of base. Fields not
// set on override inherit base's value.
func mergeGenerationParams(base *GenerationParams, override *Generation) *GenerationParams {
	out := &GenerationParams{}
	if base != nil {
		*out = *base
	}
	if override.Temperature != nil {
		out.Temperature = override.Temperature
	}
	if override.TopP != nil {
		out.TopP = override.TopP
	}
	if override.TopK != nil {
		out.TopK = override.TopK
	}
	if override.MaxTokens != nil {
		out.MaxTokens = override.MaxTokens
	}
	return out
}
