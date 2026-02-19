package oasis

import (
	"context"
	"encoding/json"
	"fmt"
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
	responseSchema *ResponseSchema
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
	a.responseSchema = cfg.responseSchema
	return a
}

func (a *LLMAgent) Name() string        { return a.name }
func (a *LLMAgent) Description() string { return a.description }

// Execute runs the tool-calling loop until the LLM produces a final text response.
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return runLoop(ctx, a.buildLoopConfig(), task, nil)
}

// ExecuteStream runs the tool-calling loop like Execute, but emits StreamEvent
// values into ch throughout execution. Events include text deltas during the
// final LLM response and tool call start/result during tool iterations.
// The channel is closed when streaming completes.
func (a *LLMAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	return runLoop(ctx, a.buildLoopConfig(), task, ch)
}

// buildLoopConfig wires LLMAgent fields into a loopConfig for runLoop.
func (a *LLMAgent) buildLoopConfig() loopConfig {
	toolDefs := a.tools.AllDefinitions()
	if a.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}
	if a.planExecution {
		toolDefs = append(toolDefs, executePlanToolDef)
	}
	return loopConfig{
		name:         "agent:" + a.name,
		provider:     a.provider,
		tools:        toolDefs,
		processors:   a.processors,
		maxIter:      a.maxIter,
		mem:          &a.mem,
		inputHandler: a.inputHandler,
		dispatch:       a.makeDispatch(),
		systemPrompt:   a.systemPrompt,
		responseSchema: a.responseSchema,
	}
}

// makeDispatch returns a dispatchFunc that executes tools via ToolRegistry
// and handles the ask_user and execute_plan special cases.
func (a *LLMAgent) makeDispatch() dispatchFunc {
	var dispatch dispatchFunc
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

		result, execErr := a.tools.Execute(ctx, tc.Name, tc.Args)
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
func executePlan(ctx context.Context, args json.RawMessage, dispatch dispatchFunc) (string, Usage) {
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
