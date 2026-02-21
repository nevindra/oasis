package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// Network is an Agent that coordinates subagents and tools via an LLM router.
// The router sees subagents as callable tools ("agent_<name>") and decides
// which primitives to invoke, in what order, and with what data.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type Network struct {
	name          string
	description   string
	router        Provider
	agents            map[string]Agent // keyed by name
	sortedAgentNames  []string         // pre-sorted for deterministic tool ordering
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
			store:             cfg.store,
			embedding:         cfg.embedding,
			memory:            cfg.memory,
			crossThreadSearch: cfg.crossThreadSearch,
			semanticMinScore:  cfg.semanticMinScore,
			maxHistory:        cfg.maxHistory,
			maxTokens:         cfg.maxTokens,
			autoTitle:         cfg.autoTitle,
			provider:          router,
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
		n.sortedAgentNames = append(n.sortedAgentNames, a.Name())
	}
	sort.Strings(n.sortedAgentNames)
	for _, p := range cfg.processors {
		n.processors.Add(p)
	}
	n.inputHandler = cfg.inputHandler
	n.planExecution = cfg.planExecution
	n.codeRunner = cfg.codeRunner
	n.responseSchema = cfg.responseSchema
	n.dynamicPrompt = cfg.dynamicPrompt
	n.dynamicModel = cfg.dynamicModel
	n.dynamicTools = cfg.dynamicTools
	n.tracer = cfg.tracer
	n.logger = cfg.logger
	n.mem.tracer = cfg.tracer
	n.mem.logger = cfg.logger
	return n
}

func (n *Network) Name() string        { return n.name }
func (n *Network) Description() string { return n.description }

// Execute runs the network's routing loop.
func (n *Network) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return n.executeWithSpan(ctx, task, nil)
}

// ExecuteStream runs the network's routing loop like Execute, but emits
// StreamEvent values into ch throughout execution. Events include text deltas,
// tool call start/result, and agent start/finish for subagent delegation.
// The channel is closed when streaming completes.
func (n *Network) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return n.executeWithSpan(ctx, task, ch)
}

// executeWithSpan wraps runLoop with an agent.execute span when a tracer is configured.
func (n *Network) executeWithSpan(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	// Emit input-received event so consumers know a task arrived.
	if ch != nil {
		ch <- StreamEvent{Type: EventInputReceived, Name: n.name, Content: task.Input}
	}

	if n.tracer != nil {
		var span Span
		ctx, span = n.tracer.Start(ctx, "agent.execute",
			StringAttr("agent.name", n.name),
			StringAttr("agent.type", "Network"))
		defer span.End()

		n.logger.Info("network started", "network", n.name)
		result, err := runLoop(ctx, n.buildLoopConfig(ctx, task, ch), task, ch)

		span.SetAttr(
			IntAttr("tokens.input", result.Usage.InputTokens),
			IntAttr("tokens.output", result.Usage.OutputTokens))
		if err != nil {
			span.Error(err)
			span.SetAttr(StringAttr("agent.status", "error"))
		} else {
			span.SetAttr(StringAttr("agent.status", "ok"))
		}
		n.logger.Info("network completed", "network", n.name,
			"status", statusStr(err),
			"tokens.input", result.Usage.InputTokens,
			"tokens.output", result.Usage.OutputTokens)
		return result, err
	}
	return runLoop(ctx, n.buildLoopConfig(ctx, task, ch), task, ch)
}

// buildLoopConfig wires Network fields into a loopConfig for runLoop.
// Resolves dynamic prompt, model, and tools when configured.
// ch is passed through so makeDispatch can emit agent-start/finish events.
func (n *Network) buildLoopConfig(ctx context.Context, task AgentTask, ch chan<- StreamEvent) loopConfig {
	// Resolve prompt: dynamic > static
	prompt := n.systemPrompt
	if n.dynamicPrompt != nil {
		prompt = n.dynamicPrompt(ctx, task)
	}

	// Resolve provider: dynamic > construction-time
	router := n.router
	if n.dynamicModel != nil {
		router = n.dynamicModel(ctx, task)
	}

	// Resolve tools: dynamic replaces static
	registry := n.tools
	if n.dynamicTools != nil {
		registry = NewToolRegistry()
		for _, t := range n.dynamicTools(ctx, task) {
			registry.Add(t)
		}
	}

	toolDefs := n.buildToolDefs(registry)
	if n.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}
	if n.planExecution {
		toolDefs = append(toolDefs, executePlanToolDef)
	}
	if n.codeRunner != nil {
		toolDefs = append(toolDefs, executeCodeToolDef)
	}
	return loopConfig{
		name:           "network:" + n.name,
		provider:       router,
		tools:          toolDefs,
		processors:     n.processors,
		maxIter:        n.maxIter,
		mem:            &n.mem,
		inputHandler:   n.inputHandler,
		dispatch:       n.makeDispatch(task, ch, registry),
		systemPrompt:   prompt,
		responseSchema: n.responseSchema,
		tracer:         n.tracer,
		logger:         n.logger,
	}
}

// makeDispatch returns a DispatchFunc that routes tool calls to subagents,
// the ask_user handler, or direct tools. When ch is non-nil, agent-start
// and agent-finish events are emitted for subagent delegation.
func (n *Network) makeDispatch(parentTask AgentTask, ch chan<- StreamEvent, registry *ToolRegistry) DispatchFunc {
	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) (string, Usage) {
		// Special case: ask_user tool
		if tc.Name == "ask_user" && n.inputHandler != nil {
			content, err := executeAskUser(ctx, n.inputHandler, n.name, tc)
			if err != nil {
				return "error: " + err.Error(), Usage{}
			}
			return content, Usage{}
		}

		// Special case: execute_plan tool
		if tc.Name == "execute_plan" && n.planExecution {
			return executePlan(ctx, tc.Args, dispatch)
		}

		// Special case: execute_code tool
		if tc.Name == "execute_code" && n.codeRunner != nil {
			return executeCode(ctx, tc.Args, n.codeRunner, dispatch)
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

			n.logger.Info("delegating to subagent", "network", n.name, "agent", agentName, "task", truncateStr(params.Task, 80))

			if ch != nil {
				ch <- StreamEvent{Type: EventAgentStart, Name: agentName, Content: params.Task}
			}

			subTask := AgentTask{
				Input:       params.Task,
				Attachments: parentTask.Attachments,
				Context:     parentTask.Context,
			}

			start := time.Now()

			var result AgentResult
			var err error

			// When streaming and the subagent supports it, delegate via
			// ExecuteStream so tokens flow through the parent channel
			// in real time instead of arriving as one big chunk.
			if ch != nil {
				if sa, ok := agent.(StreamingAgent); ok {
					subCh := make(chan StreamEvent, 64)
					done := make(chan struct{})
					go func() {
						defer close(done)
						for ev := range subCh {
							ch <- ev
						}
					}()
					result, err = sa.ExecuteStream(ctx, subTask, subCh)
					<-done // wait for all forwarded events
				} else {
					result, err = agent.Execute(ctx, subTask)
				}
			} else {
				result, err = agent.Execute(ctx, subTask)
			}

			elapsed := time.Since(start)

			if ch != nil {
				output := ""
				if err == nil {
					output = result.Output
				}
				ch <- StreamEvent{
					Type:     EventAgentFinish,
					Name:     agentName,
					Content:  output,
					Usage:    result.Usage,
					Duration: elapsed,
				}
			}

			if err != nil {
				return "error: " + err.Error(), Usage{}
			}
			return result.Output, result.Usage
		}

		// Regular tool call
		result, err := registry.Execute(ctx, tc.Name, tc.Args)
		if err != nil {
			return "error: " + err.Error(), Usage{}
		}
		if result.Error != "" {
			return "error: " + result.Error, Usage{}
		}
		return result.Content, Usage{}
	}
	return dispatch
}

// buildToolDefs builds tool definitions from subagents and the given tool registry.
// Agent tools use pre-sorted names for deterministic ordering across calls.
func (n *Network) buildToolDefs(registry *ToolRegistry) []ToolDefinition {
	var defs []ToolDefinition

	// Agent tool definitions (order fixed at construction time).
	for _, name := range n.sortedAgentNames {
		defs = append(defs, ToolDefinition{
			Name:        "agent_" + name,
			Description: n.agents[name].Description(),
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"task":{"type":"string","description":"The user's original message, copied verbatim. Do not paraphrase, translate, or summarize."}},"required":["task"]}`,
			),
		})
	}

	// Direct tool definitions
	defs = append(defs, registry.AllDefinitions()...)
	return defs
}

// compile-time checks
var (
	_ Agent          = (*Network)(nil)
	_ StreamingAgent = (*Network)(nil)
)
