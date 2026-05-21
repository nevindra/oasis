package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nevindra/oasis/core"
)

// LLMAgent is an Agent that uses an LLM with tools to complete tasks.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type LLMAgent struct {
	AgentCore
}

// NewLLMAgent creates an LLMAgent with the given provider and options.
func NewLLMAgent(name, description string, provider Provider, opts ...AgentOption) *LLMAgent {
	cfg := BuildConfig(opts)
	a := &LLMAgent{}
	InitCore(&a.AgentCore, name, description, provider, cfg)

	// Auto-register read_full_result when a store is configured.
	if cfg.toolResultStore != nil {
		a.tools.Add(NewReadFullResultTool(cfg.toolResultStore))
	}

	// Pre-compute tool definitions for the non-dynamic path.
	// Avoids rebuilding the slice on every Execute call.
	if a.dynamicTools == nil {
		a.cachedToolDefs = a.CacheBuiltinToolDefs(a.tools.AllDefinitions())
	}

	return a
}

// Execute runs the tool-calling loop until the LLM produces a final text response.
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return a.ExecuteWithSpan(ctx, task, nil, "LLMAgent", "agent", a.buildLoopConfig)
}

// ExecuteStream runs the tool-calling loop like Execute, but emits StreamEvent
// values into ch throughout execution. Events include text deltas during the
// final LLM response and tool call start/result during tool iterations.
// The channel is closed when streaming completes.
func (a *LLMAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return a.ExecuteWithSpan(ctx, task, ch, "LLMAgent", "agent", a.buildLoopConfig)
}

// buildLoopConfig wires LLMAgent fields into a LoopConfig for runLoop.
// Resolves dynamic prompt, model, and tools when configured.
func (a *LLMAgent) buildLoopConfig(ctx context.Context, task AgentTask, ch chan<- StreamEvent) LoopConfig {
	prompt, provider := a.ResolvePromptAndProvider(ctx, task)
	if a.activeSkillInstructions != "" {
		prompt = prompt + "\n\n# Active Skills\n\n" + a.activeSkillInstructions
	}

	// Resolve tools: dynamic replaces static.
	var toolDefs []ToolDefinition
	var executeTool ToolExecFunc
	var executeToolStream ToolExecStreamFunc
	if dynDefs, dynExec := a.ResolveDynamicTools(ctx, task); dynDefs != nil {
		a.logger.Debug("using dynamic tools", "agent", a.name, "tool_count", len(dynDefs))
		toolDefs = a.CacheBuiltinToolDefs(dynDefs)
		executeTool = dynExec
	} else {
		toolDefs = a.cachedToolDefs
		executeTool = a.tools.Execute
		executeToolStream = a.tools.ExecuteStream
	}

	return a.BaseLoopConfig("agent:"+a.name, prompt, provider, toolDefs, a.makeDispatch(executeTool, executeToolStream, ch, toolDefs))
}

// makeDispatch returns a DispatchFunc that executes tools via the given
// executor function and handles the ask_user, execute_plan,
// and spawn_agent special cases via the shared DispatchBuiltins method.
// When executeToolStream and ch are non-nil, tools implementing StreamingAnyTool
// emit progress events during execution.
func (a *LLMAgent) makeDispatch(executeTool ToolExecFunc, executeToolStream ToolExecStreamFunc, ch chan<- StreamEvent, resolvedToolDefs []ToolDefinition) DispatchFunc {
	return NewStandardDispatch(StandardDispatchConfig{
		Builtins:          a.DispatchBuiltins,
		SpawnHandler:      a.ExecuteSpawn,
		ExecuteTool:       executeTool,
		ExecuteToolStream: executeToolStream,
		ResolvedToolDefs:  resolvedToolDefs,
		StreamCh:          ch,
	})
}

// ExecuteWith runs the tool-calling loop like Execute but applies per-call
// overrides from opts on top of the agent's base configuration. A nil opts
// is equivalent to calling Execute directly.
func (a *LLMAgent) ExecuteWith(ctx context.Context, task AgentTask, opts *RunOptions) (AgentResult, error) {
	if err := opts.Validate(); err != nil {
		return AgentResult{}, err
	}
	effective := applyRunOptions(a.cfg(), opts)
	ctx = WithTaskContext(ctx, task)
	return a.ExecuteWithSpan(ctx, task, nil, "LLMAgent", "agent", func(ctx context.Context, task AgentTask, ch chan<- StreamEvent) LoopConfig {
		return a.buildLoopConfigFrom(ctx, task, ch, effective, opts)
	})
}

// ExecuteStreamWith runs the tool-calling loop like ExecuteStream but applies
// per-call overrides from opts. A nil opts is equivalent to calling
// ExecuteStream directly. The channel is closed when streaming completes
// (including on validation error).
func (a *LLMAgent) ExecuteStreamWith(ctx context.Context, task AgentTask, ch chan<- StreamEvent, opts *RunOptions) (AgentResult, error) {
	if err := opts.Validate(); err != nil {
		close(ch)
		return AgentResult{}, err
	}
	effective := applyRunOptions(a.cfg(), opts)
	ctx = WithTaskContext(ctx, task)
	return a.ExecuteWithSpan(ctx, task, ch, "LLMAgent", "agent", func(ctx context.Context, task AgentTask, ch chan<- StreamEvent) LoopConfig {
		return a.buildLoopConfigFrom(ctx, task, ch, effective, opts)
	})
}

// cfg snapshots the current AgentCore fields into a Config so that
// applyRunOptions has a base to shallow-copy and override. Only the fields
// that applyRunOptions may mutate need to be present; all others are ignored.
func (a *LLMAgent) cfg() *Config {
	var maxSteps int
	if a.maxSteps != 0 {
		maxSteps = a.maxSteps
	}
	return &Config{
		prompt:              a.systemPrompt,
		maxIter:             a.maxIter,
		responseSchema:      a.responseSchema,
		maxAttachmentBytes:  a.maxAttachmentBytes,
		maxToolResultLen:    a.maxToolResultLen,
		maxPlanSteps:        a.maxPlanSteps,
		generationParams:    a.genParams,
		tracer:              a.tracer,
		logger:              a.logger,
		inputHandler:        a.handler,
		maxSteps:            &maxSteps,
	}
}

// buildLoopConfigFrom builds a LoopConfig using an explicit cfg instead of
// the AgentCore fields, enabling per-call RunOptions overrides. Fields that
// are not overridable (suspend state, compressor instance) still come from
// the AgentCore. opts is consulted for memory overrides; pass nil to use the
// agent default.
func (a *LLMAgent) buildLoopConfigFrom(ctx context.Context, task AgentTask, ch chan<- StreamEvent, cfg *Config, opts *RunOptions) LoopConfig {
	// Resolve prompt: cfg.prompt takes precedence over dynamic prompt when
	// a per-call Prompt override is set; otherwise fall back to normal resolution.
	prompt := cfg.prompt
	if a.dynamicPrompt != nil {
		// Dynamic prompt overrides the base prompt; cfg.prompt may have been
		// set by a RunOptions.Prompt override, in which case we skip dynamic.
		if cfg.prompt == a.systemPrompt {
			// No per-call prompt override — apply dynamic prompt as usual.
			prompt = a.dynamicPrompt(ctx, task)
		}
	}

	// Resolve provider: dynamic model override, if configured.
	provider := a.provider
	if a.dynamicModel != nil {
		provider = a.dynamicModel(ctx, task)
	}

	// Append skill instructions.
	if a.activeSkillInstructions != "" {
		prompt = prompt + "\n\n# Active Skills\n\n" + a.activeSkillInstructions
	}

	// Resolve tools: dynamic replaces static.
	var toolDefs []ToolDefinition
	var executeTool ToolExecFunc
	var executeToolStream ToolExecStreamFunc
	if dynDefs, dynExec := a.ResolveDynamicTools(ctx, task); dynDefs != nil {
		a.logger.Debug("using dynamic tools", "agent", a.name, "tool_count", len(dynDefs))
		toolDefs = a.CacheBuiltinToolDefs(dynDefs)
		executeTool = dynExec
	} else {
		toolDefs = a.cachedToolDefs
		executeTool = a.tools.Execute
		executeToolStream = a.tools.ExecuteStream
	}

	dispatch := a.makeDispatch(executeTool, executeToolStream, ch, toolDefs)

	// Resolve memory orchestrator: per-call override takes precedence.
	mem := &a.mem
	if opts != nil && opts.Memory != nil {
		mem = opts.Memory
	}

	maxSteps := 0
	if cfg.maxSteps != nil {
		maxSteps = *cfg.maxSteps
	}
	return LoopConfig{
		name:                "agent:" + a.name,
		provider:            provider,
		tools:               toolDefs,
		processors:          a.processors,
		maxIter:             cfg.maxIter,
		mem:                 mem,
		inputHandler:        cfg.inputHandler,
		dispatch:            dispatch,
		systemPrompt:        prompt,
		responseSchema:      cfg.responseSchema,
		tracer:              cfg.tracer,
		logger:              cfg.logger,
		maxAttachmentBytes:  cfg.maxAttachmentBytes,
		suspendCount:        &a.suspendCount,
		suspendBytes:        &a.suspendBytes,
		suspendMu:           &a.suspendMu,
		maxSuspendSnapshots: a.maxSuspendSnapshots,
		maxSuspendBytes:     a.maxSuspendBytes,
		compressModel:       a.compressModel,
		compressThreshold:   a.compressThreshold,
		compressor:          a.compressor,
		generationParams:    cfg.generationParams,
		maxParallelDispatch: a.maxParallelDispatch,
		maxToolResultLen:    cfg.maxToolResultLen,
		maxPlanSteps:        cfg.maxPlanSteps,
		toolResultStore:     a.toolResultStore,
		maxSteps:            maxSteps,
		prepareStep:         cfg.prepareStep,
		onError:             cfg.onError,
		onIterationComplete: cfg.onIterationComplete,
	}
}

// compile-time checks
var (
	_ Agent                        = (*LLMAgent)(nil)
	_ StreamingAgent               = (*LLMAgent)(nil)
	_ AgentWithOptions             = (*LLMAgent)(nil)
	_ StreamingAgentWithOptions    = (*LLMAgent)(nil)
)

// --- execute_plan tool ---

// executePlanToolDef returns the tool definition for the built-in
// execute_plan tool. The schema is derived from planArgs/planStep via
// reflection — keeps the LLM-facing schema in sync with the Go types.
func executePlanToolDef() ToolDefinition {
	return ToolDefinition{
		Name:        "execute_plan",
		Description: "Execute multiple tool calls in a single batch without intermediate reasoning. Use when you need to call tools multiple times with known inputs upfront. All steps run in parallel. Returns structured results per step.",
		Parameters:  core.DeriveSchema[planArgs](),
	}
}

// planArgs is the parsed arguments for the execute_plan tool call.
type planArgs struct {
	Steps []planStep `json:"steps" describe:"Array of tool calls to execute in parallel"`
}

// planStep is a single step in an execute_plan call.
type planStep struct {
	Tool string          `json:"tool" describe:"Name of the tool to call"`
	Args json.RawMessage `json:"args" describe:"Arguments for the tool"`
}

// planStepResult is one entry in the execute_plan result array.
type planStepResult struct {
	Step   int    `json:"step"`
	Tool   string `json:"tool"`
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// maxPlanSteps caps the number of steps in a single execute_plan call
// to prevent resource exhaustion from unbounded goroutine creation.
const maxPlanSteps = 50

// executePlan handles the execute_plan tool call by parsing steps,
// executing them in parallel via the given dispatch function, and
// returning aggregated results as JSON. Shared by LLMAgent and Network.
func executePlan(ctx context.Context, args json.RawMessage, dispatch DispatchFunc, planStepsLimit, parallelLimit int) DispatchResult {
	if planStepsLimit == 0 {
		planStepsLimit = maxPlanSteps
	}
	if parallelLimit == 0 {
		parallelLimit = maxParallelDispatch
	}
	var params planArgs
	if err := json.Unmarshal(args, &params); err != nil {
		return DispatchResult{Content: "error: invalid execute_plan args: " + err.Error(), IsError: true}
	}
	if len(params.Steps) == 0 {
		return DispatchResult{Content: "error: execute_plan requires at least one step", IsError: true}
	}
	if len(params.Steps) > planStepsLimit {
		return DispatchResult{Content: fmt.Sprintf("error: execute_plan limited to %d steps, got %d", planStepsLimit, len(params.Steps)), IsError: true}
	}

	// Build tool calls, preventing recursion.
	calls := make([]ToolCall, len(params.Steps))
	for i, step := range params.Steps {
		if step.Tool == "execute_plan" {
			return DispatchResult{Content: "error: execute_plan steps cannot call execute_plan", IsError: true}
		}
		calls[i] = ToolCall{
			ID:   "plan_step_" + strconv.Itoa(i),
			Name: step.Tool,
			Args: step.Args,
		}
	}

	// Wrap dispatch to block ask_user inside parallel plan steps.
	// Most InputHandler implementations aren't designed for concurrent
	// invocation, and simultaneous user prompts are confusing.
	safeDispatch := func(ctx context.Context, tc ToolCall) DispatchResult {
		if tc.Name == "ask_user" {
			return DispatchResult{Content: "error: ask_user cannot be called from within execute_plan", IsError: true}
		}
		return dispatch(ctx, tc)
	}

	// Execute all steps in parallel.
	results := dispatchParallel(ctx, calls, safeDispatch, parallelLimit)

	// Aggregate results.
	var totalUsage Usage
	var allAttachments []Attachment
	stepResults := make([]planStepResult, len(params.Steps))
	for i, step := range params.Steps {
		totalUsage.InputTokens += results[i].usage.InputTokens
		totalUsage.OutputTokens += results[i].usage.OutputTokens

		if len(results[i].attachments) > 0 {
			allAttachments = append(allAttachments, results[i].attachments...)
		}

		sr := planStepResult{Step: i, Tool: step.Tool, Status: "ok", Result: results[i].content}
		if results[i].isError {
			sr.Status = "error"
			sr.Error = results[i].content
			sr.Result = ""
		}
		stepResults[i] = sr
	}

	out, _ := json.Marshal(stepResults)
	return DispatchResult{Content: string(out), Usage: totalUsage, Attachments: allAttachments}
}

// --- ask_user tool ---

// askUserToolDef returns the tool definition for the built-in ask_user tool.
func askUserToolDef() ToolDefinition {
	return ToolDefinition{
		Name:        "ask_user",
		Description: "Ask the user a question when you need clarification, confirmation, or additional information to proceed.",
		Parameters:  core.DeriveSchema[askUserArgs](),
	}
}

// askUserArgs is the parsed arguments for the ask_user tool call.
type askUserArgs struct {
	Question string   `json:"question" describe:"The question to ask the user"`
	Options  []string `json:"options,omitempty" describe:"Optional suggested answers for the user to choose from"`
}

// executeAskUser handles the ask_user special-case tool call.
// Shared by both LLMAgent and Network dispatch functions.
func executeAskUser(ctx context.Context, handler InputHandler, agentName string, tc ToolCall) (string, error) {
	var args askUserArgs
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		return "", err
	}

	resp, err := handler.RequestInput(ctx, InputRequest{
		Question: args.Question,
		Options:  args.Options,
		Metadata: map[string]string{
			"agent":  agentName,
			"source": "llm",
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Value, nil
}

// --- spawn_agent tool ---

// spawnAgentToolDef returns the tool definition for the built-in spawn_agent tool.
func spawnAgentToolDef() ToolDefinition {
	return ToolDefinition{
		Name:        "spawn_agent",
		Description: "Spawn a sub-agent to handle a specific task autonomously. The sub-agent has access to the same tools as you. Use when a task is independent and can be delegated. Call spawn_agent multiple times in one response to run sub-agents in parallel.",
		Parameters:  core.DeriveSchema[spawnAgentArgs](),
	}
}

// funcTool adapts a single ToolDefinition + executor into AnyTool.
// Used by spawn_agent to pass the parent's (possibly filtered) tools to the
// ephemeral sub-agent without reconstructing a ToolRegistry. Each filtered
// definition becomes one funcTool.
type funcTool struct {
	def  ToolDefinition
	exec ToolExecFunc
}

func (f *funcTool) Name() string               { return f.def.Name }
func (f *funcTool) Definition() ToolDefinition { return f.def }
func (f *funcTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	return f.exec(ctx, f.def.Name, args)
}

// spawnAgentArgs is the parsed arguments for the spawn_agent tool call.
type spawnAgentArgs struct {
	Task string `json:"task" describe:"Clear instruction for what the sub-agent should accomplish"`
	Name string `json:"name,omitempty" describe:"Short label for this sub-agent (for logging). Auto-generated if omitted."`
}

// spawnAgentName returns a short name for a sub-agent, derived from the
// args.Name if provided or from the first 20 runes of the task (slugified).
func spawnAgentName(args spawnAgentArgs) string {
	if args.Name != "" {
		return args.Name
	}
	name := TruncateStr(args.Task, 20) // rune-safe truncation (reuses loop.go helper)
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, name)
}

// subAgentPrompt is the minimal system prompt given to spawned sub-agents.
const subAgentPrompt = "You are a sub-agent. Complete the given task thoroughly and return the result. Be concise."

