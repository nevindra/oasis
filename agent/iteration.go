package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// loopState holds the mutable per-execution state shared across runIteration
// calls. Cumulative fields (messages, totalUsage, steps, accumulators) are
// mutated in place by each iteration; read-only setup fields (budgets,
// thresholds, safeCloseCh) are written once by runLoop before the loop starts.
type loopState struct {
	messages                   []ChatMessage
	messageRuneCount           int
	totalUsage                 Usage
	steps                      []StepTrace
	lastAgentOutput            string
	lastThinking               string
	accumulatedAttachments     []Attachment
	accumulatedAttachmentBytes int64
	attachByteBudget           int64
	hasAgentTools              bool
	compressThreshold          int
	safeCloseCh                func()
}

// iterationOutcome signals whether to continue to the next iteration or
// terminate the loop with the iterationResult's final AgentResult.
type iterationOutcome int

const (
	iterContinue iterationOutcome = iota
	iterDone
)

// iterationResult is returned by runIteration. When outcome == iterDone,
// runLoop returns (final, err) without further work.
type iterationResult struct {
	outcome iterationOutcome
	final   AgentResult
	err     error
}

// runIteration executes a single iteration of the tool-calling loop:
// PreProcessor hook → LLM call (one of four modes) → PostProcessor hook →
// tool dispatch → result handling → optional compression. Mutates state's
// cumulative fields in place. Returns iterContinue to advance the loop or
// iterDone to terminate it with the embedded AgentResult/error.
func runIteration(ctx context.Context, cfg LoopConfig, task AgentTask, ch chan<- StreamEvent, state *loopState, i int) iterationResult {
	cfg.logger.Debug("loop iteration started", "agent", cfg.name, "iteration", i,
		"tools", len(cfg.tools), "messages", len(state.messages), "runes", state.messageRuneCount)

	// Iteration tracing span.
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

	req := ChatRequest{Messages: state.messages, ResponseSchema: cfg.responseSchema, GenerationParams: cfg.generationParams}

	// PreProcessor hook.
	if err := cfg.processors.RunPreLLM(iterCtx, &req); err != nil {
		cfg.logger.Error("pre-processor failed", "agent", cfg.name, "iteration", i, "error", err)
		endIter()
		state.safeCloseCh()
		if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
			return iterationResult{outcome: iterDone, final: AgentResult{Usage: state.totalUsage, Steps: state.steps}, err: s}
		}
		res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
		return iterationResult{outcome: iterDone, final: res, err: retErr}
	}

	var resp ChatResponse
	var err error
	streamedThisIter := false // true when ChatStream already emitted text deltas

	req.Tools = cfg.tools
	llmStart := time.Now()
	if len(cfg.tools) > 0 && ch != nil && !state.hasAgentTools {
		// Streaming with tools (single agent only): intermediate channel so the
		// provider's defer-close doesn't shut down the main stream. Networks
		// use Chat() to preserve router text-delta deduplication.
		cfg.logger.Debug("calling LLM (streaming, with tools)", "agent", cfg.name, "iteration", i, "tool_count", len(cfg.tools))
		iterCh, wait := newStreamForwarder(ctx, ch, defaultIterChBufSize)
		resp, err = cfg.provider.ChatStream(iterCtx, req, iterCh)
		wait()
		streamedThisIter = true
	} else if len(cfg.tools) > 0 {
		cfg.logger.Debug("calling LLM (with tools)", "agent", cfg.name, "iteration", i, "tool_count", len(cfg.tools))
		resp, err = cfg.provider.Chat(iterCtx, req)
	} else if ch != nil {
		// No tools, streaming — terminal path (single-shot stream then return).
		cfg.logger.Debug("calling LLM (streaming, no tools)", "agent", cfg.name, "iteration", i)
		iterCh, wait := newStreamForwarder(ctx, ch, defaultIterChBufSize)
		resp, err = cfg.provider.ChatStream(iterCtx, req, iterCh)
		wait()
		if err != nil {
			endIter()
			state.safeCloseCh()
			return iterationResult{outcome: iterDone, final: AgentResult{Usage: state.totalUsage, Steps: state.steps}, err: err}
		}
		state.totalUsage.InputTokens += resp.Usage.InputTokens
		state.totalUsage.OutputTokens += resp.Usage.OutputTokens

		// PostProcessor hook (response already streamed, but processors still
		// run for side effects like logging and validation).
		if err := cfg.processors.RunPostLLM(iterCtx, &resp); err != nil {
			endIter()
			state.safeCloseCh()
			if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
				return iterationResult{outcome: iterDone, final: AgentResult{Usage: state.totalUsage, Steps: state.steps}, err: s}
			}
			res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
			return iterationResult{outcome: iterDone, final: res, err: retErr}
		}

		endIter()
		state.safeCloseCh()
		cfg.mem.PersistMessages(iterCtx, cfg.name, task, task.Input, resp.Content, state.steps)
		return iterationResult{
			outcome: iterDone,
			final: AgentResult{
				Output:      resp.Content,
				Thinking:    resp.Thinking,
				Attachments: mergeAttachments(state.accumulatedAttachments, resp.Attachments),
				Usage:       state.totalUsage,
				Steps:       state.steps,
			},
		}
	} else {
		cfg.logger.Debug("calling LLM (no tools)", "agent", cfg.name, "iteration", i)
		resp, err = cfg.provider.Chat(iterCtx, req)
	}

	if err != nil {
		cfg.logger.Error("LLM call failed", "agent", cfg.name, "iteration", i, "error", err, "duration", time.Since(llmStart))
		endIter()
		state.safeCloseCh()
		return iterationResult{outcome: iterDone, final: AgentResult{Usage: state.totalUsage, Steps: state.steps}, err: err}
	}
	cfg.logger.Debug("LLM call completed", "agent", cfg.name, "iteration", i,
		"duration", time.Since(llmStart),
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
		"tool_calls", len(resp.ToolCalls))
	state.totalUsage.InputTokens += resp.Usage.InputTokens
	state.totalUsage.OutputTokens += resp.Usage.OutputTokens

	// PostProcessor hook.
	if err := cfg.processors.RunPostLLM(iterCtx, &resp); err != nil {
		endIter()
		state.safeCloseCh()
		if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
			return iterationResult{outcome: iterDone, final: AgentResult{Usage: state.totalUsage, Steps: state.steps}, err: s}
		}
		res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
		return iterationResult{outcome: iterDone, final: res, err: retErr}
	}

	// Capture and emit thinking content from this LLM call.
	if resp.Thinking != "" {
		state.lastThinking = resp.Thinking
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
			content = state.lastAgentOutput
		}
		if ch != nil && !streamedThisIter {
			select {
			case ch <- StreamEvent{Type: EventTextDelta, Content: content}:
			case <-ctx.Done():
			}
		}
		state.safeCloseCh()
		endIter()
		cfg.mem.PersistMessages(iterCtx, cfg.name, task, task.Input, content, state.steps)
		return iterationResult{
			outcome: iterDone,
			final: AgentResult{
				Output:      content,
				Thinking:    state.lastThinking,
				Attachments: mergeAttachments(state.accumulatedAttachments, resp.Attachments),
				Usage:       state.totalUsage,
				Steps:       state.steps,
			},
		}
	}

	if iterSpan != nil {
		iterSpan.SetAttr(IntAttr("tool_count", len(resp.ToolCalls)))
	}

	// Append assistant message with tool calls.
	state.messages = append(state.messages, ChatMessage{
		Role:      "assistant",
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	})
	state.messageRuneCount += utf8.RuneCountInString(resp.Content)

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

	// Process results sequentially (PostToolProcessor + message assembly + trace).
	for j, tc := range resp.ToolCalls {
		state.totalUsage.InputTokens += results[j].usage.InputTokens
		state.totalUsage.OutputTokens += results[j].usage.OutputTokens

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
		state.steps = append(state.steps, trace)

		// Accumulate attachments from sub-agent results, capped by count and byte budget.
		for _, a := range results[j].attachments {
			aSize := int64(len(a.Data))
			if len(state.accumulatedAttachments) >= maxAccumulatedAttachments ||
				state.accumulatedAttachmentBytes+aSize > state.attachByteBudget {
				break
			}
			state.accumulatedAttachments = append(state.accumulatedAttachments, a)
			state.accumulatedAttachmentBytes += aSize
		}

		result := ToolResult{Content: results[j].content}
		if err := cfg.processors.RunPostTool(iterCtx, tc, &result); err != nil {
			endIter()
			state.safeCloseCh()
			if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
				return iterationResult{outcome: iterDone, final: AgentResult{Usage: state.totalUsage, Steps: state.steps}, err: s}
			}
			res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
			return iterationResult{outcome: iterDone, final: res, err: retErr}
		}
		// Truncate large tool results before appending to message history.
		// Stream events and step traces retain full content (transient).
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
		state.messages = append(state.messages, ToolResultMessage(tc.ID, msgContent))
		state.messageRuneCount += utf8.RuneCountInString(msgContent)

		// Track the last sub-agent output for fallback.
		if strings.HasPrefix(tc.Name, "agent_") {
			state.lastAgentOutput = result.Content
		}
	}
	// Compress context if over budget (within the iteration span so compression
	// traces are children of the iteration that triggered them).
	if state.compressThreshold > 0 && state.messageRuneCount > state.compressThreshold {
		cfg.logger.Info("context compression triggered", "agent", cfg.name, "iteration", i, "runes", state.messageRuneCount, "threshold", state.compressThreshold)
		state.messages, state.messageRuneCount = compressMessages(iterCtx, cfg, task, state.messages, 2, state.messageRuneCount)
	}
	endIter()
	return iterationResult{outcome: iterContinue}
}
