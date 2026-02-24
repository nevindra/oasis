package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Network is an Agent that coordinates subagents and tools via an LLM router.
// The router sees subagents as callable tools ("agent_<name>") and decides
// which primitives to invoke, in what order, and with what data.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type Network struct {
	name             string
	description      string
	router           Provider
	agents           map[string]Agent // keyed by name
	sortedAgentNames []string         // pre-sorted for deterministic tool ordering
	tools            *ToolRegistry
	processors       *ProcessorChain
	systemPrompt     string
	maxIter          int
	inputHandler     InputHandler
	planExecution    bool
	codeRunner       CodeRunner
	responseSchema   *ResponseSchema
	dynamicPrompt    PromptFunc
	dynamicModel     ModelFunc
	dynamicTools     ToolsFunc
	tracer             Tracer
	logger             *slog.Logger
	mem                agentMemory
	cachedToolDefs      []ToolDefinition // computed once at construction for the non-dynamic path
	maxAttachmentBytes  int64
	suspendCount        atomic.Int64
	suspendBytes        atomic.Int64
	maxSuspendSnapshots int
	maxSuspendBytes     int64
	compressModel       ModelFunc
	compressThreshold   int
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
	n.maxAttachmentBytes = cfg.maxAttachmentBytes
	n.maxSuspendSnapshots = cfg.maxSuspendSnapshots
	n.maxSuspendBytes = cfg.maxSuspendBytes
	n.compressModel = cfg.compressModel
	n.compressThreshold = cfg.compressThreshold
	n.mem.tracer = cfg.tracer
	n.mem.logger = cfg.logger

	// Pre-compute tool definitions for the non-dynamic path.
	// Includes agent tools + direct tools + built-in tools.
	if n.dynamicTools == nil {
		n.cachedToolDefs = n.buildToolDefs(n.tools.AllDefinitions())
		if n.inputHandler != nil {
			n.cachedToolDefs = append(n.cachedToolDefs, askUserToolDef)
		}
		if n.planExecution {
			n.cachedToolDefs = append(n.cachedToolDefs, executePlanToolDef)
		}
		if n.codeRunner != nil {
			n.cachedToolDefs = append(n.cachedToolDefs, executeCodeToolDef)
		}
	}

	return n
}

func (n *Network) Name() string        { return n.name }
func (n *Network) Description() string { return n.description }

// Drain waits for all in-flight background persist goroutines to finish.
// Call during shutdown to ensure the last messages are written to the store.
func (n *Network) Drain() { n.mem.drain() }

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
		select {
		case ch <- StreamEvent{Type: EventInputReceived, Name: n.name, Content: task.Input}:
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
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

	// Resolve tools: dynamic replaces static.
	// When dynamicTools is set, build definitions and an index per-request.
	// Otherwise, use the cached definitions computed at construction time.
	var toolDefs []ToolDefinition
	var executeTool toolExecFunc
	if n.dynamicTools != nil {
		dynTools := n.dynamicTools(ctx, task)
		var rawToolDefs []ToolDefinition
		index := make(map[string]Tool, len(dynTools))
		for _, t := range dynTools {
			for _, d := range t.Definitions() {
				rawToolDefs = append(rawToolDefs, d)
				index[d.Name] = t
			}
		}
		executeTool = func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
			if t, ok := index[name]; ok {
				return t.Execute(ctx, name, args)
			}
			return ToolResult{Error: "unknown tool: " + name}, nil
		}
		toolDefs = n.buildToolDefs(rawToolDefs)
		if n.inputHandler != nil {
			toolDefs = append(toolDefs, askUserToolDef)
		}
		if n.planExecution {
			toolDefs = append(toolDefs, executePlanToolDef)
		}
		if n.codeRunner != nil {
			toolDefs = append(toolDefs, executeCodeToolDef)
		}
	} else {
		toolDefs = n.cachedToolDefs
		executeTool = n.tools.Execute
	}
	return loopConfig{
		name:               "network:" + n.name,
		provider:           router,
		tools:              toolDefs,
		processors:         n.processors,
		maxIter:            n.maxIter,
		mem:                &n.mem,
		inputHandler:       n.inputHandler,
		dispatch:           n.makeDispatch(task, ch, executeTool),
		systemPrompt:       prompt,
		responseSchema:     n.responseSchema,
		tracer:              n.tracer,
		logger:              n.logger,
		maxAttachmentBytes:  n.maxAttachmentBytes,
		suspendCount:        &n.suspendCount,
		suspendBytes:        &n.suspendBytes,
		maxSuspendSnapshots: n.maxSuspendSnapshots,
		maxSuspendBytes:     n.maxSuspendBytes,
		compressModel:       n.compressModel,
		compressThreshold:   n.compressThreshold,
	}
}

// makeDispatch returns a DispatchFunc that routes tool calls to subagents,
// the shared built-in tools, or direct tools. When ch is non-nil, agent-start
// and agent-finish events are emitted for subagent delegation.
func (n *Network) makeDispatch(parentTask AgentTask, ch chan<- StreamEvent, executeTool toolExecFunc) DispatchFunc {
	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
		// Built-in tools: ask_user, execute_plan, execute_code.
		if r, ok := dispatchBuiltins(ctx, tc, dispatch, n.inputHandler, n.name, n.planExecution, n.codeRunner); ok {
			return r
		}

		// Check if it's an agent call (prefixed with "agent_")
		const agentPrefix = "agent_"
		if strings.HasPrefix(tc.Name, agentPrefix) {
			agentName := tc.Name[len(agentPrefix):]
			agent, ok := n.agents[agentName]
			if !ok {
				return DispatchResult{Content: fmt.Sprintf("error: unknown agent %q", agentName), IsError: true}
			}

			var params struct {
				Task string `json:"task"`
			}
			if err := json.Unmarshal(tc.Args, &params); err != nil {
				return DispatchResult{Content: "error: invalid agent call args: " + err.Error(), IsError: true}
			}

			n.logger.Info("delegating to subagent", "network", n.name, "agent", agentName, "task", truncateStr(params.Task, 80))

			if ch != nil {
				select {
				case ch <- StreamEvent{Type: EventAgentStart, Name: agentName, Content: params.Task}:
				case <-ctx.Done():
					return DispatchResult{Content: ctx.Err().Error(), IsError: true}
				}
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
					var subChOnce sync.Once
					safeCloseSubCh := func() {
						subChOnce.Do(func() {
							defer func() { recover() }()
							close(subCh)
						})
					}
					go func() {
						defer close(done)
						for ev := range subCh {
							// Filter EventInputReceived from sub-agents — Network's
							// EventAgentStart is the canonical signal for delegation.
							if ev.Type == EventInputReceived {
								continue
							}
							select {
							case ch <- ev:
							case <-ctx.Done():
								// Drain in background so this goroutine releases
								// references to ch and done promptly, even if
								// ExecuteStream is slow to close subCh.
								// Timeout prevents the drain goroutine from leaking
								// if the subagent ignores context cancellation.
								go func() {
									timeout := time.NewTimer(60 * time.Second)
									defer timeout.Stop()
									for {
										select {
										case _, ok := <-subCh:
											if !ok {
												return
											}
										case <-timeout.C:
											n.logger.Warn("subagent stream drain timed out, closing subCh", "network", n.name, "agent", agentName)
											// Close subCh so a misbehaving ExecuteStream
											// that ignores context cancellation panics on
											// its next send. The panic is caught by the
											// recover wrapper around ExecuteStream,
											// preventing a permanent goroutine leak.
											safeCloseSubCh()
											return
										}
									}
								}()
								return
							}
						}
					}()
					func() {
						defer func() {
							if p := recover(); p != nil {
								safeCloseSubCh() // unblock forwarding goroutine (safe if already closed)
								result = AgentResult{}
								err = fmt.Errorf("subagent %q panic: %v", agentName, p)
							}
						}()
						result, err = sa.ExecuteStream(ctx, subTask, subCh)
					}()
					<-done // wait for all forwarded events
				} else {
					func() {
						defer func() {
							if p := recover(); p != nil {
								result = AgentResult{}
								err = fmt.Errorf("subagent %q panic: %v", agentName, p)
							}
						}()
						result, err = agent.Execute(ctx, subTask)
					}()
				}
			} else {
				func() {
					defer func() {
						if p := recover(); p != nil {
							result = AgentResult{}
							err = fmt.Errorf("subagent %q panic: %v", agentName, p)
						}
					}()
					result, err = agent.Execute(ctx, subTask)
				}()
			}

			elapsed := time.Since(start)

			if ch != nil {
				output := ""
				if err == nil {
					output = result.Output
				}
				select {
				case ch <- StreamEvent{
					Type:     EventAgentFinish,
					Name:     agentName,
					Content:  output,
					Usage:    result.Usage,
					Duration: elapsed,
				}:
				case <-ctx.Done():
				}
			}

			if err != nil {
				return DispatchResult{Content: "error: " + err.Error(), IsError: true}
			}
			return DispatchResult{Content: result.Output, Usage: result.Usage, Attachments: result.Attachments}
		}

		// Regular tool call.
		return dispatchTool(ctx, executeTool, tc.Name, tc.Args)
	}
	return dispatch
}

// buildToolDefs builds tool definitions from subagents and the given tool definitions.
// Agent tools use pre-sorted names for deterministic ordering across calls.
func (n *Network) buildToolDefs(toolDefs []ToolDefinition) []ToolDefinition {
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
	defs = append(defs, toolDefs...)
	return defs
}

// compile-time checks
var (
	_ Agent          = (*Network)(nil)
	_ StreamingAgent = (*Network)(nil)
)
