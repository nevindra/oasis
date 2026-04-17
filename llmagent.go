package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// LLMAgent is an Agent that uses an LLM with tools to complete tasks.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type LLMAgent struct {
	agentCore
}

// NewLLMAgent creates an LLMAgent with the given provider and options.
func NewLLMAgent(name, description string, provider Provider, opts ...AgentOption) *LLMAgent {
	cfg := buildConfig(opts)
	a := &LLMAgent{}
	initCore(&a.agentCore, name, description, provider, cfg)

	if cfg.sandbox != nil {
		for _, t := range cfg.sandboxTools {
			a.tools.Add(t)
		}
	}

	// Register skill tools if a provider is configured.
	if cfg.skillProvider != nil {
		a.tools.Add(newSkillTool(cfg.skillProvider))
	}

	// Pre-compute tool definitions for the non-dynamic path.
	// Avoids rebuilding the slice on every Execute call.
	if a.dynamicTools == nil {
		a.cachedToolDefs = a.cacheBuiltinToolDefs(a.tools.AllDefinitions())
	}

	return a
}

// MCP returns the agent's MCP controller for runtime server management.
// The controller is backed by the agent's MCPRegistry (which may be shared
// across agents when WithSharedMCPRegistry was used at construction).
// The returned value is never nil.
func (a *LLMAgent) MCP() *MCPController {
	return &MCPController{reg: a.mcpRegistry}
}

// Execute runs the tool-calling loop until the LLM produces a final text response.
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return a.executeWithSpan(ctx, task, nil, "LLMAgent", "agent", a.buildLoopConfig)
}

// ExecuteStream runs the tool-calling loop like Execute, but emits StreamEvent
// values into ch throughout execution. Events include text deltas during the
// final LLM response and tool call start/result during tool iterations.
// The channel is closed when streaming completes.
func (a *LLMAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return a.executeWithSpan(ctx, task, ch, "LLMAgent", "agent", a.buildLoopConfig)
}

// buildLoopConfig wires LLMAgent fields into a loopConfig for runLoop.
// Resolves dynamic prompt, model, and tools when configured.
func (a *LLMAgent) buildLoopConfig(ctx context.Context, task AgentTask, ch chan<- StreamEvent) loopConfig {
	prompt, provider := a.resolvePromptAndProvider(ctx, task)
	if a.activeSkillInstructions != "" {
		prompt = prompt + "\n\n# Active Skills\n\n" + a.activeSkillInstructions
	}

	// Resolve tools: dynamic replaces static.
	var toolDefs []ToolDefinition
	var executeTool toolExecFunc
	var executeToolStream toolExecStreamFunc
	if dynDefs, dynExec, dynExecStream := a.resolveDynamicTools(ctx, task); dynDefs != nil {
		a.logger.Debug("using dynamic tools", "agent", a.name, "tool_count", len(dynDefs))
		toolDefs = a.cacheBuiltinToolDefs(dynDefs)
		executeTool = dynExec
		executeToolStream = dynExecStream
	} else {
		toolDefs = a.cachedToolDefs
		executeTool = a.tools.Execute
		executeToolStream = a.tools.ExecuteStream
	}

	return a.baseLoopConfig("agent:"+a.name, prompt, provider, toolDefs, a.makeDispatch(executeTool, executeToolStream, ch, toolDefs))
}

// makeDispatch returns a DispatchFunc that executes tools via the given
// executor function and handles the ask_user, execute_plan,
// and spawn_agent special cases via the shared dispatchBuiltins helper.
// When executeToolStream and ch are non-nil, tools implementing StreamingTool
// emit progress events during execution.
func (a *LLMAgent) makeDispatch(executeTool toolExecFunc, executeToolStream toolExecStreamFunc, ch chan<- StreamEvent, resolvedToolDefs []ToolDefinition) DispatchFunc {
	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
		if r, ok := dispatchBuiltins(ctx, tc, dispatch, a.inputHandler, a.name, a.planExecution); ok {
			return r
		}
		if tc.Name == "spawn_agent" && a.spawnEnabled {
			return executeSpawnAgent(ctx, tc.Args, subAgentConfig{
				provider:          a.provider,
				toolDefs:          resolvedToolDefs,
				executeTool:       executeTool,
				executeToolStream: executeToolStream,
				maxIter:           a.maxIter,
				maxSpawnDepth:     a.maxSpawnDepth,
				denySpawnTools:    a.denySpawnTools,
				planExecution:     a.planExecution,
				logger:            a.logger,
				tracer:            a.tracer,
				genParams:         a.generationParams,
				mcpRegistry:       a.mcpRegistry,
				ch:                ch,
			})
		}
		return dispatchTool(ctx, executeTool, executeToolStream, tc.Name, tc.Args, ch)
	}
	return dispatch
}

// compile-time checks
var (
	_ Agent          = (*LLMAgent)(nil)
	_ StreamingAgent = (*LLMAgent)(nil)
)

// --- execute_plan tool ---

// executePlanToolDef is the tool definition for the built-in execute_plan tool.
var executePlanToolDef = ToolDefinition{
	Name:        "execute_plan",
	Description: "Execute multiple tool calls in a single batch without intermediate reasoning. Use when you need to call tools multiple times with known inputs upfront. All steps run in parallel. Returns structured results per step.",
	Parameters: json.RawMessage(`{
		"type": "object",
		"properties": {
			"steps": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"tool": {"type": "string", "description": "Name of the tool to call"},
						"args": {"type": "object", "description": "Arguments for the tool"}
					},
					"required": ["tool", "args"]
				},
				"description": "Array of tool calls to execute in parallel"
			}
		},
		"required": ["steps"]
	}`),
}

// planArgs is the parsed arguments for the execute_plan tool call.
type planArgs struct {
	Steps []planStep `json:"steps"`
}

// planStep is a single step in an execute_plan call.
type planStep struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
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
func executePlan(ctx context.Context, args json.RawMessage, dispatch DispatchFunc) DispatchResult {
	var params planArgs
	if err := json.Unmarshal(args, &params); err != nil {
		return DispatchResult{Content: "error: invalid execute_plan args: " + err.Error(), IsError: true}
	}
	if len(params.Steps) == 0 {
		return DispatchResult{Content: "error: execute_plan requires at least one step", IsError: true}
	}
	if len(params.Steps) > maxPlanSteps {
		return DispatchResult{Content: fmt.Sprintf("error: execute_plan limited to %d steps, got %d", maxPlanSteps, len(params.Steps)), IsError: true}
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
	results := dispatchParallel(ctx, calls, safeDispatch)

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

// askUserToolDef is the tool definition for the built-in ask_user tool.
var askUserToolDef = ToolDefinition{
	Name:        "ask_user",
	Description: "Ask the user a question when you need clarification, confirmation, or additional information to proceed.",
	Parameters: json.RawMessage(`{
		"type": "object",
		"properties": {
			"question": {
				"type": "string",
				"description": "The question to ask the user"
			},
			"options": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional suggested answers for the user to choose from"
			}
		},
		"required": ["question"]
	}`),
}

// askUserArgs is the parsed arguments for the ask_user tool call.
type askUserArgs struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
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

// spawnAgentToolDef is the tool definition for the built-in spawn_agent tool.
var spawnAgentToolDef = ToolDefinition{
	Name:        "spawn_agent",
	Description: "Spawn a sub-agent to handle a specific task autonomously. The sub-agent has access to the same tools as you. Use when a task is independent and can be delegated. Call spawn_agent multiple times in one response to run sub-agents in parallel.",
	Parameters: json.RawMessage(`{
		"type": "object",
		"properties": {
			"task": {
				"type": "string",
				"description": "Clear instruction for what the sub-agent should accomplish"
			},
			"name": {
				"type": "string",
				"description": "Short label for this sub-agent (for logging). Auto-generated if omitted."
			}
		},
		"required": ["task"]
	}`),
}

// funcTool wraps resolved tool definitions and executors into the Tool /
// StreamingTool interfaces. Used by spawn_agent to pass the parent's
// (possibly filtered) tools to the ephemeral sub-agent without reconstructing
// a ToolRegistry. When execStream is set and ch is non-nil, ExecuteStream
// routes to the parent's streaming executor so StreamingTool progress events
// inside sub-agents reach the parent's stream.
type funcTool struct {
	defs       []ToolDefinition
	exec       toolExecFunc
	execStream toolExecStreamFunc
}

func (f *funcTool) Definitions() []ToolDefinition { return f.defs }
func (f *funcTool) Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	return f.exec(ctx, name, args)
}
func (f *funcTool) ExecuteStream(ctx context.Context, name string, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error) {
	if f.execStream != nil && ch != nil {
		return f.execStream(ctx, name, args, ch)
	}
	return f.exec(ctx, name, args)
}

// spawnAgentArgs is the parsed arguments for the spawn_agent tool call.
type spawnAgentArgs struct {
	Task string `json:"task"`
	Name string `json:"name,omitempty"`
}

// spawnAgentName returns a short name for a sub-agent, derived from the
// args.Name if provided or from the first 20 runes of the task (slugified).
func spawnAgentName(args spawnAgentArgs) string {
	if args.Name != "" {
		return args.Name
	}
	name := truncateStr(args.Task, 20) // rune-safe truncation (reuses loop.go helper)
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, name)
}

// subAgentPrompt is the minimal system prompt given to spawned sub-agents.
const subAgentPrompt = "You are a sub-agent. Complete the given task thoroughly and return the result. Be concise."

// subAgentConfig carries the parent's state needed to construct a sub-agent.
// Passed through makeDispatch closures — no context keys needed.
type subAgentConfig struct {
	provider          Provider
	toolDefs          []ToolDefinition
	executeTool       toolExecFunc
	executeToolStream toolExecStreamFunc // parent's streaming tool executor; propagates StreamingTool progress
	maxIter           int
	maxSpawnDepth     int
	denySpawnTools    []string
	planExecution     bool // inherit parent's execute_plan capability
	logger            *slog.Logger
	tracer            Tracer             // parent's tracer; sub-agent spans are children of parent's span
	genParams         *GenerationParams
	mcpRegistry       *MCPRegistry       // parent's MCP registry, shared to avoid per-spawn channel/map allocation
	ch                chan<- StreamEvent // parent's stream channel; when non-nil, child events are forwarded
}

// executeSpawnAgent handles the spawn_agent tool call. Constructs an ephemeral
// LLMAgent with inherited tools (minus denied ones), executes it, returns result.
func executeSpawnAgent(ctx context.Context, args json.RawMessage, cfg subAgentConfig) DispatchResult {
	var params spawnAgentArgs
	if err := json.Unmarshal(args, &params); err != nil {
		return DispatchResult{Content: "error: invalid spawn_agent args: " + err.Error(), IsError: true}
	}
	if params.Task == "" {
		return DispatchResult{Content: "error: spawn_agent requires non-empty task", IsError: true}
	}

	// Check depth limit.
	depth := spawnDepth(ctx)
	if depth >= cfg.maxSpawnDepth {
		return DispatchResult{Content: fmt.Sprintf("error: max spawn depth (%d) exceeded", cfg.maxSpawnDepth), IsError: true}
	}

	name := spawnAgentName(params)

	// Filter tool definitions: remove denied tools + ask_user.
	// When child will be at max depth, also strip spawn_agent.
	childAtMaxDepth := depth+1 >= cfg.maxSpawnDepth
	var filteredDefs []ToolDefinition
	deny := make(map[string]bool, len(cfg.denySpawnTools)+1)
	deny["ask_user"] = true
	for _, n := range cfg.denySpawnTools {
		deny[n] = true
	}
	if childAtMaxDepth {
		deny["spawn_agent"] = true
	}
	for _, d := range cfg.toolDefs {
		if !deny[d.Name] {
			filteredDefs = append(filteredDefs, d)
		}
	}

	// Build filtered executors that respect the deny list. Both Execute and
	// ExecuteStream need the same filtering so StreamingTool progress events
	// still flow through for allowed tools.
	filteredExec := func(ctx context.Context, toolName string, toolArgs json.RawMessage) (ToolResult, error) {
		if deny[toolName] {
			return ToolResult{Error: "tool " + toolName + " is not available to sub-agents"}, nil
		}
		return cfg.executeTool(ctx, toolName, toolArgs)
	}
	var filteredExecStream toolExecStreamFunc
	if cfg.executeToolStream != nil {
		filteredExecStream = func(ctx context.Context, toolName string, toolArgs json.RawMessage, streamCh chan<- StreamEvent) (ToolResult, error) {
			if deny[toolName] {
				return ToolResult{Error: "tool " + toolName + " is not available to sub-agents"}, nil
			}
			return cfg.executeToolStream(ctx, toolName, toolArgs, streamCh)
		}
	}

	// Build ephemeral options.
	opts := []AgentOption{
		WithPrompt(subAgentPrompt),
		WithTools(&funcTool{defs: filteredDefs, exec: filteredExec, execStream: filteredExecStream}),
		WithMaxIter(cfg.maxIter),
		WithLogger(cfg.logger),
		WithGenerationParams(cfg.genParams),
	}
	if cfg.tracer != nil {
		opts = append(opts, WithTracer(cfg.tracer))
	}
	if cfg.mcpRegistry != nil {
		opts = append(opts, WithSharedMCPRegistry(cfg.mcpRegistry))
	}
	// Enable spawning on child if it won't be at max depth.
	if !childAtMaxDepth {
		opts = append(opts, WithSubAgentSpawning(
			MaxSpawnDepth(cfg.maxSpawnDepth),
			DenySpawnTools(cfg.denySpawnTools...),
		))
	}
	// Inherit plan execution from parent.
	if cfg.planExecution {
		opts = append(opts, WithPlanExecution())
	}

	child := NewLLMAgent("sub:"+name, "sub-agent: "+params.Task, cfg.provider, opts...)

	// Execute with incremented depth. When the parent is streaming, use
	// executeAgent to forward the child's events through the parent's channel
	// (panic recovery + EventInputReceived filtering handled there).
	childCtx := withSpawnDepth(ctx, depth+1)
	childTask := AgentTask{Input: params.Task}
	result, err := executeAgent(childCtx, child, name, childTask, cfg.ch, cfg.logger)
	if err != nil {
		return DispatchResult{Content: "error: sub-agent failed: " + err.Error(), IsError: true}
	}
	return DispatchResult{Content: result.Output, Usage: result.Usage, Attachments: result.Attachments}
}
