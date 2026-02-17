package oasis

import (
	"context"
	"errors"
	"log"
)

const defaultMaxIter = 10

// LLMAgent is an Agent that uses an LLM with tools to complete tasks.
type LLMAgent struct {
	name         string
	description  string
	provider     Provider
	tools        *ToolRegistry
	processors   *ProcessorChain
	systemPrompt string
	maxIter      int
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
	return a
}

func (a *LLMAgent) Name() string        { return a.name }
func (a *LLMAgent) Description() string { return a.description }

// Execute runs the tool-calling loop until the LLM produces a final text response.
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	var totalUsage Usage

	// Build initial messages
	var messages []ChatMessage
	if a.systemPrompt != "" {
		messages = append(messages, SystemMessage(a.systemPrompt))
	}
	messages = append(messages, UserMessage(task.Input))

	toolDefs := a.tools.AllDefinitions()

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

	return AgentResult{Output: resp.Content, Usage: totalUsage}, nil
}

// handleProcessorError converts a processor error into an AgentResult.
// ErrHalt produces a graceful result; other errors propagate as failures.
func handleProcessorError(err error, usage Usage) (AgentResult, error) {
	var halt *ErrHalt
	if errors.As(err, &halt) {
		return AgentResult{Output: halt.Response, Usage: usage}, nil
	}
	return AgentResult{Usage: usage}, err
}

// compile-time check
var _ Agent = (*LLMAgent)(nil)
