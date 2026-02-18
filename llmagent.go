package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"log"
)

const defaultMaxIter = 10

// LLMAgent is an Agent that uses an LLM with tools to complete tasks.
// Optionally supports conversation memory, user memory, and semantic search
// when configured via WithConversationMemory, WithUserMemory, and WithSemanticSearch.
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
			store:     cfg.store,
			embedding: cfg.embedding,
			memory:    cfg.memory,
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
	var totalUsage Usage

	// Inject InputHandler into context for processors.
	if a.inputHandler != nil {
		ctx = WithInputHandlerContext(ctx, a.inputHandler)
	}

	// Build initial messages (system prompt + user memory + history + user input)
	messages := a.mem.buildMessages(ctx, a.name, a.systemPrompt, task)

	toolDefs := a.tools.AllDefinitions()

	// Append ask_user tool definition if handler is configured.
	if a.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}

	for i := 0; i < a.maxIter; i++ {
		req := ChatRequest{Messages: messages}

		// PreProcessor hook
		if err := a.processors.RunPreLLM(ctx, &req); err != nil {
			return handleProcessorError(err, totalUsage)
		}

		var resp ChatResponse
		var err error
		if len(toolDefs) > 0 {
			resp, err = a.provider.ChatWithTools(ctx, req, toolDefs)
		} else {
			resp, err = a.provider.Chat(ctx, req)
		}
		if err != nil {
			return AgentResult{Usage: totalUsage}, err
		}
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		// PostProcessor hook
		if err := a.processors.RunPostLLM(ctx, &resp); err != nil {
			return handleProcessorError(err, totalUsage)
		}

		// No tool calls — final response
		if len(resp.ToolCalls) == 0 {
			a.mem.persistMessages(ctx, a.name, task, task.Input, resp.Content)
			return AgentResult{Output: resp.Content, Usage: totalUsage}, nil
		}

		// Append assistant message with tool calls
		messages = append(messages, ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call
		for _, tc := range resp.ToolCalls {
			// Special case: ask_user tool
			if tc.Name == "ask_user" && a.inputHandler != nil {
				content, err := a.executeAskUser(ctx, tc)
				if err != nil {
					return AgentResult{Usage: totalUsage}, err
				}
				messages = append(messages, ToolResultMessage(tc.ID, content))
				continue
			}

			result, execErr := a.tools.Execute(ctx, tc.Name, tc.Args)
			content := result.Content
			if execErr != nil {
				content = "error: " + execErr.Error()
			} else if result.Error != "" {
				content = "error: " + result.Error
			}

			// PostToolProcessor hook
			result.Content = content
			if err := a.processors.RunPostTool(ctx, tc, &result); err != nil {
				return handleProcessorError(err, totalUsage)
			}

			messages = append(messages, ToolResultMessage(tc.ID, result.Content))
		}
	}

	// Max iterations — force synthesis
	log.Printf("[agent:%s] max iterations reached, forcing synthesis", a.name)
	messages = append(messages, UserMessage(
		"You have used all available tool calls. Summarize what you found and respond to the user."))
	resp, err := a.provider.Chat(ctx, ChatRequest{Messages: messages})
	if err != nil {
		return AgentResult{Usage: totalUsage}, err
	}
	totalUsage.InputTokens += resp.Usage.InputTokens
	totalUsage.OutputTokens += resp.Usage.OutputTokens

	a.mem.persistMessages(ctx, a.name, task, task.Input, resp.Content)
	return AgentResult{Output: resp.Content, Usage: totalUsage}, nil
}

// ExecuteStream runs the tool-calling loop like Execute, but streams the final
// text response into ch. Tool-calling iterations use blocking ChatWithTools;
// only the final response (no tool calls) uses ChatStream. The channel is
// closed when streaming completes.
func (a *LLMAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- string) (AgentResult, error) {
	var totalUsage Usage

	if a.inputHandler != nil {
		ctx = WithInputHandlerContext(ctx, a.inputHandler)
	}

	messages := a.mem.buildMessages(ctx, a.name, a.systemPrompt, task)

	toolDefs := a.tools.AllDefinitions()
	if a.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}

	for i := 0; i < a.maxIter; i++ {
		req := ChatRequest{Messages: messages}

		if err := a.processors.RunPreLLM(ctx, &req); err != nil {
			close(ch)
			return handleProcessorError(err, totalUsage)
		}

		// If there are tools, use blocking ChatWithTools for tool iterations
		if len(toolDefs) > 0 {
			resp, err := a.provider.ChatWithTools(ctx, req, toolDefs)
			if err != nil {
				close(ch)
				return AgentResult{Usage: totalUsage}, err
			}
			totalUsage.InputTokens += resp.Usage.InputTokens
			totalUsage.OutputTokens += resp.Usage.OutputTokens

			if err := a.processors.RunPostLLM(ctx, &resp); err != nil {
				close(ch)
				return handleProcessorError(err, totalUsage)
			}

			// No tool calls — stream the final response
			if len(resp.ToolCalls) == 0 {
				// We already have the full content from ChatWithTools.
				// Stream it as a single chunk since we can't re-request with ChatStream.
				ch <- resp.Content
				close(ch)
				a.mem.persistMessages(ctx, a.name, task, task.Input, resp.Content)
				return AgentResult{Output: resp.Content, Usage: totalUsage}, nil
			}

			// Append assistant message with tool calls
			messages = append(messages, ChatMessage{
				Role:      "assistant",
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			})

			// Execute each tool call (same as Execute)
			for _, tc := range resp.ToolCalls {
				if tc.Name == "ask_user" && a.inputHandler != nil {
					content, err := a.executeAskUser(ctx, tc)
					if err != nil {
						close(ch)
						return AgentResult{Usage: totalUsage}, err
					}
					messages = append(messages, ToolResultMessage(tc.ID, content))
					continue
				}

				result, execErr := a.tools.Execute(ctx, tc.Name, tc.Args)
				content := result.Content
				if execErr != nil {
					content = "error: " + execErr.Error()
				} else if result.Error != "" {
					content = "error: " + result.Error
				}

				result.Content = content
				if err := a.processors.RunPostTool(ctx, tc, &result); err != nil {
					close(ch)
					return handleProcessorError(err, totalUsage)
				}

				messages = append(messages, ToolResultMessage(tc.ID, result.Content))
			}
			continue
		}

		// No tools — stream the response directly via ChatStream
		resp, err := a.provider.ChatStream(ctx, req, ch)
		if err != nil {
			return AgentResult{Usage: totalUsage}, err
		}
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		a.mem.persistMessages(ctx, a.name, task, task.Input, resp.Content)
		return AgentResult{Output: resp.Content, Usage: totalUsage}, nil
	}

	// Max iterations — force synthesis, stream the final response
	log.Printf("[agent:%s] max iterations reached, forcing synthesis", a.name)
	messages = append(messages, UserMessage(
		"You have used all available tool calls. Summarize what you found and respond to the user."))
	resp, err := a.provider.ChatStream(ctx, ChatRequest{Messages: messages}, ch)
	if err != nil {
		return AgentResult{Usage: totalUsage}, err
	}
	totalUsage.InputTokens += resp.Usage.InputTokens
	totalUsage.OutputTokens += resp.Usage.OutputTokens

	a.mem.persistMessages(ctx, a.name, task, task.Input, resp.Content)
	return AgentResult{Output: resp.Content, Usage: totalUsage}, nil
}

// compile-time check: LLMAgent implements StreamingAgent
var _ StreamingAgent = (*LLMAgent)(nil)

// handleProcessorError converts a processor error into an AgentResult.
// ErrHalt produces a graceful result; other errors propagate as failures.
func handleProcessorError(err error, usage Usage) (AgentResult, error) {
	var halt *ErrHalt
	if errors.As(err, &halt) {
		return AgentResult{Output: halt.Response, Usage: usage}, nil
	}
	return AgentResult{Usage: usage}, err
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
func (a *LLMAgent) executeAskUser(ctx context.Context, tc ToolCall) (string, error) {
	var args askUserArgs
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		return "", err
	}

	resp, err := a.inputHandler.RequestInput(ctx, InputRequest{
		Question: args.Question,
		Options:  args.Options,
		Metadata: map[string]string{
			"agent":  a.name,
			"source": "llm",
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Value, nil
}

// compile-time check
var _ Agent = (*LLMAgent)(nil)
