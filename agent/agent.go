package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/history"
)

// Agent, StreamingAgent, AgentTask, AgentResult, StepTrace are defined in core/
// and re-exported here as aliases for backward compatibility.
type Agent = core.Agent
type StreamingAgent = core.StreamingAgent
type AgentTask = core.AgentTask
type AgentResult = core.AgentResult
type StepTrace = core.StepTrace

// Config holds shared configuration for LLMAgent and Network.
// Fields are unexported; use accessor methods (e.g. Agents()) for out-of-package reads.
type Config struct {
	tools            []AnyTool
	agents           []Agent
	prompt           string
	maxIter           int
	preProcessors      []PreProcessor
	postProcessors     []PostProcessor
	postToolProcessors []PostToolProcessor
	inputHandler     InputHandler
	store            Store
	embedding        EmbeddingProvider
	memory           MemoryStore
	crossThreadSearch    bool    // enabled by history.CrossThreadSearch
	semanticMinScore     float32 // set by history.MinScore
	maxHistory           int     // set by history.MaxHistory
	maxTokens            int     // set by history.MaxTokens (history budget)
	autoTitle            bool    // set by history.AutoTitle
	memoryEmbedding      EmbeddingProvider // set only by WithUserMemory; used to detect provider conflicts
	crossThreadEmbedding EmbeddingProvider // set only by history.CrossThreadSearch; used to detect provider conflicts
	planExecution     bool            // enabled by WithPlanExecution option
	sandbox           core.Sandbox    // set by WithSandbox option
	sandboxTools      []AnyTool       // tools auto-registered by WithSandbox
	responseSchema    *ResponseSchema // set by WithResponseSchema option
	dynamicPrompt     PromptFunc      // set by WithDynamicPrompt option
	dynamicModel      ModelFunc       // set by WithDynamicModel option
	dynamicTools       ToolsFunc       // set by WithDynamicTools option
	tracer             Tracer          // set by WithTracer option
	logger             *slog.Logger    // set by WithLogger option
	maxAttachmentBytes  int64          // set by WithMaxAttachmentBytes option
	maxSuspendSnapshots int            // set by WithSuspendBudget
	maxSuspendBytes     int64          // set by WithSuspendBudget
	compressModel       ModelFunc          // set by history.Compress
	compressThreshold   int                // set by history.Compress
	compactor           Compactor          // set by history.Compaction (per-thread compaction)
	compactThreshold    float64            // set by history.Compaction (0 = disabled)
	generationParams    *GenerationParams  // set by WithGeneration
	semanticTrimming    bool               // enabled by history.SemanticTrim
	trimmingEmbedding   EmbeddingProvider  // set by history.SemanticTrim
	keepRecent          int                // set by history.KeepRecent
	spawnEnabled   bool     // set by WithSubAgentSpawning
	maxSpawnDepth  int      // set by MaxSpawnDepth (default 1)
	denySpawnTools []string // set by DenySpawnTools
	activeSkills   []Skill        // set by WithActiveSkills
	skillProvider  SkillProvider  // set by WithSkills

	// Hook fields — set via WithPrepareStep / WithOnIterationComplete / WithOnError.
	prepareStep         PrepareStep
	onIterationComplete OnIterationComplete
	onError             OnError

	// toolMiddleware is applied to every registered tool at agent build time.
	// First in slice = innermost; last = outermost. Empty = no overhead.
	toolMiddleware []core.ToolMiddleware

	// Per-tool retry/timeout policy. toolPolicies are exact-name entries
	// (ServeMux-style; later registrations overwrite). toolPolicyMatchers
	// is an ordered list scanned in registration order; first matcher
	// whose predicate returns true wins. Exact matches always beat
	// matchers (mirrors net/http.ServeMux).
	toolPolicies       map[string]core.ToolPolicy
	toolPolicyMatchers []toolPolicyMatcher

	// Configurable runtime limits (defaults applied in BuildConfig).
	maxParallelDispatch int // set by WithMaxParallelDispatch; default 10
	maxPlanSteps        int // set by WithMaxPlanSteps; default 50
	maxToolResultLen    int // set by WithMaxToolResultLen; default 100_000

	// Tool result paging store (set by WithToolResultStore; default in-memory).
	toolResultStore    core.ToolResultStore
	toolResultStoreSet bool // distinguishes "default" from "explicitly nil"

	// maxSteps: nil = unset (default 100), &0 = unbounded, &n = cap at n.
	maxSteps *int

	// metadata is shallow-merged with RunOptions.Metadata at run time.
	metadata map[string]any
}

// Agents returns the subagents registered via WithAgents.
// Called by NewNetwork at construction time to populate its agent map.
func (c *Config) Agents() []core.Agent { return c.agents }

// AgentOption configures an LLMAgent or Network.
type AgentOption func(*Config)

// PromptFunc, ToolsFunc, and ModelFunc share the same func(ctx, task) T shape.
// A generic ResolveFunc[T] was considered and rejected: the named types provide
// domain clarity at call sites (PromptFunc vs ResolveFunc[string]) and better
// Godoc discoverability. Three stable, self-documenting types beat one generic.

// PromptFunc resolves the system prompt per-request.
// When set via WithDynamicPrompt, it is called at the start of every
// Execute/ExecuteStream call. The returned string replaces the static
// WithPrompt value for that execution.
type PromptFunc func(ctx context.Context, task AgentTask) string

// ToolsFunc resolves the tool set per-request.
// When set via WithDynamicTools, it is called at the start of every
// Execute/ExecuteStream call. The returned tools REPLACE (not append to)
// the construction-time tools for that execution.
type ToolsFunc func(ctx context.Context, task AgentTask) []AnyTool

// toolPolicyMatcher pairs a name predicate with a policy for use by
// WithToolPolicyMatch.
type toolPolicyMatcher struct {
	match  func(name string) bool
	policy core.ToolPolicy
}

// WithTools adds tools to the agent or network.
func WithTools(tools ...AnyTool) AgentOption {
	return func(c *Config) { c.tools = append(c.tools, tools...) }
}

// WithToolMiddleware registers a chain of tool middlewares applied to every
// tool at build time. First in mws is innermost (closest to the tool); last
// is outermost. Pass-through for empty input.
//
// Order from innermost to outermost in the final wrapping is:
//
//	[tool] -> [user middleware in order] -> [tool policy: retry+timeout] -> [approval] -> dispatch
//
// User middleware sits inside ToolPolicy so retries see one middleware
// invocation per attempt — each retry is a real attempt.
func WithToolMiddleware(mws ...core.ToolMiddleware) AgentOption {
	return func(c *Config) {
		c.toolMiddleware = append(c.toolMiddleware, mws...)
	}
}

// WithPrompt sets the system prompt for the agent or network router.
func WithPrompt(s string) AgentOption {
	return func(c *Config) { c.prompt = s }
}

// WithMaxIter sets the maximum tool-calling iterations.
func WithMaxIter(n int) AgentOption {
	return func(c *Config) { c.maxIter = n }
}

// WithMaxAttachmentBytes sets the maximum total bytes of attachments
// accumulated from tool results during the execution loop. Default is 50 MB.
// Zero means use the default.
func WithMaxAttachmentBytes(n int64) AgentOption {
	return func(c *Config) { c.maxAttachmentBytes = n }
}

// WithSuspendBudget sets per-agent limits on concurrent suspended snapshots.
// maxSnapshots caps the number of active suspensions. maxBytes caps total
// estimated memory held by snapshot closures. Defaults: 20 snapshots, 256 MB.
// When either limit is exceeded, new suspensions are rejected (the underlying
// processor error is returned instead of ErrSuspended).
func WithSuspendBudget(maxSnapshots int, maxBytes int64) AgentOption {
	return func(c *Config) {
		c.maxSuspendSnapshots = maxSnapshots
		c.maxSuspendBytes = maxBytes
	}
}

// --- History ---

// WithHistory enables conversation history and related context-window
// management strategies. Pass a combination of history.Option values:
//
//	oasis.WithHistory(
//	    history.Store(store),
//	    history.MaxHistory(30),
//	    history.CrossThreadSearch(embedding),
//	    history.Compaction(c, 0.8),
//	    history.Compress(model, 200_000),
//	)
//
// Without history.Store, only per-turn options (Compress) take effect;
// per-thread mechanisms (Compaction, SemanticTrim, AutoTitle,
// CrossThreadSearch) silently no-op.
func WithHistory(opts ...history.Option) AgentOption {
	return func(c *Config) {
		cfg := history.Build(opts)
		c.store = cfg.Store
		c.maxHistory = cfg.MaxHistory
		c.maxTokens = cfg.MaxTokens
		c.autoTitle = cfg.AutoTitle
		c.crossThreadSearch = cfg.CrossThreadSearch
		if cfg.Embedding != nil {
			c.embedding = cfg.Embedding
			c.crossThreadEmbedding = cfg.Embedding // tracked separately for conflict detection
		}
		c.semanticMinScore = cfg.MinScore
		c.compactor = cfg.Compactor
		c.compactThreshold = cfg.CompactThreshold
		c.semanticTrimming = cfg.SemanticTrimming
		if cfg.TrimmingEmbedding != nil {
			c.trimmingEmbedding = cfg.TrimmingEmbedding
		}
		c.keepRecent = cfg.KeepRecent
		c.compressModel = cfg.CompressModel
		c.compressThreshold = cfg.CompressThreshold
	}
}

// --- Generation ---

// Generation groups the LLM sampling and output parameters. Pass to
// WithGeneration. Pointer fields are optional — nil means "use provider default".
type Generation struct {
	Temperature *float64
	TopP        *float64
	TopK        *int
	MaxTokens   *int
}

// WithGeneration sets LLM sampling and output parameters in one call,
// replacing the previous per-knob options.
//
//	oasis.WithGeneration(oasis.Generation{
//	    Temperature: oasis.Ptr(0.5),
//	    TopP:        oasis.Ptr(0.9),
//	    TopK:        oasis.Ptr(40),
//	    MaxTokens:   oasis.Ptr(1024),
//	})
func WithGeneration(g Generation) AgentOption {
	return func(c *Config) {
		if c.generationParams == nil {
			c.generationParams = &GenerationParams{}
		}
		c.generationParams.Temperature = g.Temperature
		c.generationParams.TopP = g.TopP
		c.generationParams.TopK = g.TopK
		c.generationParams.MaxTokens = g.MaxTokens
	}
}

// WithAgents adds subagents to a Network. Ignored by LLMAgent.
func WithAgents(agents ...Agent) AgentOption {
	return func(c *Config) { c.agents = append(c.agents, agents...) }
}

// WithPlanExecution enables the built-in "execute_plan" tool that batches
// multiple tool calls in a single LLM turn. The LLM can call execute_plan
// with an array of steps (each specifying a tool name and arguments), and
// the framework executes all steps in parallel without re-sampling the LLM
// between each call. Returns structured per-step results.
//
// This reduces latency and token usage for fan-out patterns where the LLM
// needs to call the same or different tools multiple times with known inputs.
func WithPlanExecution() AgentOption {
	return func(c *Config) { c.planExecution = true }
}

// WithSandbox attaches a sandbox environment to the agent. Pass the sandbox
// tools generated by sandbox.Tools(sb). The framework auto-registers them
// (shell, execute_code, file_read, file_write, file_edit, file_glob,
// file_grep, browser, screenshot, snapshot, page_text, export_pdf, mcp_call).
//
// Usage:
//
//	sb, _ := mgr.Create(ctx, sandbox.CreateOpts{SessionID: "s1"})
//	agent := oasis.NewLLMAgent("a", "d", provider, oasis.WithSandbox(sb, sandbox.Tools(sb)...))
func WithSandbox(sb core.Sandbox, tools ...AnyTool) AgentOption {
	return func(c *Config) {
		c.sandbox = sb
		c.sandboxTools = tools
	}
}

// SubAgentOption configures spawn_agent behavior.
// Scoped type — only accepted by WithSubAgentSpawning.
type SubAgentOption func(*Config)

// WithSubAgentSpawning enables the built-in spawn_agent tool.
// When enabled, the LLM can dynamically create ephemeral child agents
// that inherit the parent's provider and tools. Sub-agents do not
// inherit conversation memory, user memory, store, or processors.
//
// Optional SubAgentOption values configure spawn constraints:
//
//	oasis.WithSubAgentSpawning()                                       // defaults
//	oasis.WithSubAgentSpawning(oasis.MaxSpawnDepth(2))                 // allow recursive spawning
//	oasis.WithSubAgentSpawning(oasis.DenySpawnTools("shell_exec"))     // restrict tools
func WithSubAgentSpawning(opts ...SubAgentOption) AgentOption {
	return func(c *Config) {
		c.spawnEnabled = true
		c.maxSpawnDepth = 1
		for _, o := range opts {
			o(c)
		}
	}
}

// MaxSpawnDepth sets the maximum sub-agent nesting depth.
// Default: 1 (parent can spawn, children cannot).
// A depth of 2 means sub-agents can spawn their own sub-agents once.
func MaxSpawnDepth(n int) SubAgentOption {
	return func(c *Config) { c.maxSpawnDepth = n }
}

// DenySpawnTools prevents specific tools from being inherited by sub-agents.
// Tool names are matched exactly against ToolDefinition.Name.
// Multiple calls accumulate (append, not replace).
// ask_user is always blocked in sub-agents regardless of this setting.
func DenySpawnTools(names ...string) SubAgentOption {
	return func(c *Config) { c.denySpawnTools = append(c.denySpawnTools, names...) }
}

// WithActiveSkills pre-activates skills whose instructions are appended to
// the agent's system prompt on every LLM call. Use for capabilities that
// should always be available. References are NOT auto-resolved here — call
// ActivateWithReferences before passing skills if needed.
func WithActiveSkills(skills ...Skill) AgentOption {
	return func(c *Config) { c.activeSkills = append(c.activeSkills, skills...) }
}

// WithSkills registers a SkillProvider and automatically adds skill_discover
// and skill_activate tools so the agent can discover and activate skills at
// runtime. If the provider also implements SkillWriter, skill_create and
// skill_update tools are added too.
func WithSkills(p SkillProvider) AgentOption {
	return func(c *Config) { c.skillProvider = p }
}

// WithResponseSchema sets the response schema for structured JSON output.
// When set, the provider enforces structured output matching the schema.
// Providers translate this to their native mechanism (e.g. Gemini responseSchema,
// OpenAI response_format).
func WithResponseSchema(s *ResponseSchema) AgentOption {
	return func(c *Config) { c.responseSchema = s }
}

// WithDynamicPrompt sets a per-request prompt resolution function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value is used as the system prompt for that execution.
// Overrides WithPrompt when set. If the function returns "", no system prompt
// is used (same as omitting WithPrompt).
func WithDynamicPrompt(fn PromptFunc) AgentOption {
	return func(c *Config) { c.dynamicPrompt = fn }
}

// WithDynamicModel sets a per-request model selection function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value is used as the LLM provider for that execution.
// Overrides the construction-time provider when set.
func WithDynamicModel(fn ModelFunc) AgentOption {
	return func(c *Config) { c.dynamicModel = fn }
}

// WithDynamicTools sets a per-request tool selection function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value REPLACES the construction-time tools for that
// execution. To remove all tools for a request, return nil or an empty slice.
func WithDynamicTools(fn ToolsFunc) AgentOption {
	return func(c *Config) { c.dynamicTools = fn }
}

// WithTracer sets the tracer for the agent. When set, the agent emits
// spans for execution, memory, and loop operations. Use observer.NewTracer()
// for an OTEL-backed implementation.
func WithTracer(t Tracer) AgentOption {
	return func(c *Config) { c.tracer = t }
}

// WithLogger sets the structured logger for the agent. When set, replaces
// all log.Printf calls with structured slog output. If not set, a no-op
// logger is used (no output).
func WithLogger(l *slog.Logger) AgentOption {
	return func(c *Config) { c.logger = l }
}

// WithMetadata adds key/value pairs to the agent's static metadata map.
// Metadata flows into StepTrace, structured logs, and is available to
// hooks. Per-call RunOptions.Metadata shallow-merges over this map
// (call-site keys win on conflict).
func WithMetadata(kv map[string]any) AgentOption {
	return func(c *Config) {
		if c.metadata == nil {
			c.metadata = make(map[string]any, len(kv))
		}
		for k, v := range kv {
			c.metadata[k] = v
		}
	}
}

// WithPreProcessors registers PreProcessor hooks that run before each LLM call.
func WithPreProcessors(processors ...PreProcessor) AgentOption {
	return func(c *Config) { c.preProcessors = append(c.preProcessors, processors...) }
}

// WithPostProcessors registers PostProcessor hooks that run after each LLM response.
func WithPostProcessors(processors ...PostProcessor) AgentOption {
	return func(c *Config) { c.postProcessors = append(c.postProcessors, processors...) }
}

// WithPostToolProcessors registers PostToolProcessor hooks that run after each tool result.
func WithPostToolProcessors(processors ...PostToolProcessor) AgentOption {
	return func(c *Config) { c.postToolProcessors = append(c.postToolProcessors, processors...) }
}

// WithInputHandler sets the handler for human-in-the-loop interactions.
// When set, the agent gains an "ask_user" tool (LLM-driven) and processors
// can access the handler via InputHandlerFromContext(ctx).
func WithInputHandler(h InputHandler) AgentOption {
	return func(c *Config) { c.inputHandler = h }
}

// WithPrepareStep registers a PrepareStep hook that runs before every LLM call
// in the agent loop, including retries. Use to mutate the request, swap the
// model, or override the tool set for individual iterations.
//
// If a PrepareStep is set on both the Config (via this option) and the
// RunOptions for a single call, the RunOptions hook wins entirely
// (no chaining).
func WithPrepareStep(fn PrepareStep) AgentOption {
	return func(c *Config) { c.prepareStep = fn }
}

// WithOnIterationComplete registers an OnIterationComplete hook that runs
// after each loop iteration's LLM response, tool dispatch, and post-tool
// processor chain. The hook returns an IterationDecision controlling
// what the loop does next: Continue, Stop, InjectFeedback, InjectMessages.
func WithOnIterationComplete(fn OnIterationComplete) AgentOption {
	return func(c *Config) { c.onIterationComplete = fn }
}

// WithOnError registers an OnError hook for mid-loop error recovery.
// The hook returns an ErrorDecision: Propagate, Retry, RetryWithFeedback,
// or HaltDecision. *ErrHalt, *ErrSuspended, and context cancellation
// bypass this hook.
func WithOnError(fn OnError) AgentOption {
	return func(c *Config) { c.onError = fn }
}

// WithUserMemory enables the full user memory pipeline: read + write.
//
// Read (every Execute call): embeds the input, retrieves relevant facts via
// BuildContext, and appends them to the system prompt.
//
// Write (after each turn, background): uses the agent's own LLM to extract
// durable user facts from the conversation exchange and persists them via
// UpsertFact. Write requires WithHistory(history.Store(...)) — without it,
// extraction is silently skipped (logged as a warning at construction time).
func WithUserMemory(m MemoryStore, e EmbeddingProvider) AgentOption {
	return func(c *Config) {
		c.memory = m
		c.embedding = e
		c.memoryEmbedding = e // kept separately for conflict detection in BuildConfig
	}
}

// WithMaxParallelDispatch caps the number of concurrent tool call goroutines.
// Default is 10. Set higher when tools are I/O-bound and can tolerate fan-out.
func WithMaxParallelDispatch(n int) AgentOption {
	return func(c *Config) {
		if n > 0 {
			c.maxParallelDispatch = n
		}
	}
}

// WithMaxPlanSteps caps the number of steps in a single execute_plan call.
// Default is 50. The LLM gets an error if it submits a plan with more steps.
func WithMaxPlanSteps(n int) AgentOption {
	return func(c *Config) {
		if n > 0 {
			c.maxPlanSteps = n
		}
	}
}

// WithMaxToolResultLen sets the inline budget for tool results in the
// conversation history (in runes). Results larger than this are truncated with
// a paging marker. Default is 100_000 runes (~25K tokens).
func WithMaxToolResultLen(n int) AgentOption {
	return func(c *Config) {
		if n > 0 {
			c.maxToolResultLen = n
		}
	}
}

// WithToolPolicy attaches a per-tool timeout and retry policy to the tool
// registered under the exact name. Re-registering the same name overwrites
// the prior entry (last-call-wins). Exact names take precedence over any
// matcher registered via WithToolPolicyMatch. Streaming tools (those
// implementing core.StreamingAnyTool) silently bypass policy wrapping.
func WithToolPolicy(name string, p core.ToolPolicy) AgentOption {
	return func(c *Config) {
		if c.toolPolicies == nil {
			c.toolPolicies = map[string]core.ToolPolicy{}
		}
		c.toolPolicies[name] = p
	}
}

// WithToolPolicyMatch attaches a policy to every tool whose name satisfies
// the matcher predicate. Matchers are scanned in registration order; the
// first matcher whose predicate returns true wins. Useful for applying a
// single policy to MCP tool families (e.g. names prefixed with mcp__).
// Exact-name entries from WithToolPolicy always take precedence.
func WithToolPolicyMatch(matcher func(name string) bool, p core.ToolPolicy) AgentOption {
	return func(c *Config) {
		c.toolPolicyMatchers = append(c.toolPolicyMatchers, toolPolicyMatcher{match: matcher, policy: p})
	}
}

// WithToolResultStore overrides the default in-memory tool-result store.
// Pass nil to disable result paging entirely (oversize results get the
// legacy truncation marker with no id; the read_full_result tool is not
// registered).
func WithToolResultStore(s core.ToolResultStore) AgentOption {
	return func(c *Config) {
		c.toolResultStore = s
		c.toolResultStoreSet = true
	}
}

// WithMaxSteps caps the number of StepTrace entries kept in AgentResult.Steps.
// When the cap is exceeded the oldest entry is dropped (most-recent-N semantics).
// WithMaxSteps(0) means unbounded. Default when not set: 100.
func WithMaxSteps(n int) AgentOption {
	return func(c *Config) { c.maxSteps = &n }
}

// nopLogger is a logger that discards all output. Used when WithLogger is not set.
var nopLogger = slog.New(discardHandler{})

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler            { return d }

func BuildConfig(opts []AgentOption) *Config {
	c := &Config{}
	for _, opt := range opts {
		opt(c)
	}
	if c.logger == nil {
		c.logger = nopLogger
	}
	// Warn about misconfigurations that can't be caught at compile time.
	if c.memory != nil && c.store == nil {
		c.logger.Warn("WithUserMemory without history.Store — fact extraction (write) will be silently skipped")
	}
	// Apply defaults for configurable runtime limits.
	if c.maxParallelDispatch == 0 {
		c.maxParallelDispatch = 10
	}
	if c.maxPlanSteps == 0 {
		c.maxPlanSteps = 50
	}
	if c.maxToolResultLen == 0 {
		c.maxToolResultLen = 100_000
	}
	// Default to an in-memory store unless the caller explicitly passed nil.
	if !c.toolResultStoreSet {
		c.toolResultStore = core.NewInMemoryToolResultStore()
	}
	// Default maxSteps to 100 when not explicitly set.
	if c.maxSteps == nil {
		n := 100
		c.maxSteps = &n
	}
	// Conflict: WithUserMemory and history.CrossThreadSearch configured with
	// different embedding provider instances. Both write to c.embedding; the
	// last-writer-wins silently, which produces incorrect recall. Use panic
	// instead of error-returning because NewLLMAgent's signature would otherwise
	// need a breaking change beyond the scope of Phase 2. Misconfigured embedding
	// providers are a developer-time error, not a runtime condition.
	if c.memoryEmbedding != nil && c.crossThreadEmbedding != nil && c.memoryEmbedding != c.crossThreadEmbedding {
		panic(fmt.Sprintf(
			"oasis: conflicting embedding providers — WithUserMemory uses %T, "+
				"history.CrossThreadSearch uses %T; use the same provider for both, or pick one",
			c.memoryEmbedding, c.crossThreadEmbedding))
	}
	return c
}

// --- Input handler (human-in-the-loop) ---

// InputRequest describes what the agent needs from the human.
type InputRequest struct {
	// Question is the natural language prompt shown to the human.
	Question string
	// Options provides suggested choices. Empty = free-form input.
	Options []string
	// Metadata carries context for the handler (agent name, tool being approved, etc).
	Metadata map[string]string
}

// InputResponse is the human's reply.
type InputResponse struct {
	// Value is the human's text response.
	Value string
}

// InputHandler delivers questions to a human and returns their response.
// Implementations bridge to the actual communication channel (Telegram, CLI, HTTP, etc).
// Must block until a response is received or ctx is cancelled.
type InputHandler interface {
	RequestInput(ctx context.Context, req InputRequest) (InputResponse, error)
}

// inputHandlerCtxKey is the context key for InputHandler.
type inputHandlerCtxKey struct{}

// WithInputHandlerContext returns a child context carrying the InputHandler.
func WithInputHandlerContext(ctx context.Context, h InputHandler) context.Context {
	return context.WithValue(ctx, inputHandlerCtxKey{}, h)
}

// InputHandlerFromContext retrieves the InputHandler from ctx.
// Returns nil, false if no handler is set.
func InputHandlerFromContext(ctx context.Context) (InputHandler, bool) {
	h, ok := ctx.Value(inputHandlerCtxKey{}).(InputHandler)
	return h, ok
}

// --- Task context propagation ---

// taskCtxKey is the context key for AgentTask.
type taskCtxKey struct{}

// WithTaskContext returns a child context carrying the AgentTask.
// Called automatically by LLMAgent and Network at Execute entry points.
// Tools and processors can retrieve the task via TaskFromContext.
func WithTaskContext(ctx context.Context, task AgentTask) context.Context {
	return context.WithValue(ctx, taskCtxKey{}, task)
}

// TaskFromContext retrieves the AgentTask from ctx.
// Returns the task and true if present, or zero AgentTask and false if not.
// Use this in AnyTool.ExecuteRaw to access task metadata (user ID, thread ID, etc.)
// without changing the AnyTool interface.
func TaskFromContext(ctx context.Context) (AgentTask, bool) {
	task, ok := ctx.Value(taskCtxKey{}).(AgentTask)
	return task, ok
}
