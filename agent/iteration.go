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
				endIter(core.FinishStop)
				r := decision.Result()
				r.FinishReason = core.FinishStop
				r.Warnings = state.lastWarnings
				r.ProviderMeta = state.lastProviderMeta
				r.Files = state.files
				r.Iterations = state.iterations
				r.Sources = state.sources
				finalizeRun(ctx, ch, state, cfg.Name, core.FinishStop, r)
				return iterationResult{outcome: iterDone, final: r}
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
			Output:       content,
			Thinking:     state.lastThinking,
			Attachments:  mergeAttachments(state.accumulatedAttachments, resp.Attachments),
			Usage:        state.totalUsage,
			Steps:        state.steps,
			FinishReason: core.FinishStop,
			Warnings:     state.lastWarnings,
			ProviderMeta: state.lastProviderMeta,
			Files:        state.files,
			Iterations:   state.iterations,
			Sources:      state.sources,
		}
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
			if after, ok := strings.CutPrefix(tc.Name, "agent_"); ok {
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
		// Truncate large tool results.
		content := string(result.Content)
		maxLen := cfg.MaxToolResultLen
		if maxLen == 0 {
			maxLen = maxToolResultMessageLen
		}
		msgContent := content
		if utf8.RuneCountInString(content) > maxLen {
			inline := TruncateStr(content, maxLen)
			total := utf8.RuneCountInString(content)
			if cfg.ToolResultStore != nil {
				id, putErr := cfg.ToolResultStore.Put(iterCtx, result.Content)
				if putErr == nil {
					msgContent = inline + fmt.Sprintf(
						"\n\n[truncated at %d runes of %d total. Use read_full_result(id=%q, offset=%d, length=50000) for more]",
						maxLen, total, id, maxLen)
				} else {
					cfg.Logger.Warn("tool result store put failed, falling back to legacy marker",
						"agent", cfg.Name, "error", putErr)
					msgContent = inline + "\n\n[output truncated — original was longer]"
				}
			} else {
				msgContent = inline + "\n\n[output truncated — original was longer]"
			}
		}
		state.messages = append(state.messages, core.ToolResultMessage(tc.ID, msgContent))
		state.messageRuneCount += utf8.RuneCountInString(msgContent)

		if strings.HasPrefix(tc.Name, "agent_") {
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
		toolResults := make([]core.ToolResult, len(resp.ToolCalls))
		for j := range resp.ToolCalls {
			toolResults[j] = core.ToolResult{Content: json.RawMessage(results[j].content)}
		}
		var firstTrace StepTrace
		if len(state.steps) > 0 {
			firstTrace = state.steps[len(state.steps)-len(resp.ToolCalls)]
		}
		snap := &IterationSnapshot{
			Response:    &resp,
			ToolCalls:   resp.ToolCalls,
			ToolResults: toolResults,
			Trace:       firstTrace,
		}
		decision, hookErr := cfg.OnIterationComplete(iterCtx, i, snap)
		if hookErr != nil {
			endIter(core.FinishError)
			return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, fmt.Errorf("OnIterationComplete: %w", hookErr))
		}
		if decision.IsStop() {
			endIter(core.FinishStop)
			r := decision.Result()
			r.FinishReason = core.FinishStop
			r.Warnings = state.lastWarnings
			r.ProviderMeta = state.lastProviderMeta
			r.Files = state.files
			r.Iterations = state.iterations
			r.Sources = state.sources
			finalizeRun(ctx, ch, state, cfg.Name, core.FinishStop, r)
			return iterationResult{outcome: iterDone, final: r}
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

	if useStream {
		iterCh, wait := newObjectStreamForwarder(fwdCtx, ch, defaultIterChBufSize, state, cfg.ResponseSchema)
		resp, err = provider.ChatStream(llmCtx, req, iterCh)
		if llmSpan != nil {
			llmSpan.SetAttr(
				core.IntAttr("input_tokens", resp.Usage.InputTokens),
				core.IntAttr("output_tokens", resp.Usage.OutputTokens),
				core.StringAttr("finish_reason", string(resp.FinishReason)),
			)
			llmSpan.End()
		}
		wait()
		streamed = true
	} else {
		resp, err = core.Chat(llmCtx, provider, req)
		if llmSpan != nil {
			llmSpan.SetAttr(
				core.IntAttr("input_tokens", resp.Usage.InputTokens),
				core.IntAttr("output_tokens", resp.Usage.OutputTokens),
				core.StringAttr("finish_reason", string(resp.FinishReason)),
			)
			llmSpan.End()
		}
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
			state.messageRuneCount += len([]rune(fb))
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
		Usage:           state.totalUsage,
		Steps:           state.steps,
		FinishReason:    reason,
		Warnings:        state.lastWarnings,
		ProviderMeta:    state.lastProviderMeta,
		Files:           state.files,
		Iterations:      state.iterations,
		Sources:         state.sources,
		SuspendPayload:  extra.SuspendPayload,
		SuspendProtocol: extra.SuspendProtocol,
	}
	finalizeRun(ctx, ch, state, cfg.Name, reason, result)
	return iterationResult{outcome: iterDone, final: result, err: err}
}
