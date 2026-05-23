package agent

import (
	"context"
	"log/slog"

	"github.com/nevindra/oasis/internal/runtime"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/skills"
)

// Agent, AgentTask, AgentResult, StepTrace are defined in core/
// and re-exported here as aliases for backward compatibility.
type Agent = core.Agent
type AgentTask = core.AgentTask
type AgentResult = core.AgentResult
type StepTrace = core.StepTrace

// ---- Type aliases for types that moved to runtime ----

// Config holds shared configuration for LLMAgent and Network.
// The underlying type is runtime.Config; this alias keeps the public API stable.
type Config = runtime.Config

// AgentOption configures an LLMAgent or Network.
type AgentOption = runtime.AgentOption

// PromptFunc resolves the system prompt per-request.
type PromptFunc = runtime.PromptFunc

// ToolsFunc resolves the tool set per-request.
type ToolsFunc = runtime.ToolsFunc

// Generation groups the LLM sampling and output parameters.
type Generation = runtime.Generation

// Limits groups the agent's resource-budget knobs.
type Limits = runtime.Limits

// Unbounded is the sentinel value for limit fields.
const Unbounded = runtime.Unbounded

// RunOption configures a single Execute call. Alias for core.RunOption.
type RunOption = core.RunOption

// RunOptions overrides agent-level defaults for a single Execute call.
type RunOptions = runtime.RunOptions

// DispatchResult holds the result of a single tool or agent dispatch.
type DispatchResult = runtime.DispatchResult

// DispatchFunc executes a single tool call and returns the result.
type DispatchFunc = runtime.DispatchFunc

// ToolExecFunc executes a tool by name.
type ToolExecFunc = runtime.ToolExecFunc

// ToolExecStreamFunc executes a tool with streaming progress support.
type ToolExecStreamFunc = runtime.ToolExecStreamFunc

// LoopConfig holds everything the shared runLoop needs to run.
type LoopConfig = runtime.LoopConfig

// PrepareStep runs before every LLM call inside the agent loop.
type PrepareStep = runtime.PrepareStep

// OnIterationComplete runs after each loop iteration.
type OnIterationComplete = runtime.OnIterationComplete

// OnError runs when an error occurs in the loop.
type OnError = runtime.OnError

// IterationDecision is the return value of an OnIterationComplete hook.
type IterationDecision = runtime.IterationDecision

// ErrorDecision is the return value of an OnError hook.
type ErrorDecision = runtime.ErrorDecision

// StepControl is the mutable surface PrepareStep operates on.
type StepControl = runtime.StepControl

// IterationSnapshot is a read-only view of one completed iteration.
type IterationSnapshot = runtime.IterationSnapshot

// InputHandler delivers questions to a human and returns their response.
type InputHandler = runtime.InputHandler

// InputRequest describes what the agent needs from the human.
type InputRequest = runtime.InputRequest

// InputResponse is the human's reply.
type InputResponse = runtime.InputResponse

// DenyAction controls behavior when a human denies a tool approval request.
type DenyAction = runtime.DenyAction

const (
	// DenyAskLLMToRevise is the default deny action.
	DenyAskLLMToRevise = runtime.DenyAskLLMToRevise
	// DenyHalt halts the agent loop on deny.
	DenyHalt = runtime.DenyHalt
)

// ApprovalOption is a functional option for WithToolApproval.
type ApprovalOption = runtime.ApprovalOption

// ApprovalConfig holds per-tool approval configuration.
type ApprovalConfig = runtime.ApprovalConfig

// Hook constructors re-exported from runtime.
var (
	Continue            = runtime.Continue
	Stop                = runtime.Stop
	InjectFeedback      = runtime.InjectFeedback
	InjectMessages      = runtime.InjectMessages
	Propagate           = runtime.Propagate
	Retry               = runtime.Retry
	RetryWithFeedback   = runtime.RetryWithFeedback
	HaltDecision        = runtime.HaltDecision
)

// ---- AgentOption constructors ----

// WithTools adds tools to the agent or network.
func WithTools(tools ...core.AnyTool) AgentOption {
	return func(c *Config) { c.Tools = append(c.Tools, tools...) }
}

// WithToolMiddleware registers a chain of tool middlewares applied to every
// tool at build time. First in mws is innermost (closest to the tool); last
// is outermost.
func WithToolMiddleware(mws ...core.ToolMiddleware) AgentOption {
	return func(c *Config) {
		c.ToolMiddleware = append(c.ToolMiddleware, mws...)
	}
}

// WithPrompt sets the system prompt for the agent or network router.
func WithPrompt(s string) AgentOption {
	return func(c *Config) { c.SystemPrompt = s }
}

// WithLimits sets the agent's resource-budget Limits in one option.
func WithLimits(lim Limits) AgentOption {
	return func(c *Config) { lim.ApplyTo(c) }
}

// WithGeneration sets LLM sampling and output parameters in one call.
func WithGeneration(g Generation) AgentOption {
	return func(c *Config) {
		if c.GenParams == nil {
			c.GenParams = &core.GenerationParams{}
		}
		if g.Temperature != nil {
			v := *g.Temperature
			c.GenParams.Temperature = &v
		} else {
			c.GenParams.Temperature = nil
		}
		if g.TopP != nil {
			v := *g.TopP
			c.GenParams.TopP = &v
		} else {
			c.GenParams.TopP = nil
		}
		if g.TopK != nil {
			v := *g.TopK
			c.GenParams.TopK = &v
		} else {
			c.GenParams.TopK = nil
		}
		if g.MaxTokens != nil {
			v := *g.MaxTokens
			c.GenParams.MaxTokens = &v
		} else {
			c.GenParams.MaxTokens = nil
		}
	}
}

// WithPlanExecution enables the built-in "execute_plan" tool.
func WithPlanExecution() AgentOption {
	return func(c *Config) { c.PlanExecution = true }
}

// WithSandbox attaches a sandbox environment to the agent.
func WithSandbox(sb core.Sandbox, tools ...core.AnyTool) AgentOption {
	return func(c *Config) {
		c.Sandbox = sb
		c.SandboxTools = tools
	}
}

// WithActiveSkills pre-activates skills whose instructions are appended to
// the agent's system prompt on every LLM call.
func WithActiveSkills(ss ...skills.Skill) AgentOption {
	return func(c *Config) { c.ActiveSkills = append(c.ActiveSkills, ss...) }
}

// WithSkills registers a SkillProvider and automatically adds skill tools.
func WithSkills(p skills.SkillProvider) AgentOption {
	return func(c *Config) { c.SkillProvider = p }
}

// WithResponseSchema sets the response schema for structured JSON output.
func WithResponseSchema(s *core.ResponseSchema) AgentOption {
	return func(c *Config) { c.ResponseSchema = s }
}

// WithDynamicPrompt sets a per-request prompt resolution function.
func WithDynamicPrompt(fn PromptFunc) AgentOption {
	return func(c *Config) { c.DynamicPrompt = fn }
}

// WithDynamicModel sets a per-request model selection function.
func WithDynamicModel(fn core.ModelFunc) AgentOption {
	return func(c *Config) { c.DynamicModel = fn }
}

// WithDynamicTools sets a per-request tool selection function.
func WithDynamicTools(fn ToolsFunc) AgentOption {
	return func(c *Config) { c.DynamicTools = fn }
}

// WithTracer sets the tracer for the agent.
func WithTracer(t core.Tracer) AgentOption {
	return func(c *Config) { c.Tracer = t }
}

// WithLogger sets the structured logger for the agent.
func WithLogger(l *slog.Logger) AgentOption {
	return func(c *Config) { c.Logger = l }
}

// WithMetadata adds key/value pairs to the agent's static metadata map.
func WithMetadata(kv map[string]any) AgentOption {
	return func(c *Config) {
		if c.Metadata == nil {
			c.Metadata = make(map[string]any, len(kv))
		}
		for k, v := range kv {
			c.Metadata[k] = v
		}
	}
}

// WithPreProcessors registers PreProcessor hooks that run before each LLM call.
func WithPreProcessors(processors ...core.PreProcessor) AgentOption {
	return func(c *Config) { c.PreProcessors = append(c.PreProcessors, processors...) }
}

// WithPostProcessors registers PostProcessor hooks that run after each LLM response.
func WithPostProcessors(processors ...core.PostProcessor) AgentOption {
	return func(c *Config) { c.PostProcessors = append(c.PostProcessors, processors...) }
}

// WithPostToolProcessors registers PostToolProcessor hooks that run after each tool result.
func WithPostToolProcessors(processors ...core.PostToolProcessor) AgentOption {
	return func(c *Config) { c.PostToolProcessors = append(c.PostToolProcessors, processors...) }
}

// WithInputHandler sets the handler for human-in-the-loop interactions.
func WithInputHandler(h InputHandler) AgentOption {
	return func(c *Config) { c.InputHandler = h }
}

// WithPrepareStep registers a PrepareStep hook.
func WithPrepareStep(fn PrepareStep) AgentOption {
	return func(c *Config) { c.PrepareStep = fn }
}

// WithOnIterationComplete registers an OnIterationComplete hook.
func WithOnIterationComplete(fn OnIterationComplete) AgentOption {
	return func(c *Config) { c.OnIterationComplete = fn }
}

// WithOnError registers an OnError hook for mid-loop error recovery.
func WithOnError(fn OnError) AgentOption {
	return func(c *Config) { c.OnError = fn }
}

// WithEmbedding sets the embedding provider.
func WithEmbedding(e core.EmbeddingProvider) AgentOption {
	return func(c *Config) { c.Embedding = e }
}

// WithMemory configures the agent's memory system.
func WithMemory(opts ...memory.Option) AgentOption {
	return func(c *Config) {
		cfg := memory.BuildConfig(opts...)
		c.MemoryConfig = cfg
		c.MemoryInitialized = true
		c.Tools = append(c.Tools, cfg.Tools...)

		c.MaxHistory = cfg.MaxHistory
		c.MaxTokens = cfg.MaxTokens
		c.AutoTitle = cfg.AutoTitle
		c.CrossThreadSearch = cfg.SemanticRecall
		c.SemanticMinScore = cfg.SemanticMinScore
		c.SemanticTrimming = cfg.SemanticTrimming
		if cfg.TrimmingEmbedding != nil {
			c.TrimmingEmbedding = cfg.TrimmingEmbedding
		}
		c.KeepRecent = cfg.KeepRecent
		c.Compactor = cfg.Compactor
		c.CompactThreshold = cfg.CompactThreshold
		c.CompressModel = cfg.CompressModel
		c.CompressThreshold = cfg.CompressThreshold
		if cfg.Store != nil {
			c.Store = cfg.Store
		}
		if cfg.Embedding != nil {
			c.Embedding = cfg.Embedding
		}
	}
}

// WithToolPolicy attaches a per-tool timeout and retry policy.
func WithToolPolicy(name string, p core.ToolPolicy) AgentOption {
	return func(c *Config) {
		if c.ToolPolicies == nil {
			c.ToolPolicies = map[string]core.ToolPolicy{}
		}
		c.ToolPolicies[name] = p
	}
}

// WithToolPolicyMatch attaches a policy to every tool whose name satisfies
// the matcher predicate.
func WithToolPolicyMatch(matcher func(name string) bool, p core.ToolPolicy) AgentOption {
	return func(c *Config) {
		c.AddToolPolicyMatcher(matcher, p)
	}
}

// WithToolResultStore overrides the default in-memory tool-result store.
func WithToolResultStore(s core.ToolResultStore) AgentOption {
	return func(c *Config) {
		c.ToolResultStore = s
		c.ToolResultStoreSet = true
	}
}

// WithToolApproval requires explicit human approval before the named tool runs.
func WithToolApproval(toolName string, opts ...ApprovalOption) AgentOption {
	cfg := &ApprovalConfig{
		ToolName: toolName,
		Prompt: func(call core.ToolCall) string {
			return "Approve call to " + call.Name + "?"
		},
		OnDeny: DenyAskLLMToRevise,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return func(c *Config) {
		c.ToolApprovals = append(c.ToolApprovals, *cfg)
	}
}

// ApprovalPrompt sets a custom prompt builder for WithToolApproval.
func ApprovalPrompt(fn func(call core.ToolCall) string) ApprovalOption {
	return func(c *ApprovalConfig) { c.Prompt = fn }
}

// OnDeny sets the action taken when the human denies approval.
func OnDeny(action DenyAction) ApprovalOption {
	return func(c *ApprovalConfig) { c.OnDeny = action }
}

// nopLogger is a logger that discards all output.
var nopLogger = slog.New(discardHandler{})

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

// BuildConfig applies options and fills in defaults.
func BuildConfig(opts []AgentOption) *Config {
	c := &Config{}
	for _, opt := range opts {
		opt(c)
	}
	if c.Logger == nil {
		c.Logger = nopLogger
	}
	if c.CrossThreadSearch && c.Embedding == nil {
		c.Logger.Warn("memory.WithSemanticRecall without an embedding provider — cross-thread search will be silently disabled")
	}
	if c.MaxParallelDispatch == 0 {
		c.MaxParallelDispatch = 10
	}
	if c.MaxPlanSteps == 0 {
		c.MaxPlanSteps = 50
	}
	if c.MaxToolResultLen == 0 {
		c.MaxToolResultLen = 100_000
	}
	if !c.ToolResultStoreSet {
		c.ToolResultStore = core.NewInMemoryToolResultStore()
	}
	if c.MaxSteps == nil {
		n := 100
		c.MaxSteps = &n
	}
	return c
}

// ---- Input handler ----

// inputHandlerCtxKey is the context key for InputHandler.
type inputHandlerCtxKey struct{}

// WithInputHandlerContext returns a child context carrying the InputHandler.
func WithInputHandlerContext(ctx context.Context, h InputHandler) context.Context {
	return context.WithValue(ctx, inputHandlerCtxKey{}, h)
}

// InputHandlerFromContext retrieves the InputHandler from ctx.
func InputHandlerFromContext(ctx context.Context) (InputHandler, bool) {
	h, ok := ctx.Value(inputHandlerCtxKey{}).(InputHandler)
	return h, ok
}

// ---- Task context propagation ----

// taskCtxKey is the context key for AgentTask.
type taskCtxKey struct{}

// WithTaskContext returns a child context carrying the AgentTask.
func WithTaskContext(ctx context.Context, task AgentTask) context.Context {
	return context.WithValue(ctx, taskCtxKey{}, task)
}

// TaskFromContext retrieves the AgentTask from ctx.
func TaskFromContext(ctx context.Context) (AgentTask, bool) {
	task, ok := ctx.Value(taskCtxKey{}).(AgentTask)
	return task, ok
}

