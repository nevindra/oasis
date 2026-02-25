package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// --- shared execution loop ---

// DispatchResult holds the result of a single tool or agent dispatch.
type DispatchResult struct {
	Content     string
	Usage       Usage
	Attachments []Attachment
	// IsError signals that Content represents an error message rather than
	// a successful tool result. This enables structural error detection
	// without relying on string-prefix heuristics.
	IsError bool
}

// DispatchFunc executes a single tool call and returns the result.
// LLMAgent provides one that calls ToolRegistry.Execute + ask_user.
// Network provides one that also routes to subagents via the agent_* prefix.
type DispatchFunc func(ctx context.Context, tc ToolCall) DispatchResult

// toolExecFunc executes a tool by name. Abstracts ToolRegistry.Execute so
// dispatch functions work without an intermediate registry allocation.
type toolExecFunc = func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)

// toolExecStreamFunc executes a tool with streaming progress support.
// Abstracts ToolRegistry.ExecuteStream.
type toolExecStreamFunc = func(ctx context.Context, name string, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error)

// dispatchBuiltins handles the built-in special-case tools (ask_user, execute_plan,
// execute_code). Returns (result, true) if the call was handled, or (zero, false)
// if the caller should proceed with its own routing (agent delegation, direct tools).
func dispatchBuiltins(ctx context.Context, tc ToolCall, dispatch DispatchFunc, ih InputHandler, agentName string, planExec bool, codeRunner CodeRunner) (DispatchResult, bool) {
	if tc.Name == "ask_user" && ih != nil {
		content, err := executeAskUser(ctx, ih, agentName, tc)
		if err != nil {
			return DispatchResult{Content: "error: " + err.Error(), IsError: true}, true
		}
		return DispatchResult{Content: content}, true
	}
	if tc.Name == "execute_plan" && planExec {
		return executePlan(ctx, tc.Args, dispatch), true
	}
	if tc.Name == "execute_code" && codeRunner != nil {
		// Wrap dispatch to block execute_plan/execute_code calls from within code,
		// preventing unbounded recursion via execute_code → execute_plan → execute_code.
		safeDispatch := func(ctx context.Context, tc ToolCall) DispatchResult {
			if tc.Name == "execute_plan" || tc.Name == "execute_code" {
				return DispatchResult{Content: "error: " + tc.Name + " cannot be called from within execute_code", IsError: true}
			}
			return dispatch(ctx, tc)
		}
		return executeCode(ctx, tc.Args, codeRunner, safeDispatch), true
	}
	return DispatchResult{}, false
}

// dispatchTool executes a tool via the given executor and converts the result
// to a DispatchResult. When executeToolStream is non-nil and ch is non-nil,
// it uses the streaming executor instead.
// Shared by LLMAgent and Network for the common tool path.
func dispatchTool(ctx context.Context, executeTool toolExecFunc, executeToolStream toolExecStreamFunc, name string, args json.RawMessage, ch chan<- StreamEvent) DispatchResult {
	if ch != nil && executeToolStream != nil {
		result, err := executeToolStream(ctx, name, args, ch)
		if err != nil {
			return DispatchResult{Content: "error: " + err.Error(), IsError: true}
		}
		if result.Error != "" {
			return DispatchResult{Content: "error: " + result.Error, IsError: true}
		}
		return DispatchResult{Content: result.Content}
	}
	result, err := executeTool(ctx, name, args)
	if err != nil {
		return DispatchResult{Content: "error: " + err.Error(), IsError: true}
	}
	if result.Error != "" {
		return DispatchResult{Content: "error: " + result.Error, IsError: true}
	}
	return DispatchResult{Content: result.Content}
}

// loopConfig holds everything the shared runLoop needs to run.
type loopConfig struct {
	name           string           // for logging (e.g. "agent:foo", "network:bar")
	provider       Provider
	tools          []ToolDefinition // pre-built tool defs (including ask_user if applicable)
	processors     *ProcessorChain
	maxIter        int
	mem            *agentMemory
	inputHandler   InputHandler
	dispatch       DispatchFunc
	systemPrompt   string
	resumeMessages []ChatMessage    // if set, replaces buildMessages (used by suspend/resume)
	responseSchema *ResponseSchema  // if set, attached to every ChatRequest
	tracer              Tracer           // nil = no tracing
	logger              *slog.Logger     // never nil (nopLogger fallback)
	maxAttachmentBytes  int64            // attachment size budget (0 = default 50MB)
	suspendCount        *atomic.Int64    // nil = no budget tracking
	suspendBytes        *atomic.Int64
	maxSuspendSnapshots int
	maxSuspendBytes     int64
	compressModel       ModelFunc
	compressThreshold   int // 0 = default (200K runes), negative = disabled
	generationParams    *GenerationParams // nil = use provider defaults
}

// maxToolResultMessageLen is the maximum rune length for a tool result stored
// in the conversation message history during the tool-calling loop. Results
// exceeding this limit are truncated with a marker so the LLM knows content
// was trimmed. This prevents unbounded memory growth from tools that return
// very large outputs (e.g. web scraping, file reads).
//
// Stream events and step traces retain the full content since they are
// transient and not accumulated across iterations.
const maxToolResultMessageLen = 100_000 // ~25K tokens

// maxAccumulatedAttachments caps the number of attachments collected from
// tool/agent results during the execution loop. Prevents unbounded memory
// growth when subagents produce large binary content (images, audio, etc.).
const maxAccumulatedAttachments = 50

// maxAccumulatedAttachmentBytes is the default size budget (bytes) for
// attachments collected from tool/agent results during the execution loop.
const maxAccumulatedAttachmentBytes int64 = 50 * 1024 * 1024 // 50 MB

// defaultCompressThreshold is the default rune count at which context
// compression triggers in the tool-calling loop. ~50K tokens.
const defaultCompressThreshold = 200_000

// maxParallelDispatch caps the number of concurrent tool call goroutines
// to avoid overwhelming external services with unbounded parallelism.
const maxParallelDispatch = 10

// runLoop is the shared tool-calling loop used by both LLMAgent and Network.
// When ch is nil, it operates in blocking mode (Execute). When ch is non-nil,
// it emits StreamEvent values and closes ch when done (ExecuteStream).
func runLoop(ctx context.Context, cfg loopConfig, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	var totalUsage Usage
	var steps []StepTrace

	// safeCloseCh closes the streaming channel exactly once. All exit paths
	// use this instead of raw close(ch), preventing double-close panics if
	// a provider's ChatStream also closes the channel internally.
	var closeOnce sync.Once
	safeCloseCh := func() {
		if ch != nil {
			closeOnce.Do(func() {
				defer func() { recover() }()
				close(ch)
			})
		}
	}

	// Inject InputHandler into context for processors.
	if cfg.inputHandler != nil {
		ctx = WithInputHandlerContext(ctx, cfg.inputHandler)
	}

	// Build initial messages (system prompt + user memory + history + user input).
	// If resumeMessages is set (suspend/resume), use those instead.
	var messages []ChatMessage
	if len(cfg.resumeMessages) > 0 {
		messages = cfg.resumeMessages
	} else {
		messages = cfg.mem.buildMessages(ctx, cfg.name, cfg.systemPrompt, task)
	}

	// Emit processing-start event after context is built, before the loop.
	if ch != nil {
		select {
		case ch <- StreamEvent{Type: EventProcessingStart, Name: cfg.name}:
		case <-ctx.Done():
			safeCloseCh()
			return AgentResult{Usage: totalUsage}, ctx.Err()
		}
	}

	// lastAgentOutput tracks the most recent sub-agent result so we can fall
	// back to it when the router produces an empty final response (common for
	// pure-routing LLMs that don't synthesize a reply after delegating).
	// For LLMAgent this is never set (no agent_* tools).
	attachByteBudget := cfg.maxAttachmentBytes
	if attachByteBudget <= 0 {
		attachByteBudget = maxAccumulatedAttachmentBytes
	}

	// Track message rune count for compression.
	var messageRuneCount int
	for _, m := range messages {
		messageRuneCount += len([]rune(m.Content))
	}
	compressThreshold := cfg.compressThreshold
	if compressThreshold == 0 {
		compressThreshold = defaultCompressThreshold
	}

	var lastAgentOutput string
	var lastThinking string
	var accumulatedAttachments []Attachment
	var accumulatedAttachmentBytes int64

	for i := 0; i < cfg.maxIter; i++ {
		// Start an iteration span if tracing is enabled.
		iterCtx := ctx
		var iterSpan Span
		if cfg.tracer != nil {
			iterCtx, iterSpan = cfg.tracer.Start(ctx, "agent.loop.iteration",
				IntAttr("iteration", i),
				BoolAttr("has_tools", len(cfg.tools) > 0))
		}
		endIter := func() {
			if iterSpan != nil {
				iterSpan.End()
			}
		}

		req := ChatRequest{Messages: messages, ResponseSchema: cfg.responseSchema, GenerationParams: cfg.generationParams}

		// PreProcessor hook.
		if err := cfg.processors.RunPreLLM(iterCtx, &req); err != nil {
			endIter()
			safeCloseCh()
			if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
				return AgentResult{Usage: totalUsage, Steps: steps}, s
			}
			return handleProcessorErrorWithSteps(err, totalUsage, steps)
		}

		var resp ChatResponse
		var err error

		req.Tools = cfg.tools
		if len(cfg.tools) > 0 {
			resp, err = cfg.provider.Chat(iterCtx, req)
		} else if ch != nil {
			// No tools, streaming — stream the response directly.
			resp, err = cfg.provider.ChatStream(iterCtx, req, ch)
			if err != nil {
				endIter()
				safeCloseCh()
				return AgentResult{Usage: totalUsage, Steps: steps}, err
			}
			totalUsage.InputTokens += resp.Usage.InputTokens
			totalUsage.OutputTokens += resp.Usage.OutputTokens

			// PostProcessor hook (response already streamed, but processors
			// still run for side effects like logging and validation).
			if err := cfg.processors.RunPostLLM(iterCtx, &resp); err != nil {
				endIter()
				safeCloseCh()
				if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
					return AgentResult{Usage: totalUsage, Steps: steps}, s
				}
				return handleProcessorErrorWithSteps(err, totalUsage, steps)
			}

			endIter()
			safeCloseCh()
			cfg.mem.persistMessages(iterCtx, cfg.name, task, task.Input, resp.Content, steps)
			return AgentResult{Output: resp.Content, Thinking: resp.Thinking, Attachments: mergeAttachments(accumulatedAttachments, resp.Attachments), Usage: totalUsage, Steps: steps}, nil
		} else {
			resp, err = cfg.provider.Chat(iterCtx, req)
		}

		if err != nil {
			endIter()
			safeCloseCh()
			return AgentResult{Usage: totalUsage, Steps: steps}, err
		}
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		// PostProcessor hook.
		if err := cfg.processors.RunPostLLM(iterCtx, &resp); err != nil {
			endIter()
			safeCloseCh()
			if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
				return AgentResult{Usage: totalUsage, Steps: steps}, s
			}
			return handleProcessorErrorWithSteps(err, totalUsage, steps)
		}

		// Capture and emit thinking content from this LLM call.
		if resp.Thinking != "" {
			lastThinking = resp.Thinking
			if ch != nil {
				select {
				case ch <- StreamEvent{Type: EventThinking, Content: resp.Thinking}:
				case <-ctx.Done():
				}
			}
		}

		// No tool calls — final response.
		if len(resp.ToolCalls) == 0 {
			content := resp.Content
			if content == "" {
				content = lastAgentOutput
			}
			if ch != nil {
				// Only emit text-delta if no sub-agent already streamed.
				// When a Network delegates to a streaming sub-agent, its
				// text-delta events are forwarded to the parent channel in
				// real time. The router's final response (echo, paraphrase,
				// or empty) would duplicate the content consumers already
				// received. Skip the delta entirely; AgentResult.Output
				// still carries the correct final text for non-streaming use.
				if lastAgentOutput == "" {
					select {
					case ch <- StreamEvent{Type: EventTextDelta, Content: content}:
					case <-ctx.Done():
					}
				}
			}
			safeCloseCh()
			endIter()
			cfg.mem.persistMessages(iterCtx, cfg.name, task, task.Input, content, steps)
			return AgentResult{Output: content, Thinking: lastThinking, Attachments: mergeAttachments(accumulatedAttachments, resp.Attachments), Usage: totalUsage, Steps: steps}, nil
		}

		if iterSpan != nil {
			iterSpan.SetAttr(IntAttr("tool_count", len(resp.ToolCalls)))
		}

		// Append assistant message with tool calls.
		messages = append(messages, ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		messageRuneCount += len([]rune(resp.Content))

		// Emit tool-call-start events before dispatch.
		if ch != nil {
			for _, tc := range resp.ToolCalls {
				select {
				case ch <- StreamEvent{Type: EventToolCallStart, ID: tc.ID, Name: tc.Name, Args: tc.Args}:
				case <-ctx.Done():
				}
			}

			// Emit routing-decision for Networks (when agent_* tool calls are present).
			var agents, directTools []string
			for _, tc := range resp.ToolCalls {
				if after, ok := strings.CutPrefix(tc.Name, "agent_"); ok {
					agents = append(agents, after)
				} else {
					directTools = append(directTools, tc.Name)
				}
			}
			if len(agents) > 0 {
				summary, _ := json.Marshal(map[string][]string{
					"agents": agents,
					"tools":  directTools,
				})
				select {
				case ch <- StreamEvent{
					Type:    EventRoutingDecision,
					Name:    cfg.name,
					Content: string(summary),
				}:
				case <-ctx.Done():
				}
			}
		}

		// Execute tool calls in parallel.
		results := dispatchParallel(iterCtx, resp.ToolCalls, cfg.dispatch)

		// Process results sequentially (PostToolProcessor + message assembly + trace collection).
		for j, tc := range resp.ToolCalls {
			totalUsage.InputTokens += results[j].usage.InputTokens
			totalUsage.OutputTokens += results[j].usage.OutputTokens

			// Emit tool-call-result event.
			if ch != nil {
				select {
				case ch <- StreamEvent{
					Type:     EventToolCallResult,
					ID:       tc.ID,
					Name:     tc.Name,
					Content:  results[j].content,
					Usage:    results[j].usage,
					Duration: results[j].duration,
				}:
				case <-ctx.Done():
				}
			}

			// Build step trace.
			trace := buildStepTrace(tc, results[j])
			steps = append(steps, trace)

			// Accumulate attachments from sub-agent results (e.g. image generation).
			// Capped by both count and total byte size to prevent unbounded memory growth.
			for _, a := range results[j].attachments {
				aSize := int64(len(a.Data))
				if len(accumulatedAttachments) >= maxAccumulatedAttachments ||
					accumulatedAttachmentBytes+aSize > attachByteBudget {
					break
				}
				accumulatedAttachments = append(accumulatedAttachments, a)
				accumulatedAttachmentBytes += aSize
			}

			result := ToolResult{Content: results[j].content}
			if err := cfg.processors.RunPostTool(iterCtx, tc, &result); err != nil {
				endIter()
				safeCloseCh()
				if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
					return AgentResult{Usage: totalUsage, Steps: steps}, s
				}
				return handleProcessorErrorWithSteps(err, totalUsage, steps)
			}
			// Truncate large tool results before appending to message history
			// to prevent unbounded memory growth across iterations. Stream
			// events and step traces retain the full content (transient).
			msgContent := result.Content
			if len([]rune(msgContent)) > maxToolResultMessageLen {
				msgContent = truncateStr(msgContent, maxToolResultMessageLen) + "\n\n[output truncated — original was longer]"
			}
			messages = append(messages, ToolResultMessage(tc.ID, msgContent))
			messageRuneCount += len([]rune(msgContent))

			// Track the last sub-agent output for fallback.
			if strings.HasPrefix(tc.Name, "agent_") {
				lastAgentOutput = result.Content
			}
		}
		endIter()

		// Compress context if over budget.
		if compressThreshold > 0 && messageRuneCount > compressThreshold {
			messages, messageRuneCount = compressMessages(ctx, cfg, task, messages, 2)
		}
	}

	// Max iterations — force synthesis.
	cfg.logger.Warn("max iterations reached, forcing synthesis", "agent", cfg.name, "iteration", cfg.maxIter)
	messages = append(messages, UserMessage(
		"You have used all available tool calls. Summarize what you found and respond to the user."))

	// Start a synthesis span so the forced-response LLM call is visible in traces.
	synthCtx := ctx
	if cfg.tracer != nil {
		var synthSpan Span
		synthCtx, synthSpan = cfg.tracer.Start(ctx, "agent.loop.synthesis",
			IntAttr("iteration", cfg.maxIter),
			BoolAttr("forced", true))
		defer synthSpan.End()
	}

	var resp ChatResponse
	var err error
	synthReq := ChatRequest{Messages: messages, GenerationParams: cfg.generationParams}
	if ch != nil {
		resp, err = cfg.provider.ChatStream(synthCtx, synthReq, ch)
	} else {
		resp, err = cfg.provider.Chat(synthCtx, synthReq)
	}
	if err != nil {
		safeCloseCh()
		return AgentResult{Usage: totalUsage, Steps: steps}, err
	}
	totalUsage.InputTokens += resp.Usage.InputTokens
	totalUsage.OutputTokens += resp.Usage.OutputTokens

	// PostProcessor hook.
	if err := cfg.processors.RunPostLLM(synthCtx, &resp); err != nil {
		safeCloseCh()
		if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
			return AgentResult{Usage: totalUsage, Steps: steps}, s
		}
		return handleProcessorErrorWithSteps(err, totalUsage, steps)
	}

	// Capture thinking from the synthesis response.
	if resp.Thinking != "" {
		lastThinking = resp.Thinking
	}

	safeCloseCh()
	cfg.mem.persistMessages(synthCtx, cfg.name, task, task.Input, resp.Content, steps)
	return AgentResult{Output: resp.Content, Thinking: lastThinking, Attachments: mergeAttachments(accumulatedAttachments, resp.Attachments), Usage: totalUsage, Steps: steps}, nil
}

// mergeAttachments combines accumulated sub-agent attachments with the final
// response attachments. Accumulated attachments come first (from tool calls
// during the loop), followed by any attachments from the final LLM response.
func mergeAttachments(accumulated, resp []Attachment) []Attachment {
	if len(accumulated) == 0 {
		return resp
	}
	if len(resp) == 0 {
		return accumulated
	}
	merged := make([]Attachment, 0, len(accumulated)+len(resp))
	merged = append(merged, accumulated...)
	merged = append(merged, resp...)
	return merged
}

// runeCount returns the total rune count of all message content.
func runeCount(messages []ChatMessage) int {
	var n int
	for _, m := range messages {
		n += len([]rune(m.Content))
	}
	return n
}

// compressMessages summarizes old tool-result messages via an LLM call.
// Keeps the last preserveIters iterations of tool results intact.
// Returns the compressed message slice and new rune count, or the
// original slice on error (degrade, don't die).
func compressMessages(ctx context.Context, cfg loopConfig, task AgentTask, messages []ChatMessage, preserveIters int) ([]ChatMessage, int) {
	// Pick compression provider.
	provider := cfg.provider
	if cfg.compressModel != nil {
		if p := cfg.compressModel(ctx, task); p != nil {
			provider = p
		}
	}

	// Identify tool-result messages to compress.
	// Walk backwards to find the boundary of the last preserveIters iterations.
	// An "iteration" is one assistant message (with tool calls) followed by
	// its tool-result messages.
	iterCount := 0
	preserveFrom := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			iterCount++
			if iterCount >= preserveIters {
				preserveFrom = i
				break
			}
		}
	}

	// Collect old tool-result content and prior summaries (before preserveFrom).
	// Prior summaries are re-compressed so successive passes fold together.
	const summaryPrefix = "[Summary of earlier tool results]\n"
	var oldContent strings.Builder
	var toRemove []int
	for i := 0; i < preserveFrom; i++ {
		m := messages[i]
		switch {
		case m.ToolCallID != "" && m.Content != "":
			// Tool result message.
			oldContent.WriteString(m.Content)
			oldContent.WriteString("\n---\n")
			toRemove = append(toRemove, i)
		case m.Role == "user" && strings.HasPrefix(m.Content, summaryPrefix) && i > 0:
			// Prior summary from an earlier compression pass (skip the initial user message at i=0).
			oldContent.WriteString(m.Content)
			oldContent.WriteString("\n---\n")
			toRemove = append(toRemove, i)
		}
	}
	if len(toRemove) == 0 {
		return messages, runeCount(messages)
	}

	// Start compression span if tracing.
	compressCtx := ctx
	if cfg.tracer != nil {
		var span Span
		compressCtx, span = cfg.tracer.Start(ctx, "agent.loop.compress",
			IntAttr("original_runes", runeCount(messages)),
			IntAttr("messages_compressed", len(toRemove)))
		defer span.End()
	}

	// Call compression provider.
	summaryResp, err := provider.Chat(compressCtx, ChatRequest{
		Messages: []ChatMessage{
			SystemMessage("Summarize the following tool execution results concisely. Preserve key facts, data values, decisions, and errors. Omit redundant details."),
			UserMessage(oldContent.String()),
		},
	})
	if err != nil {
		cfg.logger.Warn("context compression failed, continuing uncompressed", "error", err)
		return messages, runeCount(messages)
	}

	// Build new message slice: keep non-removed messages, insert summary.
	removeSet := make(map[int]bool, len(toRemove))
	for _, idx := range toRemove {
		removeSet[idx] = true
	}
	var compressed []ChatMessage
	summaryInserted := false
	for i, m := range messages {
		if removeSet[i] {
			if !summaryInserted {
				compressed = append(compressed, UserMessage("[Summary of earlier tool results]\n"+summaryResp.Content))
				summaryInserted = true
			}
			continue
		}
		compressed = append(compressed, m)
	}

	newRuneCount := runeCount(compressed)
	cfg.logger.Info("context compressed",
		"agent", cfg.name,
		"before_runes", runeCount(messages),
		"after_runes", newRuneCount,
		"messages_removed", len(toRemove))

	return compressed, newRuneCount
}

// handleProcessorErrorWithSteps converts a processor error into an AgentResult.
// ErrHalt produces a graceful result; other errors propagate as failures.
// Any step traces collected before the error are preserved in the result.
func handleProcessorErrorWithSteps(err error, usage Usage, steps []StepTrace) (AgentResult, error) {
	var halt *ErrHalt
	if errors.As(err, &halt) {
		return AgentResult{Output: halt.Response, Usage: usage, Steps: steps}, nil
	}
	return AgentResult{Usage: usage, Steps: steps}, err
}

// buildStepTrace creates a StepTrace from a tool call and its execution result.
// Agent delegations (tool calls prefixed with "agent_") get Type "agent" and
// the prefix stripped from Name. All other calls get Type "tool".
func buildStepTrace(tc ToolCall, res toolExecResult) StepTrace {
	name := tc.Name
	traceType := "tool"
	input := string(tc.Args)

	if after, ok := strings.CutPrefix(name, "agent_"); ok {
		name = after
		traceType = "agent"
		// Extract the task field from agent call args for a cleaner trace.
		var params struct {
			Task string `json:"task"`
		}
		if json.Unmarshal(tc.Args, &params) == nil && params.Task != "" {
			input = params.Task
		}
	}

	return StepTrace{
		Name:     name,
		Type:     traceType,
		Input:    truncateStr(input, 200),
		Output:   truncateStr(res.content, 500),
		Usage:    res.usage,
		Duration: res.duration,
	}
}

// --- parallel tool dispatch ---

// toolExecResult holds the result of a single parallel tool call.
type toolExecResult struct {
	content     string
	usage       Usage
	attachments []Attachment
	duration    time.Duration
	isError     bool
}

// indexedResult pairs a tool execution result with its position in the
// original call slice, allowing channel-based collection in order.
type indexedResult struct {
	idx    int
	result toolExecResult
}

// safeDispatch wraps a dispatch call with panic recovery. If the dispatched
// tool panics, the panic is caught and converted to an error result instead
// of crashing the process. Matches the recovery pattern used for subagent
// dispatch in Network.makeDispatch.
func safeDispatch(ctx context.Context, tc ToolCall, dispatch DispatchFunc) (dr DispatchResult) {
	defer func() {
		if p := recover(); p != nil {
			dr = DispatchResult{Content: fmt.Sprintf("error: tool %q panic: %v", tc.Name, p), IsError: true}
		}
	}()
	return dispatch(ctx, tc)
}

// dispatchParallel runs all tool calls concurrently via the dispatch function
// and returns results in the same order as the input calls.
// Single calls run inline (no goroutine). Multiple calls use a fixed worker
// pool of min(len(calls), maxParallelDispatch) goroutines pulling from a
// shared work channel, avoiding unbounded goroutine creation.
//
// The collection loop is context-aware: if ctx is cancelled while tool calls
// are still in-flight, the function returns immediately with context-error
// results for incomplete calls instead of blocking indefinitely.
func dispatchParallel(ctx context.Context, calls []ToolCall, dispatch DispatchFunc) []toolExecResult {
	// Fast path: single call, no goroutine needed.
	if len(calls) == 1 {
		start := time.Now()
		dr := safeDispatch(ctx, calls[0], dispatch)
		return []toolExecResult{{content: dr.Content, usage: dr.Usage, attachments: dr.Attachments, duration: time.Since(start), isError: dr.IsError}}
	}

	resultCh := make(chan indexedResult, len(calls))

	// Work channel: each item is an (index, ToolCall) pair for workers to consume.
	type workItem struct {
		idx int
		tc  ToolCall
	}
	workCh := make(chan workItem, len(calls))
	for i, tc := range calls {
		workCh <- workItem{idx: i, tc: tc}
	}
	close(workCh)

	// Spawn a fixed pool of workers — never more goroutines than needed.
	numWorkers := min(len(calls), maxParallelDispatch)
	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for range numWorkers {
		go func() {
			defer wg.Done()
			for w := range workCh {
				if ctx.Err() != nil {
					resultCh <- indexedResult{w.idx, toolExecResult{content: "error: " + ctx.Err().Error(), isError: true}}
					continue
				}
				start := time.Now()
				dr := safeDispatch(ctx, w.tc, dispatch)
				resultCh <- indexedResult{w.idx, toolExecResult{content: dr.Content, usage: dr.Usage, attachments: dr.Attachments, duration: time.Since(start), isError: dr.IsError}}
			}
		}()
	}

	// Close resultCh once all workers are done.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results, bailing out if ctx is cancelled while calls are in-flight.
	results := make([]toolExecResult, len(calls))
	seen := make([]bool, len(calls))
collect:
	for received := 0; received < len(calls); received++ {
		select {
		case r, ok := <-resultCh:
			if !ok {
				break collect
			}
			results[r.idx] = r.result
			seen[r.idx] = true
		case <-ctx.Done():
			errResult := toolExecResult{content: "error: " + ctx.Err().Error(), isError: true}
			for i := range results {
				if !seen[i] {
					results[i] = errResult
				}
			}
			return results
		}
	}
	// Fill any unseen results (e.g. channel closed early) with error markers.
	for i := range results {
		if !seen[i] {
			results[i] = toolExecResult{content: "error: result not received", isError: true}
		}
	}
	return results
}

// truncateStr truncates a string to n runes.
func truncateStr(s string, n int) string {
	// Fast path: byte length ≤ n guarantees rune count ≤ n,
	// avoiding the []rune allocation for short/ASCII strings.
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
