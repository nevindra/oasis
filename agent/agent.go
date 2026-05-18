package agent

import (
	"context"
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

// agentConfig holds shared configuration for LLMAgent and Network.
type agentConfig struct {
	tools            []AnyTool
	Agents           []Agent // exported for network subpackage
	prompt           string
	maxIter           int
	preProcessors      []PreProcessor
	postProcessors     []PostProcessor
	postToolProcessors []PostToolProcessor
	inputHandler     InputHandler
	store            Store
	embedding        EmbeddingProvider
	memory           MemoryStore
	crossThreadSearch bool    // enabled by history.CrossThreadSearch
	semanticMinScore  float32 // set by history.MinScore
	maxHistory        int     // set by history.MaxHistory
	maxTokens         int     // set by history.MaxTokens (history budget)
	autoTitle         bool    // set by history.AutoTitle
	planExecution     bool            // enabled by WithPlanExecution option
	Sandbox           any             // set by WithSandbox option; holds a sandbox.Sandbox (exported for network subpackage)
	SandboxTools      []AnyTool       // tools auto-registered by WithSandbox (exported for network subpackage)
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
	SkillProvider  SkillProvider   // exported for network subpackage; set by WithSkills
}

// AgentOption configures an LLMAgent or Network.
type AgentOption func(*agentConfig)

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

// WithTools adds tools to the agent or network.
func WithTools(tools ...AnyTool) AgentOption {
	return func(c *agentConfig) { c.tools = append(c.tools, tools...) }
}

// WithPrompt sets the system prompt for the agent or network router.
func WithPrompt(s string) AgentOption {
	return func(c *agentConfig) { c.prompt = s }
}

// WithMaxIter sets the maximum tool-calling iterations.
func WithMaxIter(n int) AgentOption {
	return func(c *agentConfig) { c.maxIter = n }
}

// WithMaxAttachmentBytes sets the maximum total bytes of attachments
// accumulated from tool results during the execution loop. Default is 50 MB.
// Zero means use the default.
func WithMaxAttachmentBytes(n int64) AgentOption {
	return func(c *agentConfig) { c.maxAttachmentBytes = n }
}

// WithSuspendBudget sets per-agent limits on concurrent suspended snapshots.
// maxSnapshots caps the number of active suspensions. maxBytes caps total
// estimated memory held by snapshot closures. Defaults: 20 snapshots, 256 MB.
// When either limit is exceeded, new suspensions are rejected (the underlying
// processor error is returned instead of ErrSuspended).
func WithSuspendBudget(maxSnapshots int, maxBytes int64) AgentOption {
	return func(c *agentConfig) {
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
	return func(c *agentConfig) {
		cfg := history.Build(opts)
		c.store = cfg.Store
		c.maxHistory = cfg.MaxHistory
		c.maxTokens = cfg.MaxTokens
		c.autoTitle = cfg.AutoTitle
		c.crossThreadSearch = cfg.CrossThreadSearch
		if cfg.Embedding != nil {
			c.embedding = cfg.Embedding
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
	return func(c *agentConfig) {
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
	return func(c *agentConfig) { c.Agents = append(c.Agents, agents...) }
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
	return func(c *agentConfig) { c.planExecution = true }
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
func WithSandbox(sb any, tools ...AnyTool) AgentOption {
	return func(c *agentConfig) {
		c.Sandbox = sb
		c.SandboxTools = tools
	}
}

// SubAgentOption configures spawn_agent behavior.
// Scoped type — only accepted by WithSubAgentSpawning.
type SubAgentOption func(*agentConfig)

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
	return func(c *agentConfig) {
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
	return func(c *agentConfig) { c.maxSpawnDepth = n }
}

// DenySpawnTools prevents specific tools from being inherited by sub-agents.
// Tool names are matched exactly against ToolDefinition.Name.
// Multiple calls accumulate (append, not replace).
// ask_user is always blocked in sub-agents regardless of this setting.
func DenySpawnTools(names ...string) SubAgentOption {
	return func(c *agentConfig) { c.denySpawnTools = append(c.denySpawnTools, names...) }
}

// WithActiveSkills pre-activates skills whose instructions are appended to
// the agent's system prompt on every LLM call. Use for capabilities that
// should always be available. References are NOT auto-resolved here — call
// ActivateWithReferences before passing skills if needed.
func WithActiveSkills(skills ...Skill) AgentOption {
	return func(c *agentConfig) { c.activeSkills = append(c.activeSkills, skills...) }
}

// WithSkills registers a SkillProvider and automatically adds skill_discover
// and skill_activate tools so the agent can discover and activate skills at
// runtime. If the provider also implements SkillWriter, skill_create and
// skill_update tools are added too.
func WithSkills(p SkillProvider) AgentOption {
	return func(c *agentConfig) { c.SkillProvider = p }
}

// WithResponseSchema sets the response schema for structured JSON output.
// When set, the provider enforces structured output matching the schema.
// Providers translate this to their native mechanism (e.g. Gemini responseSchema,
// OpenAI response_format).
func WithResponseSchema(s *ResponseSchema) AgentOption {
	return func(c *agentConfig) { c.responseSchema = s }
}

// WithDynamicPrompt sets a per-request prompt resolution function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value is used as the system prompt for that execution.
// Overrides WithPrompt when set. If the function returns "", no system prompt
// is used (same as omitting WithPrompt).
func WithDynamicPrompt(fn PromptFunc) AgentOption {
	return func(c *agentConfig) { c.dynamicPrompt = fn }
}

// WithDynamicModel sets a per-request model selection function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value is used as the LLM provider for that execution.
// Overrides the construction-time provider when set.
func WithDynamicModel(fn ModelFunc) AgentOption {
	return func(c *agentConfig) { c.dynamicModel = fn }
}

// WithDynamicTools sets a per-request tool selection function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value REPLACES the construction-time tools for that
// execution. To remove all tools for a request, return nil or an empty slice.
func WithDynamicTools(fn ToolsFunc) AgentOption {
	return func(c *agentConfig) { c.dynamicTools = fn }
}

// WithTracer sets the tracer for the agent. When set, the agent emits
// spans for execution, memory, and loop operations. Use observer.NewTracer()
// for an OTEL-backed implementation.
func WithTracer(t Tracer) AgentOption {
	return func(c *agentConfig) { c.tracer = t }
}

// WithLogger sets the structured logger for the agent. When set, replaces
// all log.Printf calls with structured slog output. If not set, a no-op
// logger is used (no output).
func WithLogger(l *slog.Logger) AgentOption {
	return func(c *agentConfig) { c.logger = l }
}

// WithPreProcessors registers PreProcessor hooks that run before each LLM call.
func WithPreProcessors(processors ...PreProcessor) AgentOption {
	return func(c *agentConfig) { c.preProcessors = append(c.preProcessors, processors...) }
}

// WithPostProcessors registers PostProcessor hooks that run after each LLM response.
func WithPostProcessors(processors ...PostProcessor) AgentOption {
	return func(c *agentConfig) { c.postProcessors = append(c.postProcessors, processors...) }
}

// WithPostToolProcessors registers PostToolProcessor hooks that run after each tool result.
func WithPostToolProcessors(processors ...PostToolProcessor) AgentOption {
	return func(c *agentConfig) { c.postToolProcessors = append(c.postToolProcessors, processors...) }
}

// WithInputHandler sets the handler for human-in-the-loop interactions.
// When set, the agent gains an "ask_user" tool (LLM-driven) and processors
// can access the handler via InputHandlerFromContext(ctx).
func WithInputHandler(h InputHandler) AgentOption {
	return func(c *agentConfig) { c.inputHandler = h }
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
	return func(c *agentConfig) {
		c.memory = m
		c.embedding = e
	}
}

// nopLogger is a logger that discards all output. Used when WithLogger is not set.
var nopLogger = slog.New(discardHandler{})

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler            { return d }

func BuildConfig(opts []AgentOption) agentConfig {
	var c agentConfig
	for _, opt := range opts {
		opt(&c)
	}
	if c.logger == nil {
		c.logger = nopLogger
	}
	// Warn about misconfigurations that can't be caught at compile time.
	if c.memory != nil && c.store == nil {
		c.logger.Warn("WithUserMemory without history.Store — fact extraction (write) will be silently skipped")
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
