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
	MaxIter        *int
	MaxSteps       *int
	MaxPlanSteps   *int
	ResponseSchema *ResponseSchema

	// Capability overrides — non-nil replaces fully; empty slice clears all.
	Tools        []AnyTool
	Agents       []Agent
	ActiveSkills []Skill

	// Resource budget overrides
	MaxAttachmentBytes *int64
	MaxToolResultLen   *int

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
	if o.MaxIter != nil && *o.MaxIter <= 0 {
		return &RunOptionsError{Field: "MaxIter", Message: "must be > 0"}
	}
	if o.MaxSteps != nil && *o.MaxSteps <= 0 {
		return &RunOptionsError{Field: "MaxSteps", Message: "must be > 0"}
	}
	if o.MaxPlanSteps != nil && *o.MaxPlanSteps <= 0 {
		return &RunOptionsError{Field: "MaxPlanSteps", Message: "must be > 0"}
	}
	if o.MaxAttachmentBytes != nil && *o.MaxAttachmentBytes <= 0 {
		return &RunOptionsError{Field: "MaxAttachmentBytes", Message: "must be > 0"}
	}
	if o.MaxToolResultLen != nil && *o.MaxToolResultLen <= 0 {
		return &RunOptionsError{Field: "MaxToolResultLen", Message: "must be > 0"}
	}
	if o.StreamReplayLimit < 0 {
		return &RunOptionsError{Field: "StreamReplayLimit", Message: "must be >= 0"}
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
		o.MaxIter != nil ||
		o.MaxSteps != nil ||
		o.MaxPlanSteps != nil ||
		o.ResponseSchema != nil ||
		o.Tools != nil ||
		o.Agents != nil ||
		o.ActiveSkills != nil ||
		o.MaxAttachmentBytes != nil ||
		o.MaxToolResultLen != nil ||
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
		c.prompt = *opts.Prompt
	}
	if opts.Generation != nil {
		c.generationParams = mergeGenerationParams(base.generationParams, opts.Generation)
	}
	if opts.MaxIter != nil {
		c.maxIter = *opts.MaxIter
	}
	if opts.MaxSteps != nil {
		v := *opts.MaxSteps
		c.maxSteps = &v
	}
	if opts.MaxPlanSteps != nil {
		c.maxPlanSteps = *opts.MaxPlanSteps
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

	// Resource budgets
	if opts.MaxAttachmentBytes != nil {
		c.maxAttachmentBytes = *opts.MaxAttachmentBytes
	}
	if opts.MaxToolResultLen != nil {
		c.maxToolResultLen = *opts.MaxToolResultLen
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
