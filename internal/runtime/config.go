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

// SubAgentOption configures spawn_agent behavior.
type SubAgentOption func(*Config)

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
	Agents              []core.Agent
	SystemPrompt        string
	MaxIter             int
	PreProcessors       []core.PreProcessor
	PostProcessors      []core.PostProcessor
	PostToolProcessors  []core.PostToolProcessor
	InputHandler        InputHandler
	Store               core.Store
	Embedding           core.EmbeddingProvider
	MemoryConfig        memory.AgentMemoryConfig
	MemoryInitialized   bool
	CrossThreadSearch   bool
	SemanticMinScore    float32
	MaxHistory          int
	MaxTokens           int
	AutoTitle           bool
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
	SemanticTrimming    bool
	TrimmingEmbedding   core.EmbeddingProvider
	KeepRecent          int
	SpawnEnabled        bool
	SpawnDepthLimit     int
	DeniedSpawnTools    []string
	ActiveSkills        []skills.Skill
	SkillProvider       skills.SkillProvider

	// Hook fields
	PrepareStep         PrepareStep
	OnIterationComplete OnIterationComplete
	OnError             OnError

	// Tool middleware applied to every registered tool at build time.
	ToolMiddleware []core.ToolMiddleware

	// Per-tool approval gates configured via WithToolApproval.
	ToolApprovals []ApprovalConfig

	// Per-tool retry/timeout policy (exact name entries).
	ToolPolicies map[string]core.ToolPolicy
	// Ordered matchers; first match wins (after exact).
	ToolPolicyMatchers []toolPolicyMatcher

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
	Metadata map[string]any
}

// GetAgents returns the subagents registered via WithAgents.
func (c *Config) GetAgents() []core.Agent { return c.Agents }

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
type Generation struct {
	Temperature *float64
	TopP        *float64
	TopK        *int
	MaxTokens   *int
}

// ---- RunOptions ----

// RunOptions overrides agent-level defaults for a single Execute call.
type RunOptions struct {
	Prompt         *string
	Generation     *Generation
	ResponseSchema *core.ResponseSchema

	Limits *Limits

	Tools        []core.AnyTool
	Agents       []core.Agent
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

	Metadata map[string]any

	StreamReplayLimit int
}

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
