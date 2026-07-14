package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nevindra/oasis/core"
)

// loopState holds the mutable per-execution state shared across runIteration
// calls. Pooled via loopStatePool to amortize allocation across Execute calls.
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

	// closeOnce + closeCh replace the heap-allocated onceClose closure.
	closeOnce sync.Once
	closeCh   chan<- core.StreamEvent

	lastWarnings     []string
	lastProviderMeta json.RawMessage

	files []core.Attachment

	iterations []core.IterationTrace

	sources []core.Source
}

var loopStatePool = sync.Pool{New: func() any { return new(loopState) }}

func acquireLoopState(messages []core.ChatMessage, messageRuneCount int, attachByteBudget int64, hasAgentTools bool, compressThreshold int, ch chan<- core.StreamEvent) *loopState {
	s := loopStatePool.Get().(*loopState)
	s.messages = messages
	s.messageRuneCount = messageRuneCount
	s.attachByteBudget = attachByteBudget
	s.hasAgentTools = hasAgentTools
	s.compressThreshold = compressThreshold
	s.closeCh = ch
	return s
}

func releaseLoopState(s *loopState) {
	s.messages = nil
	s.totalUsage = core.Usage{}
	s.lastAgentOutput = ""
	s.lastThinking = ""
	s.accumulatedAttachments = s.accumulatedAttachments[:0]
	s.accumulatedAttachmentBytes = 0
	s.attachByteBudget = 0
	s.hasAgentTools = false
	s.compressThreshold = 0
	s.closeOnce = sync.Once{}
	s.closeCh = nil
	s.lastProviderMeta = nil
	s.messageRuneCount = 0

	// Why: steps, lastWarnings, files, iterations and sources are assigned
	// directly into the returned AgentResult by patchTerminal (no copy). If we
	// truncated to [:0] and kept the backing array in the pool, the next Execute
	// in this process would append into those same arrays and silently corrupt
	// the previously returned result. Nil-ing transfers ownership of the escaped
	// backing arrays to the AgentResult: the pool retains nothing, and the next
	// run's appends (appendStepBounded and plain append both handle nil) allocate
	// fresh arrays — the natural cost of result-owned memory, no memcpy.
	//
	// The result fields that are empty on a given run (e.g. steps/files/sources
	// on a no-tool turn) stay nil and cost nothing. iterations is the one
	// exception: endIteration always appends at least one trace per LLM call, so
	// even a no-tool turn now allocates one small iterations slice that the
	// AgentResult owns (+1 alloc, ~176 B on SingleTurn). That allocation is
	// load-bearing — AgentResult.Iterations must point at memory the pool can
	// never reuse — and is the intended price of the fix.
	s.steps = nil
	s.lastWarnings = nil
	s.files = nil
	s.iterations = nil
	s.sources = nil

	loopStatePool.Put(s)
}

func (s *loopState) safeClose() {
	if s.closeCh != nil {
		s.closeOnce.Do(func() { close(s.closeCh) })
	}
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
func runIteration(ctx context.Context, cfg *LoopConfig, task AgentTask, ch chan<- core.StreamEvent, state *loopState, i int) iterationResult {
	if cfg.Logger.Enabled(ctx, slog.LevelDebug) {
		cfg.Logger.Debug("loop iteration started", "agent", cfg.Name, "iteration", i,
			"tools", len(cfg.Tools), "messages", len(state.messages), "runes", state.messageRuneCount)
	}

	// Emit iteration-start event.
	if ch != nil {
		select {
		case ch <- core.StreamEvent{
			Type: core.EventIterationStart,
			Name: smallItoa(i),
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

	// ep bundles iteration-end context as explicit parameters so endIteration
	// can be called directly without forming a heap-escaping closure.
	// The three mutable fields (llmCalled, llmModel, llmTrace) are written
	// directly on ep throughout the function — no separate locals needed.
	ep := iterEndParams{
		iterStart: iterStart,
		i:         i,
		state:     state,
		iterSpan:  iterSpan,
		ch:        ch,
		ctx:       ctx,
		llmModel:  cfg.Provider.Name(),
	}

	req := core.ChatRequest{Messages: state.messages, ResponseSchema: cfg.ResponseSchema, GenerationParams: cfg.GenParams}

	// PreProcessor hook.
	if err := cfg.Processors.RunPreLLM(iterCtx, &req); err != nil {
		if cfg.Logger.Enabled(iterCtx, slog.LevelError) {
			cfg.Logger.Error("pre-processor failed", "agent", cfg.Name, "iteration", i, "error", err)
		}
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
			endIteration(ep, core.FinishSuspended)
			return terminateIteration(ctx, cfg, ch, state, core.FinishSuspended, AgentResult{SuspendPayload: s.Payload, SuspendProtocol: s.tag}, s)
		}
		res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
		reason := core.FinishError
		if res.Output != "" {
			reason = core.FinishHalted
		}
		endIteration(ep, reason)
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
			if cfg.Logger.Enabled(iterCtx, slog.LevelError) {
				cfg.Logger.Error("PrepareStep hook failed", "agent", cfg.Name, "iteration", i, "error", err)
			}
			endIteration(ep, core.FinishError)
			return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, fmt.Errorf("PrepareStep: %w", err))
		}
		if ctrl.Model != nil {
			iterProvider = ctrl.Model
			ep.llmModel = iterProvider.Name()
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

	useStream := ch != nil && (len(req.Tools) == 0 || !state.hasAgentTools)
	passCh := ch
	if len(req.Tools) > 0 && !useStream {
		passCh = nil
	}
	if cfg.Logger.Enabled(ctx, slog.LevelDebug) {
		cfg.Logger.Debug("calling LLM", "agent", cfg.Name, "iteration", i, "streaming", useStream, "tool_count", len(req.Tools))
	}
	resp, ep.llmTrace, _, err = callLLM(ctx, iterCtx, cfg, req, iterProvider, passCh, state, ep.llmModel, useStream)
	ep.llmCalled = true
	streamedThisIter = useStream

	if err != nil {
		if cfg.Logger.Enabled(ctx, slog.LevelError) {
			cfg.Logger.Error("LLM call failed", "agent", cfg.Name, "iteration", i, "error", err, "duration", ep.llmTrace.Duration)
		}
		endIteration(ep, core.FinishError)
		if r, handled := handleOnError(iterCtx, cfg, state, i, err); handled {
			if r.outcome == iterDone {
				finalizeRun(ctx, ch, state, cfg.Name, core.FinishError, r.final)
			}
			return r
		}
		return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, err)
	}
	if cfg.Logger.Enabled(ctx, slog.LevelDebug) {
		cfg.Logger.Debug("LLM call completed", "agent", cfg.Name, "iteration", i,
			"duration", ep.llmTrace.Duration,
			"input_tokens", resp.Usage.InputTokens,
			"output_tokens", resp.Usage.OutputTokens,
			"tool_calls", len(resp.ToolCalls))
	}
	state.totalUsage.InputTokens += resp.Usage.InputTokens
	state.totalUsage.OutputTokens += resp.Usage.OutputTokens
	core.AddRunUsage(iterCtx, ep.llmModel, resp.Usage)

	captureProviderMeta(state, &resp)

	// PostProcessor hook.
	if r, handled := runPostLLMOrHandle(ctx, iterCtx, cfg, task, ch, state, &resp, ep); handled {
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
		if cfg.Logger.Enabled(ctx, slog.LevelDebug) {
			cfg.Logger.Debug("final response (no tool calls)", "agent", cfg.Name, "iteration", i)
		}
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
			if state.compressThreshold > 0 {
				state.messageRuneCount += utf8.RuneCountInString(content)
			}

			snap := &IterationSnapshot{
				Response:  &resp,
				ToolCalls: nil,
			}
			decision, hookErr := cfg.OnIterationComplete(iterCtx, i, snap)
			if hookErr != nil {
				endIteration(ep, core.FinishError)
				return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, fmt.Errorf("OnIterationComplete: %w", hookErr))
			}
			if decision.IsStop() {
				return finalizeIterationStop(ctx, cfg, task, ch, state, decision, ep)
			}
			if decision.IsInject() {
				for _, m := range decision.Msgs() {
					state.messages = append(state.messages, m)
					if state.compressThreshold > 0 {
						state.messageRuneCount += utf8.RuneCountInString(m.Content)
					}
				}
				endIteration(ep, core.FinishStop)
				return iterationResult{outcome: iterContinue}
			}
			// Continue: fall through to natural iterDone.
		}

		endIteration(ep, core.FinishStop)
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
	if state.compressThreshold > 0 {
		state.messageRuneCount += utf8.RuneCountInString(resp.Content)
	}

	transformsActive := len(cfg.ToolTransforms) > 0 || len(cfg.ToolTransformMatchers) > 0

	// Emit tool-call-start events before dispatch.
	if ch != nil {
		for _, tc := range resp.ToolCalls {
			args := tc.Args
			if transformsActive {
				if tt, ok := cfg.ResolveToolTransform(tc.Name); ok {
					args = applyArgsTransform(tt.Display, tc.Name, args, cfg.Logger)
				}
			}
			select {
			case ch <- core.StreamEvent{Type: core.EventToolCallStart, ID: tc.ID, Name: tc.Name, Args: args}:
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
	if cfg.Logger.Enabled(ctx, slog.LevelInfo) {
		cfg.Logger.Info("dispatching tool calls", "agent", cfg.Name, "iteration", i, "tools", toolCallNames(resp.ToolCalls))
	}
	fileSinkCh, waitFileSink := newFileCapturingSink(ctx, ch, state)
	iterCtx = contextWithStreamSink(iterCtx, fileSinkCh)
	dispatchStart := time.Now()
	results := dispatchParallel(iterCtx, resp.ToolCalls, cfg.Dispatch, cfg.MaxParallelDispatch)
	if cfg.Logger.Enabled(ctx, slog.LevelDebug) {
		cfg.Logger.Debug("tool dispatch completed", "agent", cfg.Name, "iteration", i, "duration", time.Since(dispatchStart))
	}
	if fileSinkCh != nil {
		close(fileSinkCh)
	}
	waitFileSink()

	// postProcessed holds the post-tool-processor-mutated result for each call.
	// Only allocated when OnIterationComplete is set (the sole consumer).
	var postProcessed []core.ToolResult
	if cfg.OnIterationComplete != nil {
		postProcessed = make([]core.ToolResult, len(resp.ToolCalls))
	}

	// firstTrace holds the first step trace built in THIS iteration, for the
	// OnIterationComplete snapshot.
	// Why: state.steps is a ring buffer (appendStepBounded). Once it reaches
	// MaxStepsResolved, appends overwrite the oldest slot and len() stops
	// growing — so back-indexing as state.steps[len-len(ToolCalls)] either
	// panics (when an iteration emits more parallel calls than MaxSteps) or
	// points at a trace evicted from a prior iteration. Capturing the first
	// trace forward as it is built sidesteps the eviction hazard entirely.
	var firstTrace StepTrace
	haveFirstTrace := false

	// Process results sequentially.
	for j, tc := range resp.ToolCalls {
		state.totalUsage.InputTokens += results[j].usage.InputTokens
		state.totalUsage.OutputTokens += results[j].usage.OutputTokens

		var tt core.ToolTransform
		var hasTransform bool
		if transformsActive {
			tt, hasTransform = cfg.ResolveToolTransform(tc.Name)
		}

		if results[j].isError {
			if cfg.Logger.Enabled(ctx, slog.LevelWarn) {
				cfg.Logger.Warn("tool call returned error", "agent", cfg.Name, "tool", tc.Name, "error", results[j].content, "duration", results[j].duration)
			}
		} else {
			if cfg.Logger.Enabled(ctx, slog.LevelDebug) {
				cfg.Logger.Debug("tool call result", "agent", cfg.Name, "tool", tc.Name, "duration", results[j].duration, "result_len", len(results[j].content))
			}
		}

		// Compute the Display-transformed result once; reuse for both the
		// tool-call-result and ui-component events.
		displayContent := results[j].content
		displayUI := results[j].ui
		if ch != nil && hasTransform && tt.Display != nil && tt.Display.Result != nil {
			dr := applyResultTransform(tt.Display, tc.Name,
				core.ToolResult{Content: results[j].content, UI: results[j].ui}, true, cfg.Logger)
			displayContent = dr.Content
			displayUI = dr.UI
		}

		// Emit tool-call-result event.
		if ch != nil {
			select {
			case ch <- core.StreamEvent{
				Type:     core.EventToolCallResult,
				ID:       tc.ID,
				Name:     tc.Name,
				Content:  displayContent,
				Usage:    results[j].usage,
				Duration: results[j].duration,
			}:
			case <-ctx.Done():
			}
		}

		// Emit ui-component event when the tool produced a renderable component.
		if ch != nil && displayUI != nil {
			select {
			case ch <- core.StreamEvent{
				Type:   core.EventUIComponent,
				ID:     tc.ID,
				Name:   displayUI.Name,
				Object: displayUI.Props,
			}:
			case <-ctx.Done():
			}
		}

		// Build step trace. Apply the Transcript transform to the content/args
		// the trace records. At this layer a tool error has already been folded
		// into results[j].content (with results[j].isError set) by the dispatch
		// layer — there is no separate Error payload here, so the transform
		// operates on content. buildStepTrace truncates content into Output and
		// keeps it raw in RawOutput, so the transcript-transformed content
		// redacts both. transcriptContent is reused for the result store below.
		transcriptContent := results[j].content
		transcriptCall := tc
		if hasTransform && tt.Transcript != nil {
			if tt.Transcript.Result != nil {
				tr := applyResultTransform(tt.Transcript, tc.Name,
					core.ToolResult{Content: results[j].content}, true, cfg.Logger)
				transcriptContent = tr.Content
			}
			if tt.Transcript.Args != nil {
				transcriptCall.Args = applyArgsTransform(tt.Transcript, tc.Name, tc.Args, cfg.Logger)
			}
		}
		traceRes := results[j]
		traceRes.content = transcriptContent
		trace := buildStepTrace(transcriptCall, traceRes)
		state.steps = appendStepBounded(state.steps, trace, cfg.MaxStepsResolved)
		if !haveFirstTrace {
			firstTrace = trace
			haveFirstTrace = true
		}

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

		result := core.ToolResult{Content: results[j].content}
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
				endIteration(ep, core.FinishSuspended)
				return terminateIteration(ctx, cfg, ch, state, core.FinishSuspended, AgentResult{SuspendPayload: s.Payload, SuspendProtocol: s.tag}, s)
			}
			res, retErr := handleProcessorErrorWithSteps(err, state.totalUsage, state.steps)
			reason := core.FinishError
			if res.Output != "" {
				reason = core.FinishHalted
			}
			endIteration(ep, reason)
			return terminateIteration(ctx, cfg, ch, state, reason, res, retErr)
		}
		if postProcessed != nil {
			postProcessed[j] = result
		}

		// Apply the Model transform to what the LLM sees. Runs AFTER PostTool
		// (so the Model transform observes the post-processor result) and BEFORE
		// chunking (so chunk boundaries apply to the final model payload).
		// Fail-open: a panicking Model transform leaves result unchanged so the
		// agent loop stays functional.
		if hasTransform && tt.Model != nil && tt.Model.Result != nil {
			result = applyResultTransform(tt.Model, tc.Name, result, false /*fail open*/, cfg.Logger)
		}

		// Chunk large tool results transparently.
		// Why: instead of hinting the LLM to call read_full_result, split the
		// content into multiple sequential tool-result messages (all sharing the
		// same call ID). The LLM sees them as one logical result without needing
		// to issue a follow-up tool call. ToolResultStore still receives the full
		// payload for post-hoc inspection.
		content := result.Content
		maxLen := cfg.MaxToolResultLen
		if maxLen == 0 {
			maxLen = maxToolResultMessageLen
		}
		if cfg.ToolResultStore != nil {
			storeContent := result.Content
			if hasTransform && tt.Transcript != nil && tt.Transcript.Result != nil {
				storeContent = transcriptContent
			}
			if _, putErr := cfg.ToolResultStore.Put(iterCtx, storeContent); putErr != nil {
				if cfg.Logger.Enabled(iterCtx, slog.LevelWarn) {
					cfg.Logger.Warn("tool result store put failed", "agent", cfg.Name, "error", putErr)
				}
			}
		}
		// Why byte length, not RuneCountInString: byte count is an upper
		// bound on rune count, so len <= maxLen guarantees no split is
		// needed without scanning the payload (O(1) vs O(n)). Multibyte
		// payloads in the (maxLen runes, maxLen bytes] band split one
		// message earlier than a rune-exact check would — an extra
		// same-call-ID chunk the LLM still sees as one logical result.
		if len(content) > maxLen {
			for _, chunk := range splitContentRunes(content, maxLen) {
				state.messages = append(state.messages, core.ToolResultMessage(tc.ID, chunk))
				if state.compressThreshold > 0 {
					state.messageRuneCount += utf8.RuneCountInString(chunk)
				}
			}
		} else {
			state.messages = append(state.messages, core.ToolResultMessage(tc.ID, content))
			if state.compressThreshold > 0 {
				state.messageRuneCount += utf8.RuneCountInString(content)
			}
		}

		if strings.HasPrefix(tc.Name, core.ToolPrefixAgent) {
			state.lastAgentOutput = content
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
		if cfg.Logger.Enabled(ctx, slog.LevelInfo) {
			cfg.Logger.Info("context compression triggered", "agent", cfg.Name, "iteration", i, "runes", state.messageRuneCount, "threshold", state.compressThreshold)
		}
		state.messages, state.messageRuneCount = compressMessages(iterCtx, cfg, task, state.messages, 2, state.messageRuneCount)
	}

	// OnIterationComplete hook (tool-call path).
	if cfg.OnIterationComplete != nil {
		snap := &IterationSnapshot{
			Response:    &resp,
			ToolCalls:   resp.ToolCalls,
			ToolResults: postProcessed,
			Trace:       firstTrace,
		}
		decision, hookErr := cfg.OnIterationComplete(iterCtx, i, snap)
		if hookErr != nil {
			endIteration(ep, core.FinishError)
			return terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, fmt.Errorf("OnIterationComplete: %w", hookErr))
		}
		if decision.IsStop() {
			return finalizeIterationStop(ctx, cfg, task, ch, state, decision, ep)
		}
		if decision.IsInject() {
			for _, m := range decision.Msgs() {
				state.messages = append(state.messages, m)
				if state.compressThreshold > 0 {
					state.messageRuneCount += utf8.RuneCountInString(m.Content)
				}
			}
		}
		// Continue: fall through to normal continuation
	}

	endIteration(ep, core.FinishToolCalls)
	return iterationResult{outcome: iterContinue}
}

// callLLM dispatches one LLM call (streaming or non-streaming).
func callLLM(fwdCtx, spanCtx context.Context, cfg *LoopConfig, req core.ChatRequest, provider core.Provider, ch chan<- core.StreamEvent, state *loopState, llmModel string, useStream bool) (core.ChatResponse, core.LLMCallTrace, bool, error) {
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
		iterCh, wait := newObjectStreamForwarder(fwdCtx, ch, defaultIterChBufSize, state, cfg.ResponseSchema, cfg.Processors)
		resp, err = provider.ChatStream(llmCtx, req, iterCh)
		endLLMSpan()
		wait()
		streamed = true
	} else {
		resp, err = provider.ChatStream(llmCtx, req, nil)
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
	cfg *LoopConfig,
	task AgentTask,
	ch chan<- core.StreamEvent,
	state *loopState,
	resp *core.ChatResponse,
	ep iterEndParams,
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
			endIteration(ep, core.FinishSuspended)
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
		endIteration(ep, reason)
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
func handleOnError(ctx context.Context, cfg *LoopConfig, state *loopState, i int, err error) (iterationResult, bool) {
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
			if state.compressThreshold > 0 {
				state.messageRuneCount += utf8.RuneCountInString(fb)
			}
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
func terminateIteration(ctx context.Context, cfg *LoopConfig, ch chan<- core.StreamEvent, state *loopState, reason core.FinishReason, extra AgentResult, err error) iterationResult {
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
func finalizeIterationStop(ctx context.Context, cfg *LoopConfig, task AgentTask, ch chan<- core.StreamEvent, state *loopState, decision IterationDecision, ep iterEndParams) iterationResult {
	endIteration(ep, core.FinishStop)
	r := decision.Result()
	// A hook-forced stop ends the turn as authoritatively as a natural final
	// response — persist it like the natural-stop path does, or the exchange
	// never reaches the thread store and the next Execute on this ThreadID
	// starts with a hole in its history.
	cfg.Mem.PersistTurn(ctx, cfg.Name, task, task.Input, r.Output, state.steps)
	state.patchTerminal(&r, core.FinishStop)
	finalizeRun(ctx, ch, state, cfg.Name, core.FinishStop, r)
	return iterationResult{outcome: iterDone, final: r}
}

// splitContentRunes splits s into chunks of at most maxRunes runes each.
// Splitting is rune-safe: chunks never break in the middle of a multi-byte
// UTF-8 sequence. If s fits within maxRunes, a single-element slice is
// returned. maxRunes must be > 0.
//
// Why byte-offset chunking: a chunk of at most maxRunes BYTES can never hold
// more than maxRunes runes, so cutting at byte maxRunes and backing off to
// the nearest rune start satisfies the contract in O(chunks) — no per-rune
// decode of the payload. Multi-byte content yields slightly more chunks than
// a rune-exact split; the contract is only an upper bound per chunk.
func splitContentRunes(s string, maxRunes int) []string {
	if len(s) <= maxRunes {
		return []string{s}
	}
	chunks := make([]string, 0, len(s)/maxRunes+1)
	for len(s) > maxRunes {
		cut := maxRunes
		// A valid UTF-8 rune start is at most 3 bytes back from any offset.
		for back := 0; back < 3 && cut > 0 && !utf8.RuneStart(s[cut]); back++ {
			cut--
		}
		if !utf8.RuneStart(s[cut]) {
			// Invalid UTF-8: no boundary within reach, nothing to preserve.
			cut = maxRunes
		}
		if cut == 0 {
			// maxRunes < 4 with a wider rune up front: emit the whole rune
			// to guarantee progress (still within bound — the chunk is 1 rune).
			_, size := utf8.DecodeRuneInString(s)
			cut = size
		}
		chunks = append(chunks, s[:cut])
		s = s[cut:]
	}
	return append(chunks, s)
}

// iterEndParams bundles the inputs to endIteration so helpers like
// runPostLLMOrHandle and finalizeIterationStop can call it without holding a
// heap-allocated closure over ~9 variables.
type iterEndParams struct {
	iterStart time.Time
	llmTrace  core.LLMCallTrace
	state     *loopState
	iterSpan  core.Span
	ch        chan<- core.StreamEvent
	ctx       context.Context
	llmModel  string
	i         int
	llmCalled bool
}

// endIteration finalizes one agent loop iteration: records the IterationTrace
// when an LLM was called, ends the tracing span, and emits EventIterationFinish
// on the stream channel.
func endIteration(ep iterEndParams, reason core.FinishReason) {
	dur := time.Since(ep.iterStart)
	if ep.llmCalled {
		trace := core.IterationTrace{
			Iter:         ep.i,
			Model:        ep.llmModel,
			StartedAt:    ep.iterStart,
			Duration:     dur,
			LLMCall:      ep.llmTrace,
			FinishReason: reason,
			Usage: core.Usage{
				InputTokens:  ep.llmTrace.InputTokens,
				OutputTokens: ep.llmTrace.OutputTokens,
			},
		}
		ep.state.iterations = append(ep.state.iterations, trace)
	}

	if ep.iterSpan != nil {
		ep.iterSpan.End()
	}
	if ep.ch != nil {
		select {
		case ep.ch <- core.StreamEvent{
			Type:         core.EventIterationFinish,
			Name:         smallItoa(ep.i),
			Duration:     dur,
			FinishReason: reason,
		}:
		case <-ep.ctx.Done():
		}
	}
}

// smallInts holds pre-computed string representations of iteration indices.
// Covers virtually all real agent runs (max 32 iterations).
var smallInts [32]string

func init() {
	for i := range smallInts {
		smallInts[i] = strconv.Itoa(i)
	}
}

// smallItoa returns a pre-interned string for i < 32, falling back to
// strconv.Itoa for larger values.
func smallItoa(i int) string {
	if i >= 0 && i < len(smallInts) {
		return smallInts[i]
	}
	return strconv.Itoa(i)
}

// toolCallNames is a slog.LogValuer that lazily formats tool call names
// without allocating a []string slice.
type toolCallNames []core.ToolCall

func (t toolCallNames) LogValue() slog.Value {
	attrs := make([]slog.Attr, len(t))
	for i, tc := range t {
		attrs[i] = slog.String(smallItoa(i), tc.Name)
	}
	return slog.GroupValue(attrs...)
}
