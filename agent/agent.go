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

// Processors groups the processor-chain hooks fired by the run loop.
// Use with WithProcessors.
type Processors = runtime.Processors

// Hooks groups the mid-iteration callbacks the run loop invokes.
// Use with WithHooks.
type Hooks = runtime.Hooks

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

// ToolPolicyMatcher pairs a name predicate with a policy. Use in
// ToolConfig.PolicyMatchers to attach a policy to a family of tools.
type ToolPolicyMatcher struct {
	Match  func(name string) bool
	Policy core.ToolPolicy
}

// ToolConfig groups the tool subsystem's knobs into one typed sub-config.
// Use WithTools for simple tool registration; use WithToolConfig when you
// need middleware, policies, approval gates, or a custom result store.
//
// Fields are additive: Tools/Middleware/Approvals append, Policies merge,
// PolicyMatchers append, ResultStore replaces.
type ToolConfig struct {
	// Tools to register. Appended to any tools added via WithTools.
	Tools []core.AnyTool

	// Middleware applies to every registered tool at build time.
	// First in the slice is innermost; last is outermost.
	Middleware []core.ToolMiddleware

	// Policies are per-tool timeout/retry policies keyed by exact tool name.
	Policies map[string]core.ToolPolicy

	// PolicyMatchers attach a policy to every tool whose name satisfies the
	// matcher predicate. Evaluated in registration order; first match wins
	// (after exact-name lookup).
	PolicyMatchers []ToolPolicyMatcher

	// Approvals require explicit human approval before the named tool runs.
	// Use the Approval helper for sane defaults.
	Approvals []ApprovalConfig

	// ResultStore overrides the default in-memory tool-result store.
	// A nil ResultStore disables the store explicitly (opt-out).
	ResultStore core.ToolResultStore

	// ResultStoreExplicit must be set to true when ResultStore is nil and
	// you want to explicitly disable the store (rather than accept the
	// default). When false and ResultStore is nil, the default is used.
	ResultStoreExplicit bool
}

// ApplyTo overlays tc's fields onto c.
func (tc ToolConfig) ApplyTo(c *Config) {
	c.Tools = append(c.Tools, tc.Tools...)
	c.ToolMiddleware = append(c.ToolMiddleware, tc.Middleware...)
	for name, p := range tc.Policies {
		if c.ToolPolicies == nil {
			c.ToolPolicies = map[string]core.ToolPolicy{}
		}
		c.ToolPolicies[name] = p
	}
	for _, m := range tc.PolicyMatchers {
		c.AddToolPolicyMatcher(m.Match, m.Policy)
	}
	c.ToolApprovals = append(c.ToolApprovals, tc.Approvals...)
	if tc.ResultStore != nil || tc.ResultStoreExplicit {
		c.ToolResultStore = tc.ResultStore
		c.ToolResultStoreSet = true
	}
}

// WithToolConfig configures the tool subsystem in one option. See ToolConfig.
func WithToolConfig(tc ToolConfig) AgentOption {
	return func(c *Config) { tc.ApplyTo(c) }
}

// Approval is a convenience builder for ApprovalConfig with sane defaults
// (default prompt: "Approve call to <name>?", OnDeny: DenyAskLLMToRevise).
// Use this when populating ToolConfig.Approvals.
func Approval(toolName string, opts ...ApprovalOption) ApprovalConfig {
	cfg := ApprovalConfig{
		ToolName: toolName,
		Prompt: func(call core.ToolCall) string {
			return "Approve call to " + call.Name + "?"
		},
		OnDeny: DenyAskLLMToRevise,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

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

// WithMiddleware applies one or more Middlewares to the agent's outer surface.
// Middlewares wrap the agent's Execute method and see every call, including
// those made by Network when this agent is a child agent.
//
// Middlewares are applied lazily on the first Execute call and the wrapped
// chain is cached — subsequent calls reuse the same wrapper without
// rebuilding. Earlier arguments in mws wrap further out: Chain(a, b) gives
// a wrapping b wrapping the raw agent.
func WithMiddleware(mws ...Middleware) AgentOption {
	return func(c *Config) {
		for _, mw := range mws {
			// Why: Middleware is func(core.Agent) core.Agent; the Config field
			// uses the same underlying type to avoid an import cycle (runtime cannot
			// import agent). Direct append works because named func types are
			// assignable to their underlying type.
			if mw != nil {
				c.AgentMiddleware = append(c.AgentMiddleware, mw)
			}
		}
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
// Replaces (not merges) the agent's current params: nil fields on g clear the
// corresponding agent setting. Pointer fields are deep-copied so subsequent
// caller mutation cannot leak into the agent's state.
func WithGeneration(g Generation) AgentOption {
	cloned := runtime.CloneGeneration(g)
	return func(c *Config) {
		c.GenParams = &cloned
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

// WithoutPromptCaching disables the agent's automatic prompt-cache breakpoint
// placement. By default the loop marks the system message (capturing system
// prompt + tool definitions + loaded history) and the current tail message
// as cache checkpoints on every LLM call, so providers that support ephemeral
// caching (Anthropic, Qwen, OpenAI-compat shims) read from the previous
// iteration's cached prefix instead of re-processing the whole conversation.
//
// Use this when you need byte-exact control over cache markers (e.g. via
// openaicompat.WithCacheControl directly on the provider) or when caching
// is undesirable (privacy-sensitive prompts, A/B variants that must not
// share cache). Providers without ephemeral-cache support are unaffected
// either way.
func WithoutPromptCaching() AgentOption {
	return func(c *Config) { c.DisablePromptCaching = true }
}

// WithMetadata adds key/value pairs to the agent's static metadata map.
// Values are strings — JSON-encode structured data before passing it in.
func WithMetadata(kv map[string]string) AgentOption {
	return func(c *Config) {
		if c.Metadata == nil {
			c.Metadata = make(map[string]string, len(kv))
		}
		for k, v := range kv {
			c.Metadata[k] = v
		}
	}
}

// WithProcessors wires the processor-chain hooks (pre-LLM, post-LLM,
// post-tool) in a single call. Each non-empty slice is appended to the
// existing chain, so multiple WithProcessors calls accumulate per-field.
func WithProcessors(p Processors) AgentOption {
	return func(c *Config) { p.ApplyTo(c) }
}

// WithHooks wires the mid-iteration callbacks (PrepareStep,
// OnIterationComplete, OnError) in a single call. Nil fields leave the
// corresponding hook untouched, so multiple WithHooks calls compose per-field.
func WithHooks(h Hooks) AgentOption {
	return func(c *Config) { h.ApplyTo(c) }
}

// WithInputHandler sets the handler for human-in-the-loop interactions
// (the runtime's built-in ask_user tool delivers questions through it).
// Distinct from WithHooks: this wires a request/response surface, not a
// mid-loop callback.
func WithInputHandler(h InputHandler) AgentOption {
	return func(c *Config) { c.InputHandler = h }
}

// WithEmbedding sets the embedding provider.
func WithEmbedding(e core.EmbeddingProvider) AgentOption {
	return func(c *Config) { c.Embedding = e }
}

// WithMemory configures the agent's memory system.
//
// The full memory configuration is stored on Config.MemoryConfig (consumed by
// runtime.Init). A small set of flat Config fields are also written here for
// hooks that read them outside the memory pipeline:
//   - CrossThreadSearch: BuildConfig warns when set without an embedding provider
//   - Compactor: read by runtime.Init's history-compaction wiring
//   - CompressModel/CompressThreshold: read by per-turn compression in the loop
//   - CompactThreshold: surfaced in agent assertions/inspection
//   - Embedding: per-call retrieval fallback
func WithMemory(opts ...memory.Option) AgentOption {
	return func(c *Config) {
		cfg := memory.BuildConfig(opts...)
		c.MemoryConfig = cfg
		c.MemoryInitialized = true
		c.Tools = append(c.Tools, cfg.Tools...)

		c.CrossThreadSearch = cfg.SemanticRecall
		c.Compactor = cfg.Compactor
		c.CompactThreshold = cfg.CompactThreshold
		c.CompressModel = cfg.CompressModel
		c.CompressThreshold = cfg.CompressThreshold
		if cfg.Embedding != nil {
			c.Embedding = cfg.Embedding
		}
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
var nopLogger = slog.New(slog.DiscardHandler)

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

