package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/internal/runtime"
	"github.com/nevindra/oasis/memory"
)

// LLMAgent is an Agent that uses an LLM with tools to complete tasks.
// Optionally supports conversation memory and cross-thread search when
// configured via WithMemory and memory.WithSemanticRecall.
type LLMAgent struct {
	runtime.Runtime

	// wrapped is the lazily-built middleware chain around executeRaw.
	// wrappedOnce ensures the chain is built exactly once even under concurrent
	// Execute calls. Both are zero-value safe: when no middlewares are set,
	// Execute delegates directly to executeRaw without touching these fields.
	wrapped     core.Agent
	wrappedOnce sync.Once

	// Cached dispatch for the non-streaming path (ch == nil). Built lazily on first Execute.
	cachedNonStreamDispatch     DispatchFunc
	cachedNonStreamDispatchOnce sync.Once
}

// New constructs an LLMAgent — a single-brain Agent with one provider and
// zero or more tools. For multi-agent orchestration, use network.New.
func New(name, description string, provider core.Provider, opts ...AgentOption) *LLMAgent {
	cfg := BuildConfig(opts)
	a := &LLMAgent{}
	runtime.Init(&a.Runtime, name, description, provider, cfg)

	// Pre-compute tool definitions for the non-dynamic path.
	// Avoids rebuilding the slice on every Execute call.
	if !a.HasDynamicTools() {
		askDef := askUserToolDef()
		planDef := executePlanToolDef()
		a.SetCachedToolDefs(a.CacheBuiltinToolDefs(a.Tools().AllDefinitions(), &askDef, &planDef))
	}

	return a
}

// Memory returns the agent's memory handle. Use this to call Remember, Recall,
// Forget, List, Get, Pin directly from application code. The returned pointer
// is always non-nil; methods on a zero AgentMemory (when WithMemory was not
// configured) safely no-op.
func (a *LLMAgent) Memory() *memory.AgentMemory { return a.Runtime.Memory() }

// Execute runs the tool-calling loop until the LLM produces a final text response.
// Optional RunOption values configure per-call behaviour (streaming, deadline, overrides).
// When WithMiddleware was used at construction time, the registered middlewares
// wrap this call. The chain is built lazily on the first Execute call and cached.
func (a *LLMAgent) Execute(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error) {
	if len(a.AgentMiddleware) == 0 {
		return a.executeRaw(ctx, task, opts...)
	}
	// Why: build the wrapped chain once and cache it. sync.Once ensures
	// concurrent Execute calls don't race on construction.
	a.wrappedOnce.Do(func() {
		var inner core.Agent = (*executeRawProxy)(a)
		for i := len(a.AgentMiddleware) - 1; i >= 0; i-- {
			inner = a.AgentMiddleware[i](inner)
		}
		a.wrapped = inner
	})
	return a.wrapped.Execute(ctx, task, opts...)
}

// executeRaw is the real implementation of Execute without middleware wrapping.
// Middleware wrappers call back into this via executeRawProxy.
func (a *LLMAgent) executeRaw(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error) {
	rcfg := core.ApplyRunOptions(opts...)
	var ro *RunOptions
	if rcfg.Overrides != nil {
		if v, ok := rcfg.Overrides.(*RunOptions); ok {
			ro = v
		}
	}
	if err := ro.Validate(); err != nil {
		if rcfg.Stream != nil {
			close(rcfg.Stream)
		}
		return AgentResult{}, err
	}
	if rcfg.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rcfg.Deadline)
		defer cancel()
	}
	ctx = WithTaskContext(ctx, task)
	res, err := a.ExecuteWithSpan(ctx, task, rcfg.Stream, "LLMAgent", "agent",
		func(ctx context.Context, task AgentTask, ch chan<- core.StreamEvent) *LoopConfig {
			return a.buildLoopConfig(ctx, task, ch, ro)
		},
		runLoop,
	)
	// Score the assembled result on success. Inline scorers mutate res.Scores;
	// async scorers are submitted to the bounded pool. No-op when none attached.
	if err == nil && a.HasScorers() {
		res = a.RunScorers(ctx, task.Input, res)
	}
	return res, err
}

// executeRawProxy wraps *LLMAgent so middleware sees a core.Agent interface
// that calls executeRaw rather than Execute (which would recurse through
// the middleware chain again).
type executeRawProxy LLMAgent

func (p *executeRawProxy) Name() string        { return (*LLMAgent)(p).Name() }
func (p *executeRawProxy) Description() string { return (*LLMAgent)(p).Description() }
func (p *executeRawProxy) Execute(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error) {
	return (*LLMAgent)(p).executeRaw(ctx, task, opts...)
}

// buildLoopConfig wires LLMAgent fields into a LoopConfig for runLoop.
// Used by both Execute / ExecuteStream (opts = nil → agent defaults) and
// ExecuteWith / ExecuteStreamWith (opts != nil → per-call overrides).
func (a *LLMAgent) buildLoopConfig(ctx context.Context, task AgentTask, ch chan<- core.StreamEvent, opts *RunOptions) *LoopConfig {
	cfg := a.ApplyRunOptions(opts)
	prompt, provider := a.ResolvePromptAndProviderWith(ctx, task, cfg)
	askDef := askUserToolDef()
	planDef := executePlanToolDef()
	toolDefs, executeTool, executeToolStream, isStreamingTool := a.ResolveTools(ctx, task, nil, &askDef, &planDef)

	var dispatch DispatchFunc
	if ch == nil && !a.HasDynamicTools() && len(cfg.ToolPolicies) == 0 && len(cfg.ToolPolicyMatchers) == 0 {
		a.cachedNonStreamDispatchOnce.Do(func() {
			a.cachedNonStreamDispatch = a.makeDispatch(executeTool, executeToolStream, nil, toolDefs, isStreamingTool, cfg)
		})
		dispatch = a.cachedNonStreamDispatch
	} else {
		dispatch = a.makeDispatch(executeTool, executeToolStream, ch, toolDefs, isStreamingTool, cfg)
	}

	lc := runtime.AcquireLoopConfig()
	*lc = a.BaseLoopConfig("agent:"+a.Name(), prompt, provider, toolDefs, dispatch, cfg, a.ResolveMem(opts))
	return lc
}

// makeDispatch returns a DispatchFunc that executes tools via the given
// executor function and handles the ask_user and execute_plan special cases
// via the shared DispatchBuiltins method.
// When executeToolStream and ch are non-nil, tools implementing StreamingAnyTool
// emit progress events during execution.
func (a *LLMAgent) makeDispatch(executeTool ToolExecFunc, executeToolStream ToolExecStreamFunc, ch chan<- core.StreamEvent, resolvedToolDefs []core.ToolDefinition, isStreamingTool func(string) bool, cfg *Config) DispatchFunc {
	// Wrap DispatchBuiltins to inject the ask_user and execute_plan callbacks,
	// breaking the runtime→agent cycle.
	builtins := func(ctx context.Context, tc core.ToolCall, dispatch DispatchFunc) (DispatchResult, bool) {
		return a.DispatchBuiltins(ctx, tc, dispatch, executeAskUser, executePlan)
	}
	return NewStandardDispatch(StandardDispatchConfig{
		Builtins:          builtins,
		ExecuteTool:       executeTool,
		ExecuteToolStream: executeToolStream,
		ResolvedToolDefs:  resolvedToolDefs,
		StreamCh:          ch,
		ResolvePolicy:     cfg.ResolveToolPolicy,
		IsStreamingTool:   isStreamingTool,
		Logger:            cfg.Logger,
	})
}

// compile-time check
var _ core.Agent = (*LLMAgent)(nil)

// --- execute_plan tool ---

// Why: DeriveSchema is reflection-based. Cache the result at package init so
// the dynamic-tools path (which bypasses LLMAgent.cachedToolDefs) doesn't
// re-run reflection on every Execute call. The schemas of askUserArgs and
// planArgs are structurally invariant — they never depend on runtime state.
var (
	askUserSchema     = core.DeriveSchema[askUserArgs]()
	executePlanSchema = core.DeriveSchema[planArgs]()
)

// executePlanToolDef returns the tool definition for the built-in
// execute_plan tool. The schema is pre-derived at package init.
func executePlanToolDef() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        core.ToolExecutePlan,
		Description: "Execute multiple tool calls in a single batch without intermediate reasoning. Use when you need to call tools multiple times with known inputs upfront. All steps run in parallel. Returns structured results per step.",
		Parameters:  executePlanSchema,
	}
}

// planArgs is the parsed arguments for the execute_plan tool call.
type planArgs struct {
	Steps []planStep `json:"steps" describe:"Array of tool calls to execute in parallel"`
}

// planStep is a single step in an execute_plan call.
type planStep struct {
	Tool string          `json:"tool" describe:"Name of the tool to call"`
	Args json.RawMessage `json:"args" describe:"Arguments for the tool"`
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

// defaultMaxParallelDispatch is the fallback parallel-dispatch limit used by
// executePlan when the caller passes 0 (no per-agent limit set).
const defaultMaxParallelDispatch = 10

// ExecutePlan is the exported alias for executePlan.
// Network uses it as a callback to DispatchBuiltins.
//
// Stability: runtime-integration export shared with the network subpackage;
// excluded from the v1.x compatibility promise (may change or move to internal).
var ExecutePlan = executePlan

// executePlan handles the execute_plan tool call by parsing steps,
// executing them in parallel via the given dispatch function, and
// returning aggregated results as JSON. Shared by LLMAgent and Network.
func executePlan(ctx context.Context, args json.RawMessage, dispatch DispatchFunc, planStepsLimit, parallelLimit int) DispatchResult {
	if planStepsLimit == 0 {
		planStepsLimit = maxPlanSteps
	}
	if parallelLimit == 0 {
		parallelLimit = defaultMaxParallelDispatch
	}
	var params planArgs
	if err := json.Unmarshal(args, &params); err != nil {
		return DispatchResult{Content: "error: invalid execute_plan args: " + err.Error(), IsError: true}
	}
	if len(params.Steps) == 0 {
		return DispatchResult{Content: "error: execute_plan requires at least one step", IsError: true}
	}
	if len(params.Steps) > planStepsLimit {
		return DispatchResult{Content: fmt.Sprintf("error: execute_plan limited to %d steps, got %d", planStepsLimit, len(params.Steps)), IsError: true}
	}

	// Build tool calls, preventing recursion.
	calls := make([]core.ToolCall, len(params.Steps))
	for i, step := range params.Steps {
		if step.Tool == core.ToolExecutePlan {
			return DispatchResult{Content: "error: execute_plan steps cannot call execute_plan", IsError: true}
		}
		calls[i] = core.ToolCall{
			ID:   "plan_step_" + strconv.Itoa(i),
			Name: step.Tool,
			Args: step.Args,
		}
	}

	// Wrap dispatch to block ask_user inside parallel plan steps.
	// Most InputHandler implementations aren't designed for concurrent
	// invocation, and simultaneous user prompts are confusing.
	safeDispatch := func(ctx context.Context, tc core.ToolCall) DispatchResult {
		if tc.Name == core.ToolAskUser {
			return DispatchResult{Content: "error: ask_user cannot be called from within execute_plan", IsError: true}
		}
		return dispatch(ctx, tc)
	}

	// Execute all steps in parallel.
	results := dispatchParallel(ctx, calls, safeDispatch, parallelLimit)

	// Aggregate results.
	var totalUsage core.Usage
	var allAttachments []core.Attachment
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

// --- ask_user tool ---

// askUserToolDef returns the tool definition for the built-in ask_user tool.
// The schema is pre-derived at package init (see askUserSchema).
func askUserToolDef() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        core.ToolAskUser,
		Description: "Ask the user a question when you need clarification, confirmation, or additional information to proceed.",
		Parameters:  askUserSchema,
	}
}

// askUserArgs is the parsed arguments for the ask_user tool call.
type askUserArgs struct {
	Question    string   `json:"question" describe:"The question to ask the user"`
	Options     []string `json:"options,omitempty" describe:"Optional suggested answers for the user to choose from"`
	MultiSelect bool     `json:"multi_select,omitempty" describe:"Set true to let the user choose multiple options; the result is a JSON array"`
}

// ExecuteAskUser handles the ask_user special-case tool call.
// Exported so the network package can use it as a callback to DispatchBuiltins
// without importing agent's internal types directly.
//
// Stability: runtime-integration export shared with the network subpackage;
// excluded from the v1.x compatibility promise (may change or move to internal).
var ExecuteAskUser = executeAskUser

// executeAskUser handles the ask_user special-case tool call.
// Shared by both LLMAgent and Network dispatch functions.
func executeAskUser(ctx context.Context, handler InputHandler, agentName string, tc core.ToolCall) (string, error) {
	var args askUserArgs
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		return "", err
	}

	resp, err := handler.RequestInput(ctx, InputRequest{
		Question:    args.Question,
		Options:     args.Options,
		MultiSelect: args.MultiSelect,
		Metadata: map[string]string{
			"agent":  agentName,
			"source": "llm",
		},
	})
	if err != nil {
		return "", err
	}

	if args.MultiSelect {
		// Return a JSON array so the LLM receives a structured multi-answer.
		b, err := json.Marshal(resp.Values)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return resp.Value, nil
}

// ExecuteAgent runs a and returns the result. When ch is non-nil, child events
// flow through the parent channel with envelope-event filtering via WithStream.
// Panic recovery is included on both paths. logger may be nil.
//
// Stability: runtime-integration export shared with the network subpackage;
// excluded from the v1.x compatibility promise (may change or move to internal).
func ExecuteAgent(ctx context.Context, a core.Agent, agentName string, task core.AgentTask, ch chan<- core.StreamEvent, logger *slog.Logger) (result core.AgentResult, err error) {
	if logger == nil {
		logger = nopLogger
	}
	if ch != nil {
		logger.Debug("executing subagent (streaming)", "agent", agentName)
		return forwardSubagentStream(ctx, a, agentName, task, ch, logger)
	}
	// Non-streaming path with panic recovery.
	logger.Debug("executing subagent", "agent", agentName)
	defer func() {
		if p := recover(); p != nil {
			logger.Error("subagent panic", "agent", agentName, "panic", fmt.Sprintf("%v", p))
			result = core.AgentResult{}
			err = fmt.Errorf("subagent %q panic: %v", agentName, p)
		}
	}()
	return a.Execute(ctx, task)
}

// forwardSubagentStream runs a subagent with streaming, forwarding events to the
// parent channel while filtering envelope events (EventRunStart, EventRunFinish,
// EventIterationStart, EventIterationFinish).
func forwardSubagentStream(
	ctx context.Context,
	a core.Agent,
	agentName string,
	task core.AgentTask,
	ch chan<- core.StreamEvent,
	logger *slog.Logger,
) (AgentResult, error) {
	subCh := make(chan core.StreamEvent, 64)
	done := make(chan struct{})
	safeCloseSubCh := onceClose(subCh)

	go func() {
		defer close(done)
		for ev := range subCh {
			if ev.Type == core.EventRunStart || ev.Type == core.EventRunFinish ||
				ev.Type == core.EventIterationStart || ev.Type == core.EventIterationFinish {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				// Why: drain inline rather than spawning a second goroutine.
				// The old startDrainTimeout left an orphan 60s goroutine
				// per cancelled subagent stream with no shutdown path; this
				// keeps the single forwarder goroutine and lets it exit
				// cleanly when subCh closes (or the safety timeout fires).
				drainSubCh(subCh, safeCloseSubCh, logger, agentName)
				return
			}
		}
	}()

	var result AgentResult
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				safeCloseSubCh()
				result = AgentResult{}
				err = fmt.Errorf("subagent %q panic: %v", agentName, p)
			}
		}()
		result, err = a.Execute(ctx, task, core.WithStream(subCh))
	}()

	<-done
	return result, err
}

// drainSubCh consumes remaining events from subCh until it closes or the
// 60-second safety timeout fires. Called inline by the forwarder goroutine
// after ctx cancellation so we don't leak a second goroutine per cancelled
// subagent stream.
func drainSubCh(subCh <-chan core.StreamEvent, safeClose func(), logger *slog.Logger, agentName string) {
	timeout := time.NewTimer(60 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case _, ok := <-subCh:
			if !ok {
				return
			}
		case <-timeout.C:
			logger.Warn("subagent stream drain timed out, closing subCh", "agent", agentName)
			safeClose()
			return
		}
	}
}
