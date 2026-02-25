package oasis

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Pre-compute tool definitions for the non-dynamic path.
	// Avoids rebuilding the slice on every Execute call.
	if a.dynamicTools == nil {
		a.cachedToolDefs = a.cacheBuiltinToolDefs(a.tools.AllDefinitions())
	}

	return a
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

	// Resolve tools: dynamic replaces static.
	var toolDefs []ToolDefinition
	var executeTool toolExecFunc
	var executeToolStream toolExecStreamFunc
	if dynDefs, dynExec := a.resolveDynamicTools(ctx, task); dynDefs != nil {
		toolDefs = a.cacheBuiltinToolDefs(dynDefs)
		executeTool = dynExec
	} else {
		toolDefs = a.cachedToolDefs
		executeTool = a.tools.Execute
		executeToolStream = a.tools.ExecuteStream
	}

	return a.baseLoopConfig("agent:"+a.name, prompt, provider, toolDefs, a.makeDispatch(executeTool, executeToolStream, ch))
}

// makeDispatch returns a DispatchFunc that executes tools via the given
// executor function and handles the ask_user, execute_plan, and execute_code
// special cases via the shared dispatchBuiltins helper.
// When executeToolStream and ch are non-nil, tools implementing StreamingTool
// emit progress events during execution.
func (a *LLMAgent) makeDispatch(executeTool toolExecFunc, executeToolStream toolExecStreamFunc, ch chan<- StreamEvent) DispatchFunc {
	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
		if r, ok := dispatchBuiltins(ctx, tc, dispatch, a.inputHandler, a.name, a.planExecution, a.codeRunner); ok {
			return r
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
			ID:   fmt.Sprintf("plan_step_%d", i),
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
func executeCode(ctx context.Context, args json.RawMessage, runner CodeRunner, dispatch DispatchFunc) DispatchResult {
	var params struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return DispatchResult{Content: "error: invalid execute_code args: " + err.Error(), IsError: true}
	}
	if params.Code == "" {
		return DispatchResult{Content: "error: execute_code requires non-empty code", IsError: true}
	}

	result, err := runner.Run(ctx, CodeRequest{Code: params.Code}, dispatch)
	if err != nil {
		return DispatchResult{Content: "error: code execution failed: " + err.Error(), IsError: true}
	}

	// Build response: prioritize structured output, include logs
	var response string
	if result.Error != "" {
		response = "error: " + result.Error
		if result.Logs != "" {
			response += "\n\nlogs:\n" + result.Logs
		}
		return DispatchResult{Content: response, IsError: true}
	}

	if result.Output != "" {
		response = result.Output
	} else {
		response = "(no result set — use set_result(data) to return structured output)"
	}
	if result.Logs != "" {
		response += "\n\nlogs:\n" + result.Logs
	}
	return DispatchResult{Content: response}
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
