package oasis

import (
	"context"
	"encoding/json"
)

const defaultMaxIter = 10

// LLMAgent is an Agent that uses an LLM with tools to complete tasks.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type LLMAgent struct {
	name         string
	description  string
	provider     Provider
	tools        *ToolRegistry
	processors   *ProcessorChain
	systemPrompt string
	maxIter      int
	inputHandler InputHandler
	mem          agentMemory
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
	return a
}

func (a *LLMAgent) Name() string        { return a.name }
func (a *LLMAgent) Description() string { return a.description }

// Execute runs the tool-calling loop until the LLM produces a final text response.
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return runLoop(ctx, a.buildLoopConfig(), task, nil)
}

// ExecuteStream runs the tool-calling loop like Execute, but streams the final
// text response into ch. Tool-calling iterations use blocking ChatWithTools;
// only the final response (no tool calls) uses ChatStream. The channel is
// closed when streaming completes.
func (a *LLMAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- string) (AgentResult, error) {
	return runLoop(ctx, a.buildLoopConfig(), task, ch)
}

// buildLoopConfig wires LLMAgent fields into a loopConfig for runLoop.
func (a *LLMAgent) buildLoopConfig() loopConfig {
	toolDefs := a.tools.AllDefinitions()
	if a.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}
	return loopConfig{
		name:         "agent:" + a.name,
		provider:     a.provider,
		tools:        toolDefs,
		processors:   a.processors,
		maxIter:      a.maxIter,
		mem:          &a.mem,
		inputHandler: a.inputHandler,
		dispatch:     a.makeDispatch(),
		systemPrompt: a.systemPrompt,
	}
}

// makeDispatch returns a dispatchFunc that executes tools via ToolRegistry
// and handles the ask_user special case.
func (a *LLMAgent) makeDispatch() dispatchFunc {
	return func(ctx context.Context, tc ToolCall) (string, Usage) {
		// Special case: ask_user tool
		if tc.Name == "ask_user" && a.inputHandler != nil {
			content, err := executeAskUser(ctx, a.inputHandler, a.name, tc)
			if err != nil {
				return "error: " + err.Error(), Usage{}
			}
			return content, Usage{}
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
}

// compile-time checks
var (
	_ Agent          = (*LLMAgent)(nil)
	_ StreamingAgent = (*LLMAgent)(nil)
)

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
