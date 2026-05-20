package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
)

// LoopConfig holds everything the shared runLoop needs to run.
type LoopConfig struct {
	name           string           // for logging (e.g. "agent:foo", "network:bar")
	provider       Provider
	tools          []ToolDefinition // pre-built tool defs (including ask_user if applicable)
	processors     *ProcessorChain
	maxIter        int
	mem            *memory.AgentMemory
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
	suspendMu           *sync.Mutex     // guards check-then-add on suspendCount/suspendBytes
	maxSuspendSnapshots int
	maxSuspendBytes     int64
	compressModel       ModelFunc
	compressThreshold   int // 0 = default (200K runes), negative = disabled
	compressor          Compactor         // nil = NewInlineCompactor(provider) on first call
	generationParams    *GenerationParams // nil = use provider defaults
	maxParallelDispatch int               // 0 → uses package-level default
	maxToolResultLen    int               // 0 → uses package-level default
	maxPlanSteps        int               // 0 → uses package-level default
	toolResultStore     core.ToolResultStore // nil = legacy truncation marker
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

// maxParallelDispatch caps the number of concurrent tool call goroutines
// to avoid overwhelming external services with unbounded parallelism.
const maxParallelDispatch = 10

// runLoop is the shared tool-calling loop used by both LLMAgent and Network.
// When ch is nil, it operates in blocking mode (Execute). When ch is non-nil,
// it emits StreamEvent values and closes ch when done (ExecuteStream).
func runLoop(ctx context.Context, cfg LoopConfig, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	if cfg.logger == nil {
		cfg.logger = nopLogger
	}
	var totalUsage Usage
	var steps []StepTrace

	// safeCloseCh closes the streaming channel exactly once. All exit paths
	// use this instead of raw close(ch), preventing double-close panics if
	// a provider's ChatStream also closes the channel internally.
	safeCloseCh := func() {}
	if ch != nil {
		safeCloseCh = onceClose(ch)
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
		messages = cfg.mem.BuildMessages(ctx, cfg.name, cfg.systemPrompt, task)
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
		messageRuneCount += utf8.RuneCountInString(m.Content)
	}
	// Per-turn LLM compression is disabled by default. Consumers that want
	// it must explicitly set WithCompressThreshold(n) with n > 0. The loop
	// gating below uses `compressThreshold > 0`, so zero/negative values
	// both mean "disabled". Per-thread compaction (see Compactor /
	// WithCompaction) is now the preferred strategy for long chat threads.
	compressThreshold := cfg.compressThreshold

	// Detect whether the tool set includes agent_* delegation tools (Network).
	// Networks suppress router text-deltas when a sub-agent streams, so they
	// must use non-streaming Chat() for tool-loop iterations to preserve that
	// deduplication. Single agents stream tool-loop iterations for real-time UX.
	hasAgentTools := false
	for _, t := range cfg.tools {
		if strings.HasPrefix(t.Name, "agent_") || t.Name == "spawn_agent" {
			hasAgentTools = true
			break
		}
	}

	var lastAgentOutput string
	var lastThinking string
	var accumulatedAttachments []Attachment
	var accumulatedAttachmentBytes int64

	for i := 0; i < cfg.maxIter; i++ {
		cfg.logger.Debug("loop iteration started", "agent", cfg.name, "iteration", i, "tools", len(cfg.tools), "messages", len(messages), "runes", messageRuneCount)

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
			cfg.logger.Error("pre-processor failed", "agent", cfg.name, "iteration", i, "error", err)
			endIter()
			safeCloseCh()
			if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
				return AgentResult{Usage: totalUsage, Steps: steps}, s
			}
			return handleProcessorErrorWithSteps(err, totalUsage, steps)
		}

		var resp ChatResponse
		var err error
		streamedThisIter := false // true when ChatStream already emitted text deltas

		req.Tools = cfg.tools
		llmStart := time.Now()
		if len(cfg.tools) > 0 && ch != nil && !hasAgentTools {
			// Streaming with tools (single agent only): use an intermediate
			// channel so the provider's defer-close doesn't shut down the
			// main stream. Networks use Chat() to preserve router text-delta
			// deduplication with sub-agent streaming.
			cfg.logger.Debug("calling LLM (streaming, with tools)", "agent", cfg.name, "iteration", i, "tool_count", len(cfg.tools))
			iterCh := make(chan StreamEvent, 64)
			var fwdWg sync.WaitGroup
			fwdWg.Add(1)
			go func() {
				defer fwdWg.Done()
				for ev := range iterCh {
					select {
					case ch <- ev:
					case <-ctx.Done():
						for range iterCh {
						}
						return
					}
				}
			}()
			resp, err = cfg.provider.ChatStream(iterCtx, req, iterCh)
			fwdWg.Wait()
			streamedThisIter = true
		} else if len(cfg.tools) > 0 {
			cfg.logger.Debug("calling LLM (with tools)", "agent", cfg.name, "iteration", i, "tool_count", len(cfg.tools))
			resp, err = cfg.provider.Chat(iterCtx, req)
		} else if ch != nil {
			// No tools, streaming — use an intermediate channel so the
			// provider's defer-close doesn't touch ch directly. safeCloseCh
			// remains the sole closer of ch, preventing double-close panics.
			cfg.logger.Debug("calling LLM (streaming, no tools)", "agent", cfg.name, "iteration", i)
			iterCh := make(chan StreamEvent, 64)
			var fwdWg sync.WaitGroup
			fwdWg.Add(1)
			go func() {
				defer fwdWg.Done()
				for ev := range iterCh {
					select {
					case ch <- ev:
					case <-ctx.Done():
						for range iterCh {
						}
						return
					}
				}
			}()
			resp, err = cfg.provider.ChatStream(iterCtx, req, iterCh)
			fwdWg.Wait()
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
			cfg.mem.PersistMessages(iterCtx, cfg.name, task, task.Input, resp.Content, steps)
			return AgentResult{Output: resp.Content, Thinking: resp.Thinking, Attachments: mergeAttachments(accumulatedAttachments, resp.Attachments), Usage: totalUsage, Steps: steps}, nil
		} else {
			cfg.logger.Debug("calling LLM (no tools)", "agent", cfg.name, "iteration", i)
			resp, err = cfg.provider.Chat(iterCtx, req)
		}

		if err != nil {
			cfg.logger.Error("LLM call failed", "agent", cfg.name, "iteration", i, "error", err, "duration", time.Since(llmStart))
			endIter()
			safeCloseCh()
			return AgentResult{Usage: totalUsage, Steps: steps}, err
		}
		cfg.logger.Debug("LLM call completed", "agent", cfg.name, "iteration", i,
			"duration", time.Since(llmStart),
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"tool_calls", len(resp.ToolCalls))
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
			cfg.logger.Debug("final response (no tool calls)", "agent", cfg.name, "iteration", i)
			content := resp.Content
			if content == "" {
				content = lastAgentOutput
			}
			if ch != nil && !streamedThisIter {
				select {
				case ch <- StreamEvent{Type: EventTextDelta, Content: content}:
				case <-ctx.Done():
				}
			}
			safeCloseCh()
			endIter()
			cfg.mem.PersistMessages(iterCtx, cfg.name, task, task.Input, content, steps)
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
		messageRuneCount += utf8.RuneCountInString(resp.Content)

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
				select {
				case ch <- StreamEvent{
					Type:    EventRoutingDecision,
					Name:    cfg.name,
					Content: buildRoutingSummary(agents, directTools),
				}:
				case <-ctx.Done():
				}
			}
		}

		// Execute tool calls in parallel.
		toolNames := make([]string, len(resp.ToolCalls))
		for ti, tc := range resp.ToolCalls {
			toolNames[ti] = tc.Name
		}
		cfg.logger.Info("dispatching tool calls", "agent", cfg.name, "iteration", i, "tools", toolNames)
		dispatchStart := time.Now()
		results := dispatchParallel(iterCtx, resp.ToolCalls, cfg.dispatch, cfg.maxParallelDispatch)
		cfg.logger.Debug("tool dispatch completed", "agent", cfg.name, "iteration", i, "duration", time.Since(dispatchStart))

		// Process results sequentially (PostToolProcessor + message assembly + trace collection).
		for j, tc := range resp.ToolCalls {
			totalUsage.InputTokens += results[j].usage.InputTokens
			totalUsage.OutputTokens += results[j].usage.OutputTokens

			if results[j].isError {
				cfg.logger.Warn("tool call returned error", "agent", cfg.name, "tool", tc.Name, "error", results[j].content, "duration", results[j].duration)
			} else {
				cfg.logger.Debug("tool call result", "agent", cfg.name, "tool", tc.Name, "duration", results[j].duration, "result_len", len(results[j].content))
			}

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
			maxLen := cfg.maxToolResultLen
			if maxLen == 0 {
				maxLen = maxToolResultMessageLen
			}
			if utf8.RuneCountInString(msgContent) > maxLen {
				inline := TruncateStr(msgContent, maxLen)
				total := utf8.RuneCountInString(msgContent)
				if cfg.toolResultStore != nil {
					id, putErr := cfg.toolResultStore.Put(iterCtx, msgContent)
					if putErr == nil {
						msgContent = inline + fmt.Sprintf(
							"\n\n[truncated at %d runes of %d total. Use read_full_result(id=%q, offset=%d, length=50000) for more]",
							maxLen, total, id, maxLen)
					} else {
						cfg.logger.Warn("tool result store put failed, falling back to legacy marker",
							"agent", cfg.name, "error", putErr)
						msgContent = inline + "\n\n[output truncated — original was longer]"
					}
				} else {
					msgContent = inline + "\n\n[output truncated — original was longer]"
				}
			}
			messages = append(messages, ToolResultMessage(tc.ID, msgContent))
			messageRuneCount += utf8.RuneCountInString(msgContent)

			// Track the last sub-agent output for fallback.
			if strings.HasPrefix(tc.Name, "agent_") {
				lastAgentOutput = result.Content
			}
		}
		// Compress context if over budget (within the iteration span so
		// compression traces are children of the iteration that triggered them).
		if compressThreshold > 0 && messageRuneCount > compressThreshold {
			cfg.logger.Info("context compression triggered", "agent", cfg.name, "iteration", i, "runes", messageRuneCount, "threshold", compressThreshold)
			messages, messageRuneCount = compressMessages(iterCtx, cfg, task, messages, 2, messageRuneCount)
		}
		endIter()
	}

	// Max iterations — force synthesis.
	// Surface the max-iter hit so UIs can show the forced-synthesis cost.
	if ch != nil {
		payload, _ := json.Marshal(map[string]int{
			"iter":     cfg.maxIter,
			"max_iter": cfg.maxIter,
		})
		select {
		case ch <- StreamEvent{
			Type:    EventMaxIterReached,
			Name:    cfg.name,
			Content: string(payload),
		}:
		case <-ctx.Done():
			safeCloseCh()
			return AgentResult{Usage: totalUsage}, ctx.Err()
		}
	}
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
		// Use an intermediate channel so the provider's defer-close doesn't
		// touch ch directly. safeCloseCh remains the sole closer of ch.
		synthCh := make(chan StreamEvent, 64)
		var fwdWg sync.WaitGroup
		fwdWg.Add(1)
		go func() {
			defer fwdWg.Done()
			for ev := range synthCh {
				select {
				case ch <- ev:
				case <-ctx.Done():
					for range synthCh {
					}
					return
				}
			}
		}()
		resp, err = cfg.provider.ChatStream(synthCtx, synthReq, synthCh)
		fwdWg.Wait()
	} else {
		resp, err = cfg.provider.Chat(synthCtx, synthReq)
	}
	if err != nil {
		cfg.logger.Error("synthesis LLM call failed", "agent", cfg.name, "error", err)
		safeCloseCh()
		return AgentResult{Usage: totalUsage, Steps: steps}, err
	}
	cfg.logger.Info("synthesis completed", "agent", cfg.name,
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens)
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
	cfg.mem.PersistMessages(synthCtx, cfg.name, task, task.Input, resp.Content, steps)
	return AgentResult{Output: resp.Content, Thinking: lastThinking, Attachments: mergeAttachments(accumulatedAttachments, resp.Attachments), Usage: totalUsage, Steps: steps}, nil
}


