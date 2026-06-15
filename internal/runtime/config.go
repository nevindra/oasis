package runtime

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/skills"
)

// AgentOption configures an LLMAgent or Network.
type AgentOption func(*Config)

// PromptFunc resolves the system prompt per-request.
type PromptFunc func(ctx context.Context, task core.AgentTask) string

// ToolsFunc resolves the tool set per-request.
type ToolsFunc func(ctx context.Context, task core.AgentTask) []core.AnyTool

// DenyAction controls behavior when a human denies a tool approval request.
type DenyAction int

const (
	// DenyAskLLMToRevise returns a ToolResult with Error set.
	DenyAskLLMToRevise DenyAction = iota
	// DenyHalt halts the agent loop with *core.ErrHalt.
	DenyHalt
)

// ApprovalOption is a functional option for WithToolApproval.
type ApprovalOption func(*ApprovalConfig)

// ApprovalConfig holds per-tool approval configuration.
type ApprovalConfig struct {
	ToolName string
	Prompt   func(call core.ToolCall) string
	OnDeny   DenyAction
}

// toolPolicyMatcher pairs a name predicate with a policy.
type toolPolicyMatcher struct {
	match  func(name string) bool
	policy core.ToolPolicy
}

// toolTransformMatcher pairs a name predicate with a transform.
type toolTransformMatcher struct {
	match     func(name string) bool
	transform core.ToolTransform
}

// InputRequest describes what the agent needs from the human.
type InputRequest struct {
	Question string
	Options  []string
	Metadata map[string]string
}

// InputResponse is the human's reply.
type InputResponse struct {
	Value string
}

// InputHandler delivers questions to a human and returns their response.
type InputHandler interface {
	RequestInput(ctx context.Context, req InputRequest) (InputResponse, error)
}

// Config holds shared configuration for LLMAgent and Network.
// All fields are exported so that the agent package can alias Config and still
// access them through the alias in agent subfiles.
type Config struct {
	Tools               []core.AnyTool
	SystemPrompt        string
	MaxIter             int
	PreProcessors       []core.PreProcessor
	PostProcessors      []core.PostProcessor
	PostToolProcessors  []core.PostToolProcessor
	InputHandler        InputHandler
	Embedding           core.EmbeddingProvider
	MemoryConfig        memory.AgentMemoryConfig
	MemoryInitialized   bool
	CrossThreadSearch   bool
	PlanExecution       bool
	Sandbox             core.Sandbox
	SandboxTools        []core.AnyTool
	ResponseSchema      *core.ResponseSchema
	DynamicPrompt       PromptFunc
	DynamicModel        core.ModelFunc
	DynamicTools        ToolsFunc
	Tracer              core.Tracer
	Logger              *slog.Logger
	MaxAttachmentBytes  int64
	MaxSuspendSnapshots int
	MaxSuspendBytes     int64
	CompressModel       core.ModelFunc
	CompressThreshold   int
	Compactor           core.Compactor
	CompactThreshold    float64
	GenParams           *core.GenerationParams
	ActiveSkills        []skills.Skill
	SkillProvider       skills.SkillProvider
	// SkillCatalog, when true, injects the provider's Discover() summaries into
	// the system prompt each request so the model sees available skills before
	// its first tool call. Set via agent.WithSkillCatalog.
	SkillCatalog bool

	// DisablePromptCaching opts the agent out of automatic cache-breakpoint
	// placement on its LLM calls. By default (DisablePromptCaching=false), the
	// agent loop marks messages[0] (system + tools prefix) and the current tail
	// message as cache checkpoints — providers that support ephemeral caching
	// (Anthropic, Qwen, OpenAI-compat shims) treat those as breakpoints and
	// cache the matching prefix across iterations. Providers without cache
	// support ignore the bits. Set via agent.WithoutPromptCaching.
	DisablePromptCaching bool

	// Hook fields
	PrepareStep         PrepareStep
	OnIterationComplete OnIterationComplete
	OnError             OnError

	// Tool middleware applied to every registered tool at build time.
	ToolMiddleware []core.ToolMiddleware

	// AgentMiddleware wraps the agent's Execute method. Applied lazily on
	// first Execute call and cached. Set via agent.WithMiddleware.
	// Stored as the underlying function type to avoid an import cycle between
	// agent and runtime (agent.Middleware is defined as this function type).
	AgentMiddleware []func(core.Agent) core.Agent

	// Per-tool approval gates configured via WithToolApproval.
	ToolApprovals []ApprovalConfig

	// Per-tool retry/timeout policy (exact name entries).
	ToolPolicies map[string]core.ToolPolicy
	// Ordered matchers; first match wins (after exact).
	ToolPolicyMatchers []toolPolicyMatcher

	// Per-tool payload transforms (exact name entries).
	ToolTransforms map[string]core.ToolTransform
	// Ordered transform matchers; first match wins (after exact).
	ToolTransformMatchers []toolTransformMatcher

	// Configurable runtime limits.
	MaxParallelDispatch int
	MaxPlanSteps        int
	MaxToolResultLen    int

	// Tool result paging store.
	ToolResultStore    core.ToolResultStore
	ToolResultStoreSet bool

	// maxSteps: nil=unset(default 100), &0=unbounded, &n=cap at n.
	MaxSteps *int

	// Metadata is shallow-merged with RunOptions.Metadata at run time.
	// Values are strings — callers needing structured data should JSON-encode it.
	Metadata map[string]string
}

// ResolveToolPolicy implements ServeMux-style policy lookup: exact-name first,
// then matchers in registration order.
func (c *Config) ResolveToolPolicy(name string) (core.ToolPolicy, bool) {
	if c == nil {
		return core.ToolPolicy{}, false
	}
	if p, ok := c.ToolPolicies[name]; ok {
		return p, true
	}
	for _, m := range c.ToolPolicyMatchers {
		if m.match(name) {
			return m.policy, true
		}
	}
	return core.ToolPolicy{}, false
}

// AddToolPolicyMatcher appends a matcher. Used by agent.WithToolPolicyMatch.
func (c *Config) AddToolPolicyMatcher(match func(string) bool, policy core.ToolPolicy) {
	c.ToolPolicyMatchers = append(c.ToolPolicyMatchers, toolPolicyMatcher{match: match, policy: policy})
}

// ResolveToolTransform implements ServeMux-style lookup: exact-name first,
// then matchers in registration order. Mirrors ResolveToolPolicy.
func (c *Config) ResolveToolTransform(name string) (core.ToolTransform, bool) {
	if c == nil {
		return core.ToolTransform{}, false
	}
	if tt, ok := c.ToolTransforms[name]; ok {
		return tt, true
	}
	for _, m := range c.ToolTransformMatchers {
		if m.match(name) {
			return m.transform, true
		}
	}
	return core.ToolTransform{}, false
}

// AddToolTransformMatcher appends a transform matcher. Used by ToolConfig.ApplyTo.
func (c *Config) AddToolTransformMatcher(match func(string) bool, tt core.ToolTransform) {
	c.ToolTransformMatchers = append(c.ToolTransformMatchers, toolTransformMatcher{match: match, transform: tt})
}

// ---- Limits ----

// Unbounded is the sentinel value for limit fields whose "0" already has a meaning.
const Unbounded = -1

// Limits groups the agent's resource-budget knobs into one typed sub-config.
type Limits struct {
	MaxIter             int
	MaxSteps            int
	MaxPlanSteps        int
	MaxParallelDispatch int
	MaxAttachmentBytes  int64
	MaxToolResultLen    int
	MaxSuspendSnapshots int
	MaxSuspendBytes     int64
}

// ApplyTo overlays non-zero fields from l onto c.
func (l Limits) ApplyTo(c *Config) {
	if l.MaxIter != 0 {
		c.MaxIter = l.MaxIter
	}
	if l.MaxSteps != 0 {
		v := l.MaxSteps
		if v == Unbounded {
			v = 0
		}
		c.MaxSteps = &v
	}
	if l.MaxPlanSteps != 0 {
		c.MaxPlanSteps = l.MaxPlanSteps
	}
	if l.MaxParallelDispatch != 0 {
		c.MaxParallelDispatch = l.MaxParallelDispatch
	}
	if l.MaxAttachmentBytes != 0 {
		c.MaxAttachmentBytes = l.MaxAttachmentBytes
	}
	if l.MaxToolResultLen != 0 {
		c.MaxToolResultLen = l.MaxToolResultLen
	}
	if l.MaxSuspendSnapshots != 0 {
		c.MaxSuspendSnapshots = l.MaxSuspendSnapshots
	}
	if l.MaxSuspendBytes != 0 {
		c.MaxSuspendBytes = l.MaxSuspendBytes
	}
}

// ---- Processors & Hooks ----

// Processors groups the processor-chain hooks fired by the run loop.
// Use with agent.WithProcessors to wire several chains in one call instead
// of three separate option functions.
type Processors struct {
	// Pre runs before each LLM call. Stops the iteration if any returns ErrHalt.
	Pre []core.PreProcessor
	// Post runs after each LLM response. Can rewrite content or stop the loop.
	Post []core.PostProcessor
	// PostTool runs after each tool result, in dispatch order.
	PostTool []core.PostToolProcessor
}

// ApplyTo appends p's non-empty slices onto c's existing processor chains.
// Append (not replace) preserves the legacy per-option behavior where multiple
// agent.With*Processor calls accumulated.
func (p Processors) ApplyTo(c *Config) {
	if len(p.Pre) > 0 {
		c.PreProcessors = append(c.PreProcessors, p.Pre...)
	}
	if len(p.Post) > 0 {
		c.PostProcessors = append(c.PostProcessors, p.Post...)
	}
	if len(p.PostTool) > 0 {
		c.PostToolProcessors = append(c.PostToolProcessors, p.PostTool...)
	}
}

// Hooks groups the mid-iteration callbacks the run loop invokes.
// Use with agent.WithHooks to wire several callbacks in one call. A nil field
// leaves the corresponding hook untouched, so multiple WithHooks calls compose
// per-field rather than replacing the whole bundle.
type Hooks struct {
	// PrepareStep runs before every LLM call; can mutate the prompt/messages
	// via *StepControl or short-circuit the iteration.
	PrepareStep PrepareStep
	// OnIterationComplete runs after every iteration with the post-iteration
	// snapshot; can request stop/continue.
	OnIterationComplete OnIterationComplete
	// OnError runs on a mid-loop error and can recover (retry, halt, propagate).
	OnError OnError
}

// ApplyTo writes h's non-nil hooks onto c. Nil fields leave c untouched.
func (h Hooks) ApplyTo(c *Config) {
	if h.PrepareStep != nil {
		c.PrepareStep = h.PrepareStep
	}
	if h.OnIterationComplete != nil {
		c.OnIterationComplete = h.OnIterationComplete
	}
	if h.OnError != nil {
		c.OnError = h.OnError
	}
}

// LimitsFromConfig projects Config budget fields back into a Limits value.
func LimitsFromConfig(c *Config) Limits {
	maxSteps := 0
	if c.MaxSteps != nil {
		maxSteps = *c.MaxSteps
	}
	if maxSteps == 0 {
		maxSteps = Unbounded
	}
	return Limits{
		MaxIter:             c.MaxIter,
		MaxSteps:            maxSteps,
		MaxPlanSteps:        c.MaxPlanSteps,
		MaxParallelDispatch: c.MaxParallelDispatch,
		MaxAttachmentBytes:  c.MaxAttachmentBytes,
		MaxToolResultLen:    c.MaxToolResultLen,
		MaxSuspendSnapshots: c.MaxSuspendSnapshots,
		MaxSuspendBytes:     c.MaxSuspendBytes,
	}
}

// ---- Generation ----

// Generation groups the LLM sampling and output parameters.
//
// Alias to core.GenerationParams so RunOptions.Generation and
// core.ChatRequest.GenerationParams share one shape — any new field added to
// the canonical type auto-propagates to the user-facing override surface
// without a second edit.
type Generation = core.GenerationParams

// CloneGeneration returns a deep copy of g — each non-nil pointer field is
// freshly allocated so the caller and source no longer share underlying
// values. Used by Runtime.Generation, mergeGenerationParams, and
// agent.WithGeneration to keep their copy semantics consistent.
func CloneGeneration(g Generation) Generation {
	var out Generation
	overlayNonNilGeneration(&out, &g)
	return out
}

// overlayNonNilGeneration copies each non-nil pointer field of src into dst,
// deep-copying so dst no longer aliases src. nil fields on src leave dst's
// existing value in place. The single owner of the GenerationParams field
// list — both CloneGeneration and mergeGenerationParams delegate here so a
// new field added to core.GenerationParams only needs one edit.
func overlayNonNilGeneration(dst *core.GenerationParams, src *Generation) {
	if src.Temperature != nil {
		v := *src.Temperature
		dst.Temperature = &v
	}
	if src.TopP != nil {
		v := *src.TopP
		dst.TopP = &v
	}
	if src.TopK != nil {
		v := *src.TopK
		dst.TopK = &v
	}
	if src.MaxTokens != nil {
		v := *src.MaxTokens
		dst.MaxTokens = &v
	}
}

// ---- RunOptions ----

// RunOptions overrides agent-level defaults for a single Execute call.
type RunOptions struct {
	Prompt         *string
	Generation     *Generation
	ResponseSchema *core.ResponseSchema

	Limits *Limits

	Tools        []core.AnyTool
	ActiveSkills []skills.Skill

	PreProcessors      []core.PreProcessor
	PostProcessors     []core.PostProcessor
	PostToolProcessors []core.PostToolProcessor

	PrepareStep         PrepareStep
	OnIterationComplete OnIterationComplete
	OnError             OnError

	Memory       *memory.AgentMemory
	InputHandler InputHandler
	Tracer       core.Tracer
	Logger       *slog.Logger

	Metadata map[string]string

	StreamReplayLimit int
}

// IsRunOverrides marks *RunOptions as a valid core.RunOverrides payload so
// it can be carried in core.RunConfig.Overrides without going through an
// untyped any boundary. The method has no behavior.
func (*RunOptions) IsRunOverrides() {}

// Validate checks RunOptions for invalid field values. nil is valid.
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

// RunOptionsError reports a RunOptions validation failure.
type RunOptionsError struct {
	Field   string
	Message string
}

func (e *RunOptionsError) Error() string {
	return fmt.Sprintf("RunOptions.%s: %s", e.Field, e.Message)
}

// HasOverrides reports whether any field is set.
func (o *RunOptions) HasOverrides() bool {
	if o == nil {
		return false
	}
	return o.Prompt != nil ||
		o.Generation != nil ||
		o.ResponseSchema != nil ||
		o.Limits != nil ||
		o.Tools != nil ||
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
