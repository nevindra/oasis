package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
)

// Network is an Agent that coordinates subagents and tools via an LLM router.
// The router sees subagents as callable tools ("agent_<name>") and decides
// which primitives to invoke, in what order, and with what data.
// Optionally supports conversation memory, user memory, and semantic search
// when configured via WithConversationMemory, WithUserMemory, and WithSemanticSearch.
type Network struct {
	name         string
	description  string
	router       Provider
	agents       map[string]Agent // keyed by name
	tools        *ToolRegistry
	processors   *ProcessorChain
	systemPrompt string
	maxIter      int
	inputHandler InputHandler
	mem          agentMemory
}

// NewNetwork creates a Network with the given router provider and options.
func NewNetwork(name, description string, router Provider, opts ...AgentOption) *Network {
	cfg := buildConfig(opts)
	n := &Network{
		name:         name,
		description:  description,
		router:       router,
		agents:       make(map[string]Agent),
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
		n.maxIter = cfg.maxIter
	}
	for _, t := range cfg.tools {
		n.tools.Add(t)
	}
	for _, a := range cfg.agents {
		n.agents[a.Name()] = a
	}
	for _, p := range cfg.processors {
		n.processors.Add(p)
	}
	n.inputHandler = cfg.inputHandler
	return n
}

func (n *Network) Name() string        { return n.name }
func (n *Network) Description() string { return n.description }

// Execute runs the network's routing loop.
func (n *Network) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	var totalUsage Usage

	// Inject InputHandler into context for processors and subagents.
	if n.inputHandler != nil {
		ctx = WithInputHandlerContext(ctx, n.inputHandler)
	}

	// Build tool definitions: agent tools + direct tools
	toolDefs := n.buildToolDefs()

	// Append ask_user tool definition if handler is configured.
	if n.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}

	// Build initial messages (system prompt + user memory + history + user input)
	messages := n.mem.buildMessages(ctx, n.name, n.systemPrompt, task)

	// lastAgentOutput tracks the most recent sub-agent result so we can fall
	// back to it when the router produces an empty final response (common for
	// pure-routing LLMs that don't synthesize a reply after delegating).
	var lastAgentOutput string

	for i := 0; i < n.maxIter; i++ {
		req := ChatRequest{Messages: messages}

		// PreProcessor hook
		if err := n.processors.RunPreLLM(ctx, &req); err != nil {
			return handleProcessorError(err, totalUsage)
		}

		resp, err := n.router.ChatWithTools(ctx, req, toolDefs)
		if err != nil {
			return AgentResult{Usage: totalUsage}, err
		}
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		// PostProcessor hook
		if err := n.processors.RunPostLLM(ctx, &resp); err != nil {
			return handleProcessorError(err, totalUsage)
		}

		// No tool calls — final response
		if len(resp.ToolCalls) == 0 {
			content := resp.Content
			if content == "" {
				content = lastAgentOutput
			}
			n.mem.persistMessages(ctx, n.name, task, task.Input, content)
			return AgentResult{Output: content, Usage: totalUsage}, nil
		}

		// Append assistant message
		messages = append(messages, ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute tool calls in parallel
		results := n.dispatchParallel(ctx, resp.ToolCalls, task)

		// Process results sequentially (PostToolProcessor + message assembly)
		for i, tc := range resp.ToolCalls {
			totalUsage.InputTokens += results[i].usage.InputTokens
			totalUsage.OutputTokens += results[i].usage.OutputTokens

			result := ToolResult{Content: results[i].content}
			if err := n.processors.RunPostTool(ctx, tc, &result); err != nil {
				return handleProcessorError(err, totalUsage)
			}
			messages = append(messages, ToolResultMessage(tc.ID, result.Content))

			// Track the last sub-agent output for fallback.
			if len(tc.Name) > 6 && tc.Name[:6] == "agent_" {
				lastAgentOutput = result.Content
			}
		}
	}

	// Max iterations — force synthesis
	log.Printf("[network:%s] max iterations reached, forcing synthesis", n.name)
	messages = append(messages, UserMessage(
		"You have used all available calls. Summarize what you have and respond to the user."))
	resp, err := n.router.Chat(ctx, ChatRequest{Messages: messages})
	if err != nil {
		return AgentResult{Usage: totalUsage}, err
	}
	totalUsage.InputTokens += resp.Usage.InputTokens
	totalUsage.OutputTokens += resp.Usage.OutputTokens

	n.mem.persistMessages(ctx, n.name, task, task.Input, resp.Content)
	return AgentResult{Output: resp.Content, Usage: totalUsage}, nil
}

// ExecuteStream runs the network's routing loop like Execute, but streams the
// final text response into ch. Tool-calling/routing iterations use blocking
// ChatWithTools; only the final response (no tool calls) is streamed.
// The channel is closed when streaming completes.
func (n *Network) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- string) (AgentResult, error) {
	var totalUsage Usage

	if n.inputHandler != nil {
		ctx = WithInputHandlerContext(ctx, n.inputHandler)
	}

	toolDefs := n.buildToolDefs()
	if n.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}

	messages := n.mem.buildMessages(ctx, n.name, n.systemPrompt, task)

	// lastAgentOutput tracks the most recent sub-agent result so we can fall
	// back to it when the router produces an empty final response (common for
	// pure-routing LLMs that don't synthesize a reply after delegating).
	var lastAgentOutput string

	for i := 0; i < n.maxIter; i++ {
		req := ChatRequest{Messages: messages}

		if err := n.processors.RunPreLLM(ctx, &req); err != nil {
			close(ch)
			return handleProcessorError(err, totalUsage)
		}

		resp, err := n.router.ChatWithTools(ctx, req, toolDefs)
		if err != nil {
			close(ch)
			return AgentResult{Usage: totalUsage}, err
		}
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		if err := n.processors.RunPostLLM(ctx, &resp); err != nil {
			close(ch)
			return handleProcessorError(err, totalUsage)
		}

		// No tool calls — stream the final response
		if len(resp.ToolCalls) == 0 {
			content := resp.Content
			if content == "" {
				content = lastAgentOutput
			}
			ch <- content
			close(ch)
			n.mem.persistMessages(ctx, n.name, task, task.Input, content)
			return AgentResult{Output: content, Usage: totalUsage}, nil
		}

		messages = append(messages, ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		results := n.dispatchParallel(ctx, resp.ToolCalls, task)

		for i, tc := range resp.ToolCalls {
			totalUsage.InputTokens += results[i].usage.InputTokens
			totalUsage.OutputTokens += results[i].usage.OutputTokens

			result := ToolResult{Content: results[i].content}
			if err := n.processors.RunPostTool(ctx, tc, &result); err != nil {
				close(ch)
				return handleProcessorError(err, totalUsage)
			}
			messages = append(messages, ToolResultMessage(tc.ID, result.Content))

			// Track the last sub-agent output for fallback.
			if len(tc.Name) > 6 && tc.Name[:6] == "agent_" {
				lastAgentOutput = result.Content
			}
		}
	}

	// Max iterations — force synthesis, stream the response
	log.Printf("[network:%s] max iterations reached, forcing synthesis", n.name)
	messages = append(messages, UserMessage(
		"You have used all available calls. Summarize what you have and respond to the user."))
	resp, err := n.router.ChatStream(ctx, ChatRequest{Messages: messages}, ch)
	if err != nil {
		return AgentResult{Usage: totalUsage}, err
	}
	totalUsage.InputTokens += resp.Usage.InputTokens
	totalUsage.OutputTokens += resp.Usage.OutputTokens

	n.mem.persistMessages(ctx, n.name, task, task.Input, resp.Content)
	return AgentResult{Output: resp.Content, Usage: totalUsage}, nil
}

// compile-time check: Network implements StreamingAgent
var _ StreamingAgent = (*Network)(nil)

// dispatch routes a tool call to either a subagent, ask_user, or a direct tool.
func (n *Network) dispatch(ctx context.Context, tc ToolCall, parentTask AgentTask) (string, Usage) {
	// Special case: ask_user tool
	if tc.Name == "ask_user" && n.inputHandler != nil {
		var args askUserArgs
		if err := json.Unmarshal(tc.Args, &args); err != nil {
			return "error: invalid ask_user args: " + err.Error(), Usage{}
		}
		resp, err := n.inputHandler.RequestInput(ctx, InputRequest{
			Question: args.Question,
			Options:  args.Options,
			Metadata: map[string]string{
				"agent":  n.name,
				"source": "llm",
			},
		})
		if err != nil {
			return "error: " + err.Error(), Usage{}
		}
		return resp.Value, Usage{}
	}

	// Check if it's an agent call (prefixed with "agent_")
	if len(tc.Name) > 6 && tc.Name[:6] == "agent_" {
		agentName := tc.Name[6:]
		agent, ok := n.agents[agentName]
		if !ok {
			return fmt.Sprintf("error: unknown agent %q", agentName), Usage{}
		}

		var params struct {
			Task string `json:"task"`
		}
		if err := json.Unmarshal(tc.Args, &params); err != nil {
			return "error: invalid agent call args: " + err.Error(), Usage{}
		}

		log.Printf("[network:%s] -> agent_%s: %s", n.name, agentName, truncateStr(params.Task, 80))

		result, err := agent.Execute(ctx, AgentTask{
			Input:   params.Task,
			Attachments: parentTask.Attachments,
			Context: parentTask.Context,
		})
		if err != nil {
			return "error: " + err.Error(), Usage{}
		}
		return result.Output, result.Usage
	}

	// Regular tool call
	result, err := n.tools.Execute(ctx, tc.Name, tc.Args)
	if err != nil {
		return "error: " + err.Error(), Usage{}
	}
	if result.Error != "" {
		return "error: " + result.Error, Usage{}
	}
	return result.Content, Usage{}
}

// dispatchParallel runs all tool calls concurrently via dispatch and returns
// results in the same order as the input calls.
func (n *Network) dispatchParallel(ctx context.Context, calls []ToolCall, task AgentTask) []toolExecResult {
	results := make([]toolExecResult, len(calls))
	var wg sync.WaitGroup

	for i, tc := range calls {
		wg.Add(1)
		go func(idx int, tc ToolCall) {
			defer wg.Done()
			content, usage := n.dispatch(ctx, tc, task)
			results[idx] = toolExecResult{content: content, usage: usage}
		}(i, tc)
	}

	wg.Wait()
	return results
}

// buildToolDefs builds tool definitions from subagents and direct tools.
func (n *Network) buildToolDefs() []ToolDefinition {
	var defs []ToolDefinition

	// Agent tool definitions
	for name, agent := range n.agents {
		defs = append(defs, ToolDefinition{
			Name:        "agent_" + name,
			Description: agent.Description(),
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"task":{"type":"string","description":"Natural language description of the task to delegate to this agent"}},"required":["task"]}`,
			),
		})
	}

	// Direct tool definitions
	defs = append(defs, n.tools.AllDefinitions()...)
	return defs
}

// truncateStr truncates a string to n characters.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// compile-time check
var _ Agent = (*Network)(nil)
