package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nevindra/oasis/core"
)

// loopState holds the mutable per-execution state shared across runIteration
// calls.
type loopState struct {
	messages                   []core.ChatMessage
	messageRuneCount           int
	totalUsage                 core.Usage
	steps                      []core.StepTrace
	lastAgentOutput            string
	lastThinking               string
	accumulatedAttachments     []core.Attachment
	accumulatedAttachmentBytes int64
	attachByteBudget           int64
	hasAgentTools              bool
	compressThreshold          int
	safeCloseCh                func()

	lastWarnings     []string
	lastProviderMeta json.RawMessage

	files []core.Attachment

	iterations []core.IterationTrace

	sources []core.Source
}

// patchTerminal applies state-derived fields (usage, steps, warnings,
// provider meta, files, iterations, sources) and the finish reason onto r.
// Why: four terminal-result builders shared this boilerplate; centralizing it
// means a new state-derived field automatically applies to all exit paths.
func (s *loopState) patchTerminal(r *AgentResult, reason core.FinishReason) {
	r.Usage = s.totalUsage
	r.Steps = s.steps
	r.FinishReason = reason
	r.Warnings = s.lastWarnings
	r.ProviderMeta = s.lastProviderMeta
	r.Files = s.files
	r.Iterations = s.iterations
	r.Sources = s.sources
}

// applyPromptCacheMarkers stamps cache-breakpoint flags on the message slice
// for providers that support ephemeral prompt caching (Anthropic, Qwen,
// OpenAI-compat shims). It resets every CacheCheckpoint to false first so
// stale markers from prior iterations don't accumulate past Anthropic's
// 4-breakpoint limit, then — if not disabled — marks two stable points:
//
//   - messages[0]: the system message (system prompt + tools prefix). Always
//     cacheable for the lifetime of an agent run.
//   - messages[len-1]: the current tail. On the next iteration the tail will
//     have moved forward, so the previous tail becomes a mid-list cache hit
//     against the provider's stored prefix from the previous call.
//
// Providers without ephemeral-cache support ignore the bit; this function
// has no observable effect against them.
//
// Why: the agent loop owns this placement so users don't have to track
// indices manually across iterations. Per-index control is still available
// via openaicompat.WithCacheControl when WithoutPromptCaching is set.
func applyPromptCacheMarkers(msgs []core.ChatMessage, disabled bool) {
	for i := range msgs {
		msgs[i].CacheCheckpoint = false
	}
	if disabled || len(msgs) == 0 {
		return
	}
	msgs[0].CacheCheckpoint = true
	if last := len(msgs) - 1; last > 0 {
		msgs[last].CacheCheckpoint = true
	}
}

// iterationOutcome signals whether to continue to the next iteration or
// terminate the loop with the iterationResult's final AgentResult.
type iterationOutcome int

const (
	iterContinue iterationOutcome = iota
	iterDone
)

// iterationResult is returned by runIteration.
type iterationResult struct {
	outcome iterationOutcome
	final   AgentResult
	err     error
}

// runIteration executes a single iteration of the tool-calling loop.
func runIteration(ctx context.Context, cfg LoopConfig, task AgentTask, ch chan<- core.StreamEvent, state *loopState, i int) iterationResult {
	cfg.Logger.Debug("loop iteration started", "agent", cfg.Name, "iteration", i,
		"tools", len(cfg.Tools), "messages", len(state.messages), "runes", state.messageRuneCount)

	// Emit iteration-start event.
	if ch != nil {
		select {
		case ch <- core.StreamEvent{
			Type: core.EventIterationStart,
			Name: strconv.Itoa(i),
		}:
		case <-ctx.Done():
		}
	}
	iterStart := time.Now()

	iterCtx := ctx
	var iterSpan core.Span
	if cfg.Tracer != nil {
		iterCtx, iterSpan = cfg.Tracer.Start(ctx, "agent.iteration",
			core.IntAttr("iteration", i),
			core.BoolAttr("has_tools", len(cfg.Tools) > 0))
	}

	var llmTrace core.LLMCallTrace
	llmModel := cfg.Provider.Name()
	llmCalled := false

	endIter := func(reason core.FinishReason) {
		dur := time.Since(iterStart)
		if llmCalled {
			trace := core.IterationTrace{
				Iter:         i,
				Model:        llmModel,
				StartedAt:    iterStart,
				Duration:     dur,
				LLMCall:      llmTrace,
				FinishReason: reason,
				Usage: core.Usage{
					InputTokens:  llmTrace.InputTokens,
					OutputTokens: llmTrace.OutputTokens,
				},
			}
			state.iterations = append(state.iterations, trace)
		}

		if iterSpan != nil {
			iterSpan.End()
		}
		if ch != nil {
			select {
			case ch <- core.StreamEvent{
				Type:         core.EventIterationFinish,
				Name:         strconv.Itoa(i),
				Duration:     dur,
				FinishReason: reason,
			}:
			case <-ctx.Done():
			}
		}
	}

	req := core.ChatRequest{Messages: state.messages, ResponseSchema: cfg.ResponseSchema, GenerationParams: cfg.GenParams}

	// PreProcessor hook.
	if err := cfg.Processors.RunPreLLM(iterCtx, &req); err != nil {
		cfg.Logger.Error("pre-processor failed", "agent", cfg.Name, "iteration", i, "error", err)
		if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
			if ch != nil {
				select {
				case ch <- core.StreamEvent{
					Type:           core.EventProcessorSuspended,
					Content:        "pre",
					Protocol:       s.tag,
					SuspendPayload: s.Payload,
				}:
				case <-ctx.Done():
				}
			}
			endIter(core.FinishSuspended)
			return terminateIteration(ctx, cfg, ch, state, core.FinishSuspended, AgentResult{SuspendPayload: s.Payload, SuspendProtocol: s.tag}, s)
		}
		res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
		reason := core.FinishError
		if res.Output != "" {
			reason = core.FinishHalted
		}
		endIter(reason)
		return terminateIteration(ctx, cfg, ch, state, reason, res, retErr)
	}

	var resp core.ChatResponse
	var err error
	streamedThisIter := false

	req.Tools = cfg.Tools

	// PrepareStep hook.
	iterProvider := cfg.Provider
	if cfg.PrepareStep != nil {
		ctrl := &StepControl{Request: &req}
		if err := cfg.PrepareStep(iterCtx, i, ctrl); err != nil {
			cfg.Logger.Error("PrepareStep hook failed", "agent", cfg.Name, "iteration", i, "error", err)
			endIter(core.FinishError)
			return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, fmt.Errorf("PrepareStep: %w", err))
		}
		if ctrl.Model != nil {
			iterProvider = ctrl.Model
			llmModel = iterProvider.Name()
		}
		if ctrl.Tools != nil {
			defs := make([]core.ToolDefinition, len(ctrl.Tools))
			for idx, t := range ctrl.Tools {
				defs[idx] = t.Definition()
			}
			req.Tools = defs
		}
	}

	// Why: placed after RunPreLLM and PrepareStep so any message-list mutations
	// (e.g. processors appending guardrail messages, hooks rewriting tool defs)
	// are visible when we pick the tail. The loop owns CacheCheckpoint placement;
	// callers wanting per-index control should use WithoutPromptCaching and set
	// markers via openaicompat.WithCacheControl directly.
	applyPromptCacheMarkers(req.Messages, cfg.DisablePromptCaching)

	if len(req.Tools) > 0 && ch != nil && !state.hasAgentTools {
		cfg.Logger.Debug("calling LLM (streaming, with tools)", "agent", cfg.Name, "iteration", i, "tool_count", len(req.Tools))
		resp, llmTrace, _, err = callLLM(ctx, iterCtx, cfg, req, iterProvider, ch, state, llmModel, true)
		llmCalled = true
		streamedThisIter = true
	} else if len(req.Tools) > 0 {
		cfg.Logger.Debug("calling LLM (with tools)", "agent", cfg.Name, "iteration", i, "tool_count", len(req.Tools))
		resp, llmTrace, _, err = callLLM(ctx, iterCtx, cfg, req, iterProvider, nil, state, llmModel, false)
		llmCalled = true
	} else {
		useStream := ch != nil
		cfg.Logger.Debug("calling LLM (no tools)", "agent", cfg.Name, "iteration", i, "streaming", useStream)
		resp, llmTrace, _, err = callLLM(ctx, iterCtx, cfg, req, iterProvider, ch, state, llmModel, useStream)
		streamedThisIter = useStream
		llmCalled = true
	}

	if err != nil {
		cfg.Logger.Error("LLM call failed", "agent", cfg.Name, "iteration", i, "error", err, "duration", llmTrace.Duration)
		endIter(core.FinishError)
		if r, handled := handleOnError(iterCtx, cfg, state, i, err); handled {
			if r.outcome == iterDone {
				finalizeRun(ctx, ch, state, cfg.Name, core.FinishError, r.final)
			}
			return r
		}
		return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, err)
	}
	cfg.Logger.Debug("LLM call completed", "agent", cfg.Name, "iteration", i,
		"duration", llmTrace.Duration,
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
		"tool_calls", len(resp.ToolCalls))
	state.totalUsage.InputTokens += resp.Usage.InputTokens
	state.totalUsage.OutputTokens += resp.Usage.OutputTokens

	captureProviderMeta(state, &resp)

	// PostProcessor hook.
	if r, handled := runPostLLMOrHandle(ctx, iterCtx, cfg, task, ch, state, &resp, endIter); handled {
		return r
	}

	// Capture and emit thinking content.
	if resp.Thinking != "" {
		state.lastThinking = resp.Thinking
		if ch != nil {
			select {
			case ch <- core.StreamEvent{Type: core.EventThinking, Content: resp.Thinking}:
			case <-ctx.Done():
			}
		}
	}

	// No tool calls — final response.
	if len(resp.ToolCalls) == 0 {
		cfg.Logger.Debug("final response (no tool calls)", "agent", cfg.Name, "iteration", i)
		content := resp.Content
		if content == "" {
			content = state.lastAgentOutput
		}
		if ch != nil && !streamedThisIter {
			select {
			case ch <- core.StreamEvent{Type: core.EventTextDelta, Content: content}:
			case <-ctx.Done():
			}
		}

		// OnIterationComplete hook (no-tool-call / final path).
		if cfg.OnIterationComplete != nil {
			state.messages = append(state.messages, core.ChatMessage{
				Role:    "assistant",
				Content: content,
			})
			state.messageRuneCount += utf8.RuneCountInString(content)

			snap := &IterationSnapshot{
				Response:  &resp,
				ToolCalls: nil,
			}
			decision, hookErr := cfg.OnIterationComplete(iterCtx, i, snap)
			if hookErr != nil {
				endIter(core.FinishError)
				return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, fmt.Errorf("OnIterationComplete: %w", hookErr))
			}
			if decision.IsStop() {
				return finalizeIterationStop(ctx, cfg, ch, state, decision, endIter)
			}
			if decision.IsInject() {
				for _, m := range decision.Msgs() {
					state.messages = append(state.messages, m)
					state.messageRuneCount += utf8.RuneCountInString(m.Content)
				}
				endIter(core.FinishStop)
				return iterationResult{outcome: iterContinue}
			}
			// Continue: fall through to natural iterDone.
		}

		endIter(core.FinishStop)
		cfg.Mem.PersistTurn(iterCtx, cfg.Name, task, task.Input, content, state.steps)
		result := AgentResult{
			Output:      content,
			Thinking:    state.lastThinking,
			Attachments: mergeAttachments(state.accumulatedAttachments, resp.Attachments),
		}
		state.patchTerminal(&result, core.FinishStop)
		emitObjectFinish(ctx, ch, cfg.ResponseSchema, content, &result)
		finalizeRun(ctx, ch, state, cfg.Name, core.FinishStop, result)
		return iterationResult{
			outcome: iterDone,
			final:   result,
		}
	}

	if iterSpan != nil {
		iterSpan.SetAttr(core.IntAttr("tool_count", len(resp.ToolCalls)))
	}

	// Append assistant message with tool calls.
	state.messages = append(state.messages, core.ChatMessage{
		Role:      "assistant",
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	})
	state.messageRuneCount += utf8.RuneCountInString(resp.Content)

	// Emit tool-call-start events before dispatch.
	if ch != nil {
		for _, tc := range resp.ToolCalls {
			select {
			case ch <- core.StreamEvent{Type: core.EventToolCallStart, ID: tc.ID, Name: tc.Name, Args: tc.Args}:
			case <-ctx.Done():
			}
		}

		var agents, directTools []string
		for _, tc := range resp.ToolCalls {
			if after, ok := strings.CutPrefix(tc.Name, core.ToolPrefixAgent); ok {
				agents = append(agents, after)
			} else {
				directTools = append(directTools, tc.Name)
			}
		}
		if len(agents) > 0 {
			select {
			case ch <- core.StreamEvent{
				Type:    core.EventRoutingDecision,
				Name:    cfg.Name,
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
	cfg.Logger.Info("dispatching tool calls", "agent", cfg.Name, "iteration", i, "tools", toolNames)
	fileSinkCh, waitFileSink := newFileCapturingSink(ctx, ch, state)
	iterCtx = contextWithStreamSink(iterCtx, fileSinkCh)
	dispatchStart := time.Now()
	results := dispatchParallel(iterCtx, resp.ToolCalls, cfg.Dispatch, cfg.MaxParallelDispatch)
	cfg.Logger.Debug("tool dispatch completed", "agent", cfg.Name, "iteration", i, "duration", time.Since(dispatchStart))
	if fileSinkCh != nil {
		close(fileSinkCh)
	}
	waitFileSink()

	// postProcessed holds the post-tool-processor-mutated result for each call.
	// Built inside the loop below and consumed by the OnIterationComplete snapshot
	// so the hook always sees the post-processed content.
	postProcessed := make([]core.ToolResult, len(resp.ToolCalls))

	// Process results sequentially.
	for j, tc := range resp.ToolCalls {
		state.totalUsage.InputTokens += results[j].usage.InputTokens
		state.totalUsage.OutputTokens += results[j].usage.OutputTokens

		if results[j].isError {
			cfg.Logger.Warn("tool call returned error", "agent", cfg.Name, "tool", tc.Name, "error", results[j].content, "duration", results[j].duration)
		} else {
			cfg.Logger.Debug("tool call result", "agent", cfg.Name, "tool", tc.Name, "duration", results[j].duration, "result_len", len(results[j].content))
		}

		// Emit tool-call-result event.
		if ch != nil {
			select {
			case ch <- core.StreamEvent{
				Type:     core.EventToolCallResult,
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
		state.steps = appendStepBounded(state.steps, trace, cfg.MaxStepsResolved)

		// Accumulate attachments.
		for _, a := range results[j].attachments {
			aSize := int64(len(a.Data))
			if len(state.accumulatedAttachments) >= maxAccumulatedAttachments ||
				state.accumulatedAttachmentBytes+aSize > state.attachByteBudget {
				break
			}
			state.accumulatedAttachments = append(state.accumulatedAttachments, a)
			state.accumulatedAttachmentBytes += aSize
		}

		result := core.ToolResult{Content: json.RawMessage(results[j].content)}
		if err := cfg.Processors.RunPostTool(iterCtx, tc, &result); err != nil {
			if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
				if ch != nil {
					select {
					case ch <- core.StreamEvent{
						Type:           core.EventToolCallSuspended,
						ID:             tc.ID,
						Name:           tc.Name,
						Args:           tc.Args,
						Protocol:       s.tag,
						SuspendPayload: s.Payload,
					}:
					case <-ctx.Done():
					}
				}
				endIter(core.FinishSuspended)
				return terminateIteration(ctx, cfg, ch, state, core.FinishSuspended, AgentResult{SuspendPayload: s.Payload, SuspendProtocol: s.tag}, s)
			}
			res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
			reason := core.FinishError
			if res.Output != "" {
				reason = core.FinishHalted
			}
			endIter(reason)
			return terminateIteration(ctx, cfg, ch, state, reason, res, retErr)
		}
		// Record the post-processed result so OnIterationComplete sees mutated content.
		postProcessed[j] = result

		// Chunk large tool results transparently.
		// Why: instead of hinting the LLM to call read_full_result, split the
		// content into multiple sequential tool-result messages (all sharing the
		// same call ID). The LLM sees them as one logical result without needing
		// to issue a follow-up tool call. ToolResultStore still receives the full
		// payload for post-hoc inspection.
		content := string(result.Content)
		maxLen := cfg.MaxToolResultLen
		if maxLen == 0 {
			maxLen = maxToolResultMessageLen
		}
		if cfg.ToolResultStore != nil {
			if _, putErr := cfg.ToolResultStore.Put(iterCtx, result.Content); putErr != nil {
				cfg.Logger.Warn("tool result store put failed", "agent", cfg.Name, "error", putErr)
			}
		}
		if utf8.RuneCountInString(content) > maxLen {
			for _, chunk := range splitContentRunes(content, maxLen) {
				state.messages = append(state.messages, core.ToolResultMessage(tc.ID, chunk))
				state.messageRuneCount += utf8.RuneCountInString(chunk)
			}
		} else {
			state.messages = append(state.messages, core.ToolResultMessage(tc.ID, content))
			state.messageRuneCount += utf8.RuneCountInString(content)
		}

		if strings.HasPrefix(tc.Name, core.ToolPrefixAgent) {
			state.lastAgentOutput = string(result.Content)
		}

		// Collect citations.
		if !results[j].isError && cfg.LookupTool != nil {
			if t, ok := cfg.LookupTool(tc.Name); ok {
				if sourced, ok := t.(core.Sourced); ok {
					state.sources = append(state.sources, sourced.Sources()...)
				}
			}
		}
	}
	// Compress context if over budget.
	if state.compressThreshold > 0 && state.messageRuneCount > state.compressThreshold {
		cfg.Logger.Info("context compression triggered", "agent", cfg.Name, "iteration", i, "runes", state.messageRuneCount, "threshold", state.compressThreshold)
		state.messages, state.messageRuneCount = compressMessages(iterCtx, cfg, task, state.messages, 2, state.messageRuneCount)
	}

	// OnIterationComplete hook (tool-call path).
	if cfg.OnIterationComplete != nil {
		var firstTrace StepTrace
		if len(state.steps) > 0 {
			firstTrace = state.steps[len(state.steps)-len(resp.ToolCalls)]
		}
		snap := &IterationSnapshot{
			Response:    &resp,
			ToolCalls:   resp.ToolCalls,
			ToolResults: postProcessed,
			Trace:       firstTrace,
		}
		decision, hookErr := cfg.OnIterationComplete(iterCtx, i, snap)
		if hookErr != nil {
			endIter(core.FinishError)
			return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, fmt.Errorf("OnIterationComplete: %w", hookErr))
		}
		if decision.IsStop() {
			return finalizeIterationStop(ctx, cfg, ch, state, decision, endIter)
		}
		if decision.IsInject() {
			for _, m := range decision.Msgs() {
				state.messages = append(state.messages, m)
				state.messageRuneCount += utf8.RuneCountInString(m.Content)
			}
		}
		// Continue: fall through to normal continuation
	}

	endIter(core.FinishToolCalls)
	return iterationResult{outcome: iterContinue}
}

// callLLM dispatches one LLM call (streaming or non-streaming).
func callLLM(fwdCtx, spanCtx context.Context, cfg LoopConfig, req core.ChatRequest, provider core.Provider, ch chan<- core.StreamEvent, state *loopState, llmModel string, useStream bool) (core.ChatResponse, core.LLMCallTrace, bool, error) {
	start := time.Now()
	llmCtx := spanCtx
	var llmSpan core.Span
	if cfg.Tracer != nil {
		llmCtx, llmSpan = cfg.Tracer.Start(spanCtx, "llm.generate",
			core.StringAttr("provider", llmModel))
	}

	var resp core.ChatResponse
	var err error
	streamed := false

	// endLLMSpan closes llmSpan with usage attrs. Called BEFORE wait() in the
	// streaming branch so the span measures LLM call time only, not forwarder
	// drain time.
	endLLMSpan := func() {
		if llmSpan == nil {
			return
		}
		llmSpan.SetAttr(
			core.IntAttr("input_tokens", resp.Usage.InputTokens),
			core.IntAttr("output_tokens", resp.Usage.OutputTokens),
			core.StringAttr("finish_reason", string(resp.FinishReason)),
		)
		llmSpan.End()
	}

	if useStream {
		iterCh, wait := newObjectStreamForwarder(fwdCtx, ch, defaultIterChBufSize, state, cfg.ResponseSchema)
		resp, err = provider.ChatStream(llmCtx, req, iterCh)
		endLLMSpan()
		wait()
		streamed = true
	} else {
		resp, err = core.Chat(llmCtx, provider, req)
		endLLMSpan()
	}

	trace := core.LLMCallTrace{
		Duration:     time.Since(start),
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		FinishReason: resp.FinishReason,
	}
	return resp, trace, streamed, err
}

// runPostLLMOrHandle runs the post-LLM processor chain.
func runPostLLMOrHandle(
	ctx, iterCtx context.Context,
	cfg LoopConfig,
	task AgentTask,
	ch chan<- core.StreamEvent,
	state *loopState,
	resp *core.ChatResponse,
	endIter func(core.FinishReason),
) (iterationResult, bool) {
	if err := cfg.Processors.RunPostLLM(iterCtx, resp); err != nil {
		if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
			if ch != nil {
				select {
				case ch <- core.StreamEvent{
					Type:           core.EventProcessorSuspended,
					Content:        "post",
					Protocol:       s.tag,
					SuspendPayload: s.Payload,
				}:
				case <-ctx.Done():
				}
			}
			if endIter != nil {
				endIter(core.FinishSuspended)
			}
			suspResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: core.FinishSuspended, SuspendPayload: s.Payload, SuspendProtocol: s.tag, Iterations: state.iterations}
			finalizeRun(ctx, ch, state, cfg.Name, core.FinishSuspended, suspResult)
			return iterationResult{outcome: iterDone, final: suspResult, err: s}, true
		}
		res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
		reason := core.FinishError
		if res.Output != "" {
			reason = core.FinishHalted
		}
		res.FinishReason = reason
		if endIter != nil {
			endIter(reason)
		}
		finalizeRun(ctx, ch, state, cfg.Name, reason, res)
		return iterationResult{outcome: iterDone, final: res, err: retErr}, true
	}
	return iterationResult{}, false
}

// captureProviderMeta appends resp.Warnings to state.lastWarnings and
// updates state.lastProviderMeta.
func captureProviderMeta(state *loopState, resp *core.ChatResponse) {
	if len(resp.Warnings) > 0 {
		state.lastWarnings = append(state.lastWarnings, resp.Warnings...)
	}
	if len(resp.ProviderMeta) > 0 {
		state.lastProviderMeta = resp.ProviderMeta
	}
}

// handleOnError consults cfg.OnError when a non-graceful LLM error occurs.
func handleOnError(ctx context.Context, cfg LoopConfig, state *loopState, i int, err error) (iterationResult, bool) {
	if ctx.Err() != nil {
		return iterationResult{}, false
	}
	var suspended *ErrSuspended
	if errors.As(err, &suspended) {
		return iterationResult{}, false
	}

	if cfg.OnError == nil {
		return iterationResult{}, false
	}

	decision, hookErr := cfg.OnError(ctx, i, err)
	if hookErr != nil {
		return iterationResult{
			outcome: iterDone,
			final:   AgentResult{Usage: state.totalUsage, Steps: state.steps},
			err:     fmt.Errorf("OnError: %w", hookErr),
		}, true
	}

	if decision.IsRetry() {
		if fb := decision.Feedback(); fb != "" {
			msg := core.ChatMessage{Role: core.RoleUser, Content: fb}
			state.messages = append(state.messages, msg)
			state.messageRuneCount += utf8.RuneCountInString(fb)
		}
		return iterationResult{outcome: iterContinue}, true
	}
	if decision.IsHalt() {
		return iterationResult{outcome: iterDone, final: decision.Result()}, true
	}
	// IsPropagate or unknown: let caller propagate the original error.
	return iterationResult{}, false
}

// terminateIteration builds the standard AgentResult for a terminal exit.
func terminateIteration(ctx context.Context, cfg LoopConfig, ch chan<- core.StreamEvent, state *loopState, reason core.FinishReason, extra AgentResult, err error) iterationResult {
	result := AgentResult{
		Output:          extra.Output,
		Thinking:        extra.Thinking,
		Attachments:     extra.Attachments,
		SuspendPayload:  extra.SuspendPayload,
		SuspendProtocol: extra.SuspendProtocol,
	}
	state.patchTerminal(&result, reason)
	finalizeRun(ctx, ch, state, cfg.Name, reason, result)
	return iterationResult{outcome: iterDone, final: result, err: err}
}

// finalizeIterationStop handles the IsStop() branch of an OnIterationComplete
// decision, attaching accumulated loop state to the hook's result and
// finalizing the run.
func finalizeIterationStop(ctx context.Context, cfg LoopConfig, ch chan<- core.StreamEvent, state *loopState, decision IterationDecision, endIter func(core.FinishReason)) iterationResult {
	endIter(core.FinishStop)
	r := decision.Result()
	state.patchTerminal(&r, core.FinishStop)
	finalizeRun(ctx, ch, state, cfg.Name, core.FinishStop, r)
	return iterationResult{outcome: iterDone, final: r}
}

// splitContentRunes splits s into chunks of at most maxRunes runes each.
// Splitting is rune-safe: chunks never break in the middle of a multi-byte
// UTF-8 sequence. If s fits within maxRunes, a single-element slice is
// returned. maxRunes must be > 0.
func splitContentRunes(s string, maxRunes int) []string {
	runes := []rune(s)
	total := len(runes)
	if total <= maxRunes {
		return []string{s}
	}
	chunks := make([]string, 0, (total+maxRunes-1)/maxRunes)
	for i := 0; i < total; i += maxRunes {
		end := i + maxRunes
		if end > total {
			end = total
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}
