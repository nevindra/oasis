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

	lastWarnings     []string
	lastProviderMeta json.RawMessage

	// Files aggregated from EventFileAttachment events emitted by the provider
	// or tool implementations; copied onto AgentResult.Files on all exit paths.
	files []Attachment

	iterations []IterationTrace

	// Why: tools implementing core.Sourced are collected here so the caller
	// gets a unified citation list across all tool calls in a run.
	// Copied onto AgentResult.Sources on every terminal exit path.
	sources []core.Source
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

	// Emit iteration-start event.
	if ch != nil {
		select {
		case ch <- StreamEvent{
			Type: EventIterationStart,
			Name: strconv.Itoa(i),
		}:
		case <-ctx.Done():
		}
	}
	iterStart := time.Now()

	iterCtx := ctx
	var iterSpan Span
	if cfg.tracer != nil {
		iterCtx, iterSpan = cfg.tracer.Start(ctx, "agent.iteration",
			IntAttr("iteration", i),
			BoolAttr("has_tools", len(cfg.tools) > 0))
	}

	// llmCalled guards IterationTrace recording: endIter only appends a trace
	// when an actual provider call completed (pre-processor errors may exit
	// before any LLM call, and should not produce a trace).
	var llmTrace LLMCallTrace
	llmModel := cfg.provider.Name() // default; may be updated if PrepareStep overrides provider
	llmCalled := false

	// endIter closes the tracing span, emits EventIterationFinish, and appends
	// the completed IterationTrace to state.iterations.
	// reason is the FinishReason for this iteration (e.g. FinishStop,
	// FinishToolCalls, FinishSuspended, FinishError, FinishHalted).
	endIter := func(reason FinishReason) {
		dur := time.Since(iterStart)
		// Only record a trace if an LLM call actually happened this iteration.
		if llmCalled {
			trace := IterationTrace{
				Iter:         i,
				Model:        llmModel,
				StartedAt:    iterStart,
				Duration:     dur,
				LLMCall:      llmTrace,
				FinishReason: reason,
				Usage: Usage{
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
			case ch <- StreamEvent{
				Type:         EventIterationFinish,
				Name:         strconv.Itoa(i),
				Duration:     dur,
				FinishReason: reason,
			}:
			case <-ctx.Done():
			}
		}
	}

	req := ChatRequest{Messages: state.messages, ResponseSchema: cfg.responseSchema, GenerationParams: cfg.generationParams}

	// PreProcessor hook.
	if err := cfg.processors.RunPreLLM(iterCtx, &req); err != nil {
		cfg.logger.Error("pre-processor failed", "agent", cfg.name, "iteration", i, "error", err)
		if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
			if ch != nil {
				select {
				case ch <- StreamEvent{
					Type:           EventProcessorSuspended,
					Content:        "pre",
					Protocol:       s.tag,
					SuspendPayload: s.Payload,
				}:
				case <-ctx.Done():
				}
			}
			endIter(FinishSuspended)
			suspResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishSuspended, SuspendPayload: s.Payload, SuspendProtocol: s.tag, Iterations: state.iterations}
			finalizeRun(ctx, ch, state, cfg.name, FinishSuspended, suspResult)
			return iterationResult{outcome: iterDone, final: suspResult, err: s}
		}
		res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
		reason := FinishError
		if res.Output != "" {
			reason = FinishHalted
		}
		res.FinishReason = reason
		endIter(reason)
		finalizeRun(ctx, ch, state, cfg.name, reason, res)
		return iterationResult{outcome: iterDone, final: res, err: retErr}
	}

	var resp ChatResponse
	var err error
	streamedThisIter := false // true when ChatStream already emitted text deltas

	req.Tools = cfg.tools

	// PrepareStep hook: mutate the request and optionally override the provider
	// or tools for this iteration only.
	iterProvider := cfg.provider
	if cfg.prepareStep != nil {
		ctrl := &StepControl{Request: &req}
		if err := cfg.prepareStep(iterCtx, i, ctrl); err != nil {
			cfg.logger.Error("PrepareStep hook failed", "agent", cfg.name, "iteration", i, "error", err)
			endIter(FinishError)
			errResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishError}
			finalizeRun(ctx, ch, state, cfg.name, FinishError, errResult)
			return iterationResult{outcome: iterDone, final: errResult, err: fmt.Errorf("PrepareStep: %w", err)}
		}
		if ctrl.Model != nil {
			iterProvider = ctrl.Model
			llmModel = iterProvider.Name() // keep trace accurate when PrepareStep overrides the provider
		}
		if ctrl.Tools != nil {
			defs := make([]ToolDefinition, len(ctrl.Tools))
			for idx, t := range ctrl.Tools {
				defs[idx] = t.Definition()
			}
			req.Tools = defs
		}
	}

	llmStart := time.Now()
	if len(req.Tools) > 0 && ch != nil && !state.hasAgentTools {
		// Streaming with tools (single agent only): intermediate channel so the
		// provider's defer-close doesn't shut down the main stream. Networks
		// use Chat() to preserve router text-delta deduplication.
		// newObjectStreamForwarder intercepts EventFileAttachment events into
		// state.files and emits EventObjectDelta snapshots when a schema is set.
		cfg.logger.Debug("calling LLM (streaming, with tools)", "agent", cfg.name, "iteration", i, "tool_count", len(req.Tools))
		iterCh, wait := newObjectStreamForwarder(ctx, ch, defaultIterChBufSize, state, cfg.responseSchema)
		llmCtx := iterCtx
		var llmSpan Span
		if cfg.tracer != nil {
			llmCtx, llmSpan = cfg.tracer.Start(iterCtx, "llm.generate",
				StringAttr("provider", llmModel))
		}
		resp, err = iterProvider.ChatStream(llmCtx, req, iterCh)
		if llmSpan != nil {
			llmSpan.SetAttr(
				IntAttr("input_tokens", resp.Usage.InputTokens),
				IntAttr("output_tokens", resp.Usage.OutputTokens),
				StringAttr("finish_reason", string(resp.FinishReason)),
			)
			llmSpan.End()
		}
		llmTrace = LLMCallTrace{
			Duration:     time.Since(llmStart),
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			FinishReason: resp.FinishReason,
		}
		llmCalled = true
		wait()
		streamedThisIter = true
	} else if len(req.Tools) > 0 {
		cfg.logger.Debug("calling LLM (with tools)", "agent", cfg.name, "iteration", i, "tool_count", len(req.Tools))
		llmCtx := iterCtx
		var llmSpan Span
		if cfg.tracer != nil {
			llmCtx, llmSpan = cfg.tracer.Start(iterCtx, "llm.generate",
				StringAttr("provider", llmModel))
		}
		resp, err = core.Chat(llmCtx, iterProvider, req)
		if llmSpan != nil {
			llmSpan.SetAttr(
				IntAttr("input_tokens", resp.Usage.InputTokens),
				IntAttr("output_tokens", resp.Usage.OutputTokens),
				StringAttr("finish_reason", string(resp.FinishReason)),
			)
			llmSpan.End()
		}
		llmTrace = LLMCallTrace{
			Duration:     time.Since(llmStart),
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			FinishReason: resp.FinishReason,
		}
		llmCalled = true
	} else if ch != nil {
		// No tools, streaming — terminal path (single-shot stream then return).
		// newObjectStreamForwarder intercepts EventFileAttachment events into
		// state.files and emits EventObjectDelta snapshots when a schema is set.
		cfg.logger.Debug("calling LLM (streaming, no tools)", "agent", cfg.name, "iteration", i)
		iterCh, wait := newObjectStreamForwarder(ctx, ch, defaultIterChBufSize, state, cfg.responseSchema)
		llmCtx := iterCtx
		var llmSpan Span
		if cfg.tracer != nil {
			llmCtx, llmSpan = cfg.tracer.Start(iterCtx, "llm.generate",
				StringAttr("provider", llmModel))
		}
		resp, err = iterProvider.ChatStream(llmCtx, req, iterCh)
		if llmSpan != nil {
			llmSpan.SetAttr(
				IntAttr("input_tokens", resp.Usage.InputTokens),
				IntAttr("output_tokens", resp.Usage.OutputTokens),
				StringAttr("finish_reason", string(resp.FinishReason)),
			)
			llmSpan.End()
		}
		llmTrace = LLMCallTrace{
			Duration:     time.Since(llmStart),
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			FinishReason: resp.FinishReason,
		}
		llmCalled = true
		wait()
		if err != nil {
			endIter(FinishError)
			if r, handled := handleOnError(iterCtx, cfg, state, i, err); handled {
				// iterContinue means retry — ch stays open; no finalizeRun.
				// iterDone means the hook handled it terminally.
				if r.outcome == iterDone {
					finalizeRun(ctx, ch, state, cfg.name, FinishError, r.final)
				}
				return r
			}
			errResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishError}
			finalizeRun(ctx, ch, state, cfg.name, FinishError, errResult)
			return iterationResult{outcome: iterDone, final: errResult, err: err}
		}
		state.totalUsage.InputTokens += resp.Usage.InputTokens
		state.totalUsage.OutputTokens += resp.Usage.OutputTokens

		captureProviderMeta(state, &resp)

		// PostProcessor hook (response already streamed, but processors still
		// run for side effects like logging and validation).
		if err := cfg.processors.RunPostLLM(iterCtx, &resp); err != nil {
			if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
				if ch != nil {
					select {
					case ch <- StreamEvent{
						Type:           EventProcessorSuspended,
						Content:        "post",
						Protocol:       s.tag,
						SuspendPayload: s.Payload,
					}:
					case <-ctx.Done():
					}
				}
				endIter(FinishSuspended)
				suspResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishSuspended, SuspendPayload: s.Payload, SuspendProtocol: s.tag, Iterations: state.iterations}
				finalizeRun(ctx, ch, state, cfg.name, FinishSuspended, suspResult)
				return iterationResult{outcome: iterDone, final: suspResult, err: s}
			}
			res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
			reason := FinishError
			if res.Output != "" {
				reason = FinishHalted
			}
			res.FinishReason = reason
			endIter(reason)
			finalizeRun(ctx, ch, state, cfg.name, reason, res)
			return iterationResult{outcome: iterDone, final: res, err: retErr}
		}

		// OnIterationComplete hook (streaming no-tools path).
		if cfg.onIterationComplete != nil {
			// Append the assistant response to history before running the hook
			// so that an InjectFeedback decision places feedback after it.
			state.messages = append(state.messages, ChatMessage{
				Role:    "assistant",
				Content: resp.Content,
			})
			state.messageRuneCount += utf8.RuneCountInString(resp.Content)

			snap := &IterationSnapshot{
				Response:  &resp,
				ToolCalls: nil,
			}
			decision, hookErr := cfg.onIterationComplete(iterCtx, i, snap)
			if hookErr != nil {
				endIter(FinishError)
				errResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishError}
				finalizeRun(ctx, ch, state, cfg.name, FinishError, errResult)
				return iterationResult{outcome: iterDone, final: errResult, err: fmt.Errorf("OnIterationComplete: %w", hookErr)}
			}
			if decision.action == decisionStop {
				endIter(FinishStop)
				r := decision.result
				r.FinishReason = FinishStop
				r.Warnings = state.lastWarnings
				r.ProviderMeta = state.lastProviderMeta
				r.Files = state.files
				r.Iterations = state.iterations
				r.Sources = state.sources
				finalizeRun(ctx, ch, state, cfg.name, FinishStop, r)
				return iterationResult{outcome: iterDone, final: r}
			}
			if decision.action == decisionInject {
				// Inject feedback messages and re-run. Note: content was already
				// streamed to the caller before this point.
				for _, m := range decision.msgs {
					state.messages = append(state.messages, m)
					state.messageRuneCount += utf8.RuneCountInString(m.Content)
				}
				endIter(FinishStop)
				return iterationResult{outcome: iterContinue}
			}
			// decisionContinue: fall through to natural iterDone.
		}

		endIter(FinishStop)
		cfg.mem.PersistMessages(iterCtx, cfg.name, task, task.Input, resp.Content, state.steps)
		result := AgentResult{
			Output:       resp.Content,
			Thinking:     resp.Thinking,
			Attachments:  mergeAttachments(state.accumulatedAttachments, resp.Attachments),
			Usage:        state.totalUsage,
			Steps:        state.steps,
			FinishReason: FinishStop,
			Warnings:     state.lastWarnings,
			ProviderMeta: state.lastProviderMeta,
			Files:        state.files,
			Iterations:   state.iterations,
			Sources:      state.sources,
		}
		emitObjectFinish(ctx, ch, cfg.responseSchema, resp.Content, &result)
		finalizeRun(ctx, ch, state, cfg.name, FinishStop, result)
		return iterationResult{
			outcome: iterDone,
			final:   result,
		}
	} else {
		cfg.logger.Debug("calling LLM (no tools)", "agent", cfg.name, "iteration", i)
		llmCtx := iterCtx
		var llmSpan Span
		if cfg.tracer != nil {
			llmCtx, llmSpan = cfg.tracer.Start(iterCtx, "llm.generate",
				StringAttr("provider", llmModel))
		}
		resp, err = core.Chat(llmCtx, iterProvider, req)
		if llmSpan != nil {
			llmSpan.SetAttr(
				IntAttr("input_tokens", resp.Usage.InputTokens),
				IntAttr("output_tokens", resp.Usage.OutputTokens),
				StringAttr("finish_reason", string(resp.FinishReason)),
			)
			llmSpan.End()
		}
		llmTrace = LLMCallTrace{
			Duration:     time.Since(llmStart),
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			FinishReason: resp.FinishReason,
		}
		llmCalled = true
	}

	if err != nil {
		cfg.logger.Error("LLM call failed", "agent", cfg.name, "iteration", i, "error", err, "duration", time.Since(llmStart))
		endIter(FinishError)
		if r, handled := handleOnError(iterCtx, cfg, state, i, err); handled {
			// iterContinue means retry — ch stays open; no finalizeRun.
			if r.outcome == iterDone {
				finalizeRun(ctx, ch, state, cfg.name, FinishError, r.final)
			}
			return r
		}
		errResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishError}
		finalizeRun(ctx, ch, state, cfg.name, FinishError, errResult)
		return iterationResult{outcome: iterDone, final: errResult, err: err}
	}
	cfg.logger.Debug("LLM call completed", "agent", cfg.name, "iteration", i,
		"duration", time.Since(llmStart),
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens,
		"tool_calls", len(resp.ToolCalls))
	state.totalUsage.InputTokens += resp.Usage.InputTokens
	state.totalUsage.OutputTokens += resp.Usage.OutputTokens

	captureProviderMeta(state, &resp)

	// PostProcessor hook.
	if err := cfg.processors.RunPostLLM(iterCtx, &resp); err != nil {
		if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
			if ch != nil {
				select {
				case ch <- StreamEvent{
					Type:           EventProcessorSuspended,
					Content:        "post",
					Protocol:       s.tag,
					SuspendPayload: s.Payload,
				}:
				case <-ctx.Done():
				}
			}
			endIter(FinishSuspended)
			suspResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishSuspended, SuspendPayload: s.Payload, SuspendProtocol: s.tag, Iterations: state.iterations}
			finalizeRun(ctx, ch, state, cfg.name, FinishSuspended, suspResult)
			return iterationResult{outcome: iterDone, final: suspResult, err: s}
		}
		res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
		reason := FinishError
		if res.Output != "" {
			reason = FinishHalted
		}
		res.FinishReason = reason
		endIter(reason)
		finalizeRun(ctx, ch, state, cfg.name, reason, res)
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

		// OnIterationComplete hook (no-tool-call / final path).
		if cfg.onIterationComplete != nil {
			// Append the assistant response to history before running the hook
			// so that an InjectFeedback decision places feedback after it.
			state.messages = append(state.messages, ChatMessage{
				Role:    "assistant",
				Content: content,
			})
			state.messageRuneCount += utf8.RuneCountInString(content)

			snap := &IterationSnapshot{
				Response:  &resp,
				ToolCalls: nil,
			}
			decision, hookErr := cfg.onIterationComplete(iterCtx, i, snap)
			if hookErr != nil {
				endIter(FinishError)
				errResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishError}
				finalizeRun(ctx, ch, state, cfg.name, FinishError, errResult)
				return iterationResult{outcome: iterDone, final: errResult, err: fmt.Errorf("OnIterationComplete: %w", hookErr)}
			}
			if decision.action == decisionStop {
				endIter(FinishStop)
				r := decision.result
				r.FinishReason = FinishStop
				r.Warnings = state.lastWarnings
				r.ProviderMeta = state.lastProviderMeta
				r.Files = state.files
				r.Iterations = state.iterations
				r.Sources = state.sources
				finalizeRun(ctx, ch, state, cfg.name, FinishStop, r)
				return iterationResult{outcome: iterDone, final: r}
			}
			if decision.action == decisionInject {
				// Inject feedback messages and re-run so the LLM can address the
				// feedback. The assistant message was already appended above.
				for _, m := range decision.msgs {
					state.messages = append(state.messages, m)
					state.messageRuneCount += utf8.RuneCountInString(m.Content)
				}
				endIter(FinishStop)
				return iterationResult{outcome: iterContinue}
			}
			// decisionContinue: fall through to natural iterDone.
		}

		endIter(FinishStop)
		cfg.mem.PersistMessages(iterCtx, cfg.name, task, task.Input, content, state.steps)
		result := AgentResult{
			Output:       content,
			Thinking:     state.lastThinking,
			Attachments:  mergeAttachments(state.accumulatedAttachments, resp.Attachments),
			Usage:        state.totalUsage,
			Steps:        state.steps,
			FinishReason: FinishStop,
			Warnings:     state.lastWarnings,
			ProviderMeta: state.lastProviderMeta,
			Files:        state.files,
			Iterations:   state.iterations,
			Sources:      state.sources,
		}
		emitObjectFinish(ctx, ch, cfg.responseSchema, content, &result)
		finalizeRun(ctx, ch, state, cfg.name, FinishStop, result)
		return iterationResult{
			outcome: iterDone,
			final:   result,
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
	// Why: tool implementations may emit EventFileAttachment during dispatch.
	// newFileCapturingSink intercepts those events into state.files while
	// forwarding everything else to ch. Returns nil/no-op when ch is nil.
	fileSinkCh, waitFileSink := newFileCapturingSink(ctx, ch, state)
	iterCtx = contextWithStreamSink(iterCtx, fileSinkCh)
	dispatchStart := time.Now()
	results := dispatchParallel(iterCtx, resp.ToolCalls, cfg.dispatch, cfg.maxParallelDispatch)
	cfg.logger.Debug("tool dispatch completed", "agent", cfg.name, "iteration", i, "duration", time.Since(dispatchStart))
	// Close the capturing sink (only when non-nil) and wait for the forwarder to drain.
	if fileSinkCh != nil {
		close(fileSinkCh)
	}
	waitFileSink()

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
		state.steps = appendStepBounded(state.steps, trace, cfg.maxSteps)

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

		result := ToolResult{Content: json.RawMessage(results[j].content)}
		if err := cfg.processors.RunPostTool(iterCtx, tc, &result); err != nil {
			if s := checkSuspendLoop(err, cfg, state.messages, task); s != nil {
				if ch != nil {
					select {
					case ch <- StreamEvent{
						Type:           EventToolCallSuspended,
						ID:             tc.ID,
						Name:           tc.Name,
						Args:           tc.Args,
						Protocol:       s.tag,
						SuspendPayload: s.Payload,
					}:
					case <-ctx.Done():
					}
				}
				endIter(FinishSuspended)
				suspResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishSuspended, SuspendPayload: s.Payload, SuspendProtocol: s.tag, Iterations: state.iterations}
				finalizeRun(ctx, ch, state, cfg.name, FinishSuspended, suspResult)
				return iterationResult{outcome: iterDone, final: suspResult, err: s}
			}
			res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
			reason := FinishError
			if res.Output != "" {
				reason = FinishHalted
			}
			res.FinishReason = reason
			endIter(reason)
			finalizeRun(ctx, ch, state, cfg.name, reason, res)
			return iterationResult{outcome: iterDone, final: res, err: retErr}
		}
		// Truncate large tool results before appending to message history.
		// Stream events and step traces retain full content (transient).
		content := string(result.Content) // boundary conversion for rune ops
		maxLen := cfg.maxToolResultLen
		if maxLen == 0 {
			maxLen = maxToolResultMessageLen
		}
		msgContent := content
		if utf8.RuneCountInString(content) > maxLen {
			inline := TruncateStr(content, maxLen)
			total := utf8.RuneCountInString(content)
			if cfg.toolResultStore != nil {
				id, putErr := cfg.toolResultStore.Put(iterCtx, result.Content) // bytes in — zero copy
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
			state.lastAgentOutput = string(result.Content)
		}

		// Collect citations from tools that implement core.Sourced.
		// Skipped on error results since a failed call has no authoritative sources.
		if !results[j].isError && cfg.lookupTool != nil {
			if t, ok := cfg.lookupTool(tc.Name); ok {
				if sourced, ok := t.(core.Sourced); ok {
					state.sources = append(state.sources, sourced.Sources()...)
				}
			}
		}
	}
	// Compress context if over budget (within the iteration span so compression
	// traces are children of the iteration that triggered them).
	if state.compressThreshold > 0 && state.messageRuneCount > state.compressThreshold {
		cfg.logger.Info("context compression triggered", "agent", cfg.name, "iteration", i, "runes", state.messageRuneCount, "threshold", state.compressThreshold)
		state.messages, state.messageRuneCount = compressMessages(iterCtx, cfg, task, state.messages, 2, state.messageRuneCount)
	}

	// OnIterationComplete hook (tool-call path): fires after all tool results
	// have been processed and history has been updated.
	if cfg.onIterationComplete != nil {
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
		decision, hookErr := cfg.onIterationComplete(iterCtx, i, snap)
		if hookErr != nil {
			endIter(FinishError)
			errResult := AgentResult{Usage: state.totalUsage, Steps: state.steps, FinishReason: FinishError}
			finalizeRun(ctx, ch, state, cfg.name, FinishError, errResult)
			return iterationResult{outcome: iterDone, final: errResult, err: fmt.Errorf("OnIterationComplete: %w", hookErr)}
		}
		switch decision.action {
		case decisionStop:
			endIter(FinishStop)
			r := decision.result
			r.FinishReason = FinishStop
			r.Warnings = state.lastWarnings
			r.ProviderMeta = state.lastProviderMeta
			r.Files = state.files
			r.Iterations = state.iterations
			r.Sources = state.sources
			finalizeRun(ctx, ch, state, cfg.name, FinishStop, r)
			return iterationResult{outcome: iterDone, final: r}
		case decisionInject:
			for _, m := range decision.msgs {
				state.messages = append(state.messages, m)
				state.messageRuneCount += utf8.RuneCountInString(m.Content)
			}
		// decisionContinue: fall through to normal continuation
		}
	}

	endIter(FinishToolCalls)
	return iterationResult{outcome: iterContinue}
}

// captureProviderMeta appends resp.Warnings to state.lastWarnings and
// updates state.lastProviderMeta with resp.ProviderMeta when non-empty.
// Call after every successful provider call so the final AgentResult carries
// the accumulated warnings and the last-seen provider metadata.
func captureProviderMeta(state *loopState, resp *ChatResponse) {
	if len(resp.Warnings) > 0 {
		state.lastWarnings = append(state.lastWarnings, resp.Warnings...)
	}
	if len(resp.ProviderMeta) > 0 {
		state.lastProviderMeta = resp.ProviderMeta
	}
}

// handleOnError consults cfg.onError when a non-graceful LLM error occurs.
// It skips the hook for graceful exits: context cancellation, *ErrSuspended,
// and errors wrapping *errSuspend (processor-level suspend signals).
//
// Returns (result, true) when the hook handled the error (retry, halt, or
// hook-level error); returns (zero, false) when the caller should propagate
// the original error as-is.
func handleOnError(ctx context.Context, cfg LoopConfig, state *loopState, i int, err error) (iterationResult, bool) {
	// Grace exits bypass the hook.
	if ctx.Err() != nil {
		return iterationResult{}, false
	}
	var suspended *ErrSuspended
	if errors.As(err, &suspended) {
		return iterationResult{}, false
	}

	if cfg.onError == nil {
		return iterationResult{}, false
	}

	decision, hookErr := cfg.onError(ctx, i, err)
	if hookErr != nil {
		return iterationResult{
			outcome: iterDone,
			final:   AgentResult{Usage: state.totalUsage, Steps: state.steps},
			err:     fmt.Errorf("OnError: %w", hookErr),
		}, true
	}

	switch decision.action {
	case errRetry:
		if decision.feedback != "" {
			msg := ChatMessage{Role: core.RoleUser, Content: decision.feedback}
			state.messages = append(state.messages, msg)
			state.messageRuneCount += len([]rune(decision.feedback))
		}
		return iterationResult{outcome: iterContinue}, true
	case errHalt:
		return iterationResult{outcome: iterDone, final: decision.result}, true
	default: // errPropagate
		return iterationResult{}, false
	}
}
