package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

const defaultMaxIter = 10

// LLMAgent is an Agent that uses an LLM with tools to complete tasks.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type LLMAgent struct {
	name          string
	description   string
	provider      Provider
	tools         *ToolRegistry
	processors    *ProcessorChain
	systemPrompt  string
	maxIter       int
	inputHandler  InputHandler
	planExecution  bool
	codeRunner     CodeRunner
	responseSchema *ResponseSchema
	dynamicPrompt  PromptFunc
	dynamicModel   ModelFunc
	dynamicTools   ToolsFunc
	tracer         Tracer
	logger         *slog.Logger
	mem            agentMemory
}

// NewLLMAgent creates an LLMAgent with the given provider and options.
func NewLLMAgent(name, description string, provider Provider, opts ...AgentOption) *LLMAgent {
	cfg := buildConfig(opts)
	a := &LLMAgent{
		name:         name,
		description:  description,
		provider:     provider,
		tools:        NewToolRegistry(),
		processors:   NewProcessorChain(),
		systemPrompt: cfg.prompt,
		maxIter:      defaultMaxIter,
		mem: agentMemory{
			store:             cfg.store,
			embedding:         cfg.embedding,
			memory:            cfg.memory,
			crossThreadSearch: cfg.crossThreadSearch,
			semanticMinScore:  cfg.semanticMinScore,
			maxHistory:        cfg.maxHistory,
			maxTokens:         cfg.maxTokens,
			provider:          provider,
		},
	}
	if cfg.maxIter > 0 {
		a.maxIter = cfg.maxIter
	}
	for _, t := range cfg.tools {
		a.tools.Add(t)
	}
	for _, p := range cfg.processors {
		a.processors.Add(p)
	}
	a.inputHandler = cfg.inputHandler
	a.planExecution = cfg.planExecution
	a.codeRunner = cfg.codeRunner
	a.responseSchema = cfg.responseSchema
	a.dynamicPrompt = cfg.dynamicPrompt
	a.dynamicModel = cfg.dynamicModel
	a.dynamicTools = cfg.dynamicTools
	a.tracer = cfg.tracer
	a.logger = cfg.logger
	a.mem.tracer = cfg.tracer
	a.mem.logger = cfg.logger
	return a
}

func (a *LLMAgent) Name() string        { return a.name }
func (a *LLMAgent) Description() string { return a.description }

// Execute runs the tool-calling loop until the LLM produces a final text response.
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return a.executeWithSpan(ctx, task, nil)
}

// ExecuteStream runs the tool-calling loop like Execute, but emits StreamEvent
// values into ch throughout execution. Events include text deltas during the
// final LLM response and tool call start/result during tool iterations.
// The channel is closed when streaming completes.
func (a *LLMAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return a.executeWithSpan(ctx, task, ch)
}

// executeWithSpan wraps runLoop with an agent.execute span when a tracer is configured.
func (a *LLMAgent) executeWithSpan(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	if a.tracer != nil {
		var span Span
		ctx, span = a.tracer.Start(ctx, "agent.execute",
			StringAttr("agent.name", a.name),
			StringAttr("agent.type", "LLMAgent"))
		defer func() {
			span.End()
		}()

		a.logger.Info("agent started", "agent", a.name)
		result, err := runLoop(ctx, a.buildLoopConfig(ctx, task), task, ch)

		span.SetAttr(
			IntAttr("tokens.input", result.Usage.InputTokens),
			IntAttr("tokens.output", result.Usage.OutputTokens))
		if err != nil {
			span.Error(err)
			span.SetAttr(StringAttr("agent.status", "error"))
		} else {
			span.SetAttr(StringAttr("agent.status", "ok"))
		}
		a.logger.Info("agent completed", "agent", a.name,
			"status", statusStr(err),
			"tokens.input", result.Usage.InputTokens,
			"tokens.output", result.Usage.OutputTokens)
		return result, err
	}
	return runLoop(ctx, a.buildLoopConfig(ctx, task), task, ch)
}

func statusStr(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// buildLoopConfig wires LLMAgent fields into a loopConfig for runLoop.
// Resolves dynamic prompt, model, and tools when configured.
func (a *LLMAgent) buildLoopConfig(ctx context.Context, task AgentTask) loopConfig {
	// Resolve prompt: dynamic > static
	prompt := a.systemPrompt
	if a.dynamicPrompt != nil {
		prompt = a.dynamicPrompt(ctx, task)
	}

	// Resolve provider: dynamic > construction-time
	provider := a.provider
	if a.dynamicModel != nil {
		provider = a.dynamicModel(ctx, task)
	}

	// Resolve tools: dynamic replaces static
	registry := a.tools
	if a.dynamicTools != nil {
		registry = NewToolRegistry()
		for _, t := range a.dynamicTools(ctx, task) {
			registry.Add(t)
		}
	}

	toolDefs := registry.AllDefinitions()
	if a.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}
	if a.planExecution {
		toolDefs = append(toolDefs, executePlanToolDef)
	}
	if a.codeRunner != nil {
		toolDefs = append(toolDefs, executeCodeToolDef)
	}
	return loopConfig{
		name:           "agent:" + a.name,
		provider:       provider,
		tools:          toolDefs,
		processors:     a.processors,
		maxIter:        a.maxIter,
		mem:            &a.mem,
		inputHandler:   a.inputHandler,
		dispatch:       a.makeDispatch(registry),
		systemPrompt:   prompt,
		responseSchema: a.responseSchema,
		tracer:         a.tracer,
		logger:         a.logger,
	}
}

// makeDispatch returns a DispatchFunc that executes tools via the given registry
// and handles the ask_user and execute_plan special cases.
func (a *LLMAgent) makeDispatch(registry *ToolRegistry) DispatchFunc {
	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) (string, Usage) {
		// Special case: ask_user tool
		if tc.Name == "ask_user" && a.inputHandler != nil {
			content, err := executeAskUser(ctx, a.inputHandler, a.name, tc)
			if err != nil {
				return "error: " + err.Error(), Usage{}
			}
			return content, Usage{}
		}

		// Special case: execute_plan tool
		if tc.Name == "execute_plan" && a.planExecution {
			return executePlan(ctx, tc.Args, dispatch)
		}

		// Special case: execute_code tool
		if tc.Name == "execute_code" && a.codeRunner != nil {
			return executeCode(ctx, tc.Args, a.codeRunner, dispatch)
		}

		result, execErr := registry.Execute(ctx, tc.Name, tc.Args)
		content := result.Content
		if execErr != nil {
			content = "error: " + execErr.Error()
		} else if result.Error != "" {
			content = "error: " + result.Error
		}
		return content, Usage{}
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

// executePlan handles the execute_plan tool call by parsing steps,
// executing them in parallel via the given dispatch function, and
// returning aggregated results as JSON. Shared by LLMAgent and Network.
func executePlan(ctx context.Context, args json.RawMessage, dispatch DispatchFunc) (string, Usage) {
	var params planArgs
	if err := json.Unmarshal(args, &params); err != nil {
		return "error: invalid execute_plan args: " + err.Error(), Usage{}
	}
	if len(params.Steps) == 0 {
		return "error: execute_plan requires at least one step", Usage{}
	}

	// Build tool calls, preventing recursion.
	calls := make([]ToolCall, len(params.Steps))
	for i, step := range params.Steps {
		if step.Tool == "execute_plan" {
			return "error: execute_plan steps cannot call execute_plan", Usage{}
		}
		calls[i] = ToolCall{
			ID:   fmt.Sprintf("plan_step_%d", i),
			Name: step.Tool,
			Args: step.Args,
		}
	}

	// Execute all steps in parallel.
	results := dispatchParallel(ctx, calls, dispatch)

	// Aggregate results.
	var totalUsage Usage
	stepResults := make([]planStepResult, len(params.Steps))
	for i, step := range params.Steps {
		totalUsage.InputTokens += results[i].usage.InputTokens
		totalUsage.OutputTokens += results[i].usage.OutputTokens

		sr := planStepResult{Step: i, Tool: step.Tool, Status: "ok", Result: results[i].content}
		if len(results[i].content) > 7 && results[i].content[:7] == "error: " {
			sr.Status = "error"
			sr.Error = results[i].content[7:]
			sr.Result = ""
		}
		stepResults[i] = sr
	}

	out, _ := json.Marshal(stepResults)
	return string(out), totalUsage
}

// --- execute_code tool ---

// executeCodeToolDef is the tool definition for the built-in execute_code tool.
var executeCodeToolDef = ToolDefinition{
	Name:        "execute_code",
	Description: "Execute Python code to perform complex operations. Use when you need conditional logic, data processing, loops, or to chain multiple tool calls with dependencies. You have access to call_tool(name, args) to invoke any available tool from within your code. The Python environment has full access to installed packages. Use print() for logs/debug output. Return results via set_result(data).",
	Parameters: json.RawMessage(`{
		"type": "object",
		"properties": {
			"code": {
				"type": "string",
				"description": "Python code to execute. Use call_tool(name, args) to call tools. Use call_tools_parallel([(name, args), ...]) for parallel tool calls. Use set_result(data) to return structured results. Use print() for debug output."
			}
		},
		"required": ["code"]
	}`),
}

// executeCode handles the execute_code tool call by delegating to the CodeRunner.
func executeCode(ctx context.Context, args json.RawMessage, runner CodeRunner, dispatch DispatchFunc) (string, Usage) {
	var params struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "error: invalid execute_code args: " + err.Error(), Usage{}
	}
	if params.Code == "" {
		return "error: execute_code requires non-empty code", Usage{}
	}

	result, err := runner.Run(ctx, CodeRequest{Code: params.Code}, dispatch)
	if err != nil {
		return "error: code execution failed: " + err.Error(), Usage{}
	}

	// Build response: prioritize structured output, include logs
	var response string
	if result.Error != "" {
		response = "error: " + result.Error
		if result.Logs != "" {
			response += "\n\nlogs:\n" + result.Logs
		}
		return response, Usage{}
	}

	if result.Output != "" {
		response = result.Output
	} else {
		response = "(no result set — use set_result(data) to return structured output)"
	}
	if result.Logs != "" {
		response += "\n\nlogs:\n" + result.Logs
	}
	return response, Usage{}
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
