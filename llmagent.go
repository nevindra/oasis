package oasis

import (
	"context"
	"log"
)

const defaultMaxIter = 10

// LLMAgent is an Agent that uses an LLM with tools to complete tasks.
type LLMAgent struct {
	name         string
	description  string
	provider     Provider
	tools        *ToolRegistry
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
		systemPrompt: cfg.prompt,
		maxIter:      defaultMaxIter,
	}
	if cfg.maxIter > 0 {
		a.maxIter = cfg.maxIter
	}
	for _, t := range cfg.tools {
		a.tools.Add(t)
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
			messages = append(messages, ToolResultMessage(tc.ID, content))
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

// compile-time check
var _ Agent = (*LLMAgent)(nil)
