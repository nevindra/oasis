package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/skills"
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

	if cfg.Sandbox != nil {
		for _, t := range cfg.SandboxTools {
			a.Tools.Add(t)
		}
	}

	// Register skill tools if a provider is configured.
	if cfg.SkillProvider != nil {
		for _, t := range skills.NewSkillTools(cfg.SkillProvider) {
			a.Tools.Add(t)
		}
	}

	// Pre-compute tool definitions for the non-dynamic path.
	// Avoids rebuilding the slice on every Execute call.
	if a.DynamicTools == nil {
		a.CachedToolDefs = a.CacheBuiltinToolDefs(a.Tools.AllDefinitions())
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
	if a.ActiveSkillInstructions != "" {
		prompt = prompt + "\n\n# Active Skills\n\n" + a.ActiveSkillInstructions
	}

	// Resolve tools: dynamic replaces static.
	var toolDefs []ToolDefinition
	var executeTool ToolExecFunc
	var executeToolStream ToolExecStreamFunc
	if dynDefs, dynExec := a.ResolveDynamicTools(ctx, task); dynDefs != nil {
		a.Logger.Debug("using dynamic tools", "agent", a.name, "tool_count", len(dynDefs))
		toolDefs = a.CacheBuiltinToolDefs(dynDefs)
		executeTool = dynExec
	} else {
		toolDefs = a.CachedToolDefs
		executeTool = a.Tools.Execute
		executeToolStream = a.Tools.ExecuteStream
	}

	return a.BaseLoopConfig("agent:"+a.name, prompt, provider, toolDefs, a.makeDispatch(executeTool, executeToolStream, ch, toolDefs))
}

// makeDispatch returns a DispatchFunc that executes tools via the given
// executor function and handles the ask_user, execute_plan,
// and spawn_agent special cases via the shared dispatchBuiltins helper.
// When executeToolStream and ch are non-nil, tools implementing StreamingAnyTool
// emit progress events during execution.
func (a *LLMAgent) makeDispatch(executeTool ToolExecFunc, executeToolStream ToolExecStreamFunc, ch chan<- StreamEvent, resolvedToolDefs []ToolDefinition) DispatchFunc {
	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
		if r, ok := DispatchBuiltins(ctx, tc, dispatch, a.Handler, a.name, a.PlanExecution, a.MaxPlanSteps, a.MaxParallelDispatch); ok {
			return r
		}
		if tc.Name == "spawn_agent" && a.SpawnEnabled {
			return ExecuteSpawnAgent(ctx, tc.Args, SubAgentConfig{
				Provider:       a.LLMProvider,
				ToolDefs:       resolvedToolDefs,
				ExecuteTool:    executeTool,
				MaxIter:        a.MaxIter,
				MaxSpawnDepth:  a.SpawnDepthLimit,
				DenySpawnTools: a.DeniedSpawnTools,
				PlanExecution:  a.PlanExecution,
				Logger:         a.Logger,
				GenParams:      a.GenParams,
			})
		}
		return DispatchTool(ctx, executeTool, executeToolStream, tc.Name, tc.Args, ch)
	}
	return dispatch
}

// compile-time checks
var (
	_ Agent          = (*LLMAgent)(nil)
	_ StreamingAgent = (*LLMAgent)(nil)
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

// SubAgentConfig carries the parent's state needed to construct a sub-agent.
// Passed through makeDispatch closures — no context keys needed.
// Exported for network subpackage access.
type SubAgentConfig struct {
	Provider       Provider
	ToolDefs       []ToolDefinition
	ExecuteTool    ToolExecFunc
	MaxIter        int
	MaxSpawnDepth  int
	DenySpawnTools []string
	PlanExecution  bool         // inherit parent's execute_plan capability
	Logger         *slog.Logger
	GenParams      *GenerationParams
}

// ExecuteSpawnAgent handles the spawn_agent tool call. Constructs an ephemeral
// LLMAgent with inherited tools (minus denied ones), executes it, returns result.
// Exported for network subpackage access.
func ExecuteSpawnAgent(ctx context.Context, args json.RawMessage, cfg SubAgentConfig) DispatchResult {
	var params spawnAgentArgs
	if err := json.Unmarshal(args, &params); err != nil {
		return DispatchResult{Content: "error: invalid spawn_agent args: " + err.Error(), IsError: true}
	}
	if params.Task == "" {
		return DispatchResult{Content: "error: spawn_agent requires non-empty task", IsError: true}
	}

	// Check depth limit.
	depth := spawnDepth(ctx)
	if depth >= cfg.MaxSpawnDepth {
		return DispatchResult{Content: fmt.Sprintf("error: max spawn depth (%d) exceeded", cfg.MaxSpawnDepth), IsError: true}
	}

	name := spawnAgentName(params)

	// Filter tool definitions: remove denied tools + ask_user.
	// When child will be at max depth, also strip spawn_agent.
	childAtMaxDepth := depth+1 >= cfg.MaxSpawnDepth
	var filteredDefs []ToolDefinition
	deny := make(map[string]bool, len(cfg.DenySpawnTools)+1)
	deny["ask_user"] = true
	for _, n := range cfg.DenySpawnTools {
		deny[n] = true
	}
	if childAtMaxDepth {
		deny["spawn_agent"] = true
	}
	for _, d := range cfg.ToolDefs {
		if !deny[d.Name] {
			filteredDefs = append(filteredDefs, d)
		}
	}

	// Build filtered executor that respects deny list.
	filteredExec := func(ctx context.Context, toolName string, toolArgs json.RawMessage) (ToolResult, error) {
		if deny[toolName] {
			return ToolResult{Error: "tool " + toolName + " is not available to sub-agents"}, nil
		}
		return cfg.ExecuteTool(ctx, toolName, toolArgs)
	}

	// Build ephemeral options. Wrap each definition as its own AnyTool.
	subTools := make([]AnyTool, len(filteredDefs))
	for i, d := range filteredDefs {
		subTools[i] = &funcTool{def: d, exec: filteredExec}
	}
	opts := []AgentOption{
		WithPrompt(subAgentPrompt),
		WithTools(subTools...),
		WithMaxIter(cfg.MaxIter),
		WithLogger(cfg.Logger),
	}
	if cfg.GenParams != nil {
		opts = append(opts, WithGeneration(Generation{
			Temperature: cfg.GenParams.Temperature,
			TopP:        cfg.GenParams.TopP,
			TopK:        cfg.GenParams.TopK,
			MaxTokens:   cfg.GenParams.MaxTokens,
		}))
	}
	// Enable spawning on child if it won't be at max depth.
	if !childAtMaxDepth {
		opts = append(opts, WithSubAgentSpawning(
			MaxSpawnDepth(cfg.MaxSpawnDepth),
			DenySpawnTools(cfg.DenySpawnTools...),
		))
	}
	// Inherit plan execution from parent.
	if cfg.PlanExecution {
		opts = append(opts, WithPlanExecution())
	}

	child := NewLLMAgent("sub:"+name, "sub-agent: "+params.Task, cfg.Provider, opts...)

	// Execute with incremented depth.
	childCtx := withSpawnDepth(ctx, depth+1)
	result, err := child.Execute(childCtx, AgentTask{Input: params.Task})
	if err != nil {
		return DispatchResult{Content: "error: sub-agent failed: " + err.Error(), IsError: true}
	}
	return DispatchResult{Content: result.Output, Usage: result.Usage, Attachments: result.Attachments}
}
