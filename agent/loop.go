package agent

import (
	"context"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nevindra/oasis/core"
)

// LoopConfig is defined in internal/runtime and re-exported as a type alias
// in agent/agent.go. It holds everything runLoop needs to execute.

// onceClose returns a function that closes ch exactly once (idempotent).
// The returned func is safe to call multiple times; only the first call closes
// the channel. Used to guard streaming channels that may receive a close from
// both a deferred finalizeRun call and an early-exit path.
func onceClose[T any](ch chan<- T) func() {
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// maxToolResultMessageLen is the maximum rune length for a tool result stored
// in the conversation message history during the tool-calling loop.
const maxToolResultMessageLen = 100_000 // ~25K tokens

// maxAccumulatedAttachments caps the number of attachments collected from
// tool/agent results during the execution loop.
const maxAccumulatedAttachments = 50

// maxAccumulatedAttachmentBytes is the default size budget (bytes) for
// attachments collected from tool/agent results during the execution loop.
const maxAccumulatedAttachmentBytes int64 = 50 * 1024 * 1024 // 50 MB

// RunLoop is the exported alias for runLoop, used by the network package
// (which cannot call unexported functions) as the runLoopFn callback to
// Runtime.ExecuteWithSpan.
var RunLoop = runLoop

// runLoop is the shared tool-calling orchestrator used by both LLMAgent and
// Network. When ch is nil, it operates in blocking mode (Execute). When ch is
// non-nil, it emits StreamEvent values and closes ch when done (ExecuteStream).
//
// Iteration body lives in runIteration (iteration.go); the post-loop
// forced-synthesis tail lives in forceSynthesis below.
func runLoop(ctx context.Context, cfg *LoopConfig, task AgentTask, ch chan<- core.StreamEvent) (AgentResult, error) {
	if cfg.Logger == nil {
		cfg.Logger = nopLogger
	}

	// safeClose is called via state.safeClose() — no heap closure needed.

	// Inject InputHandler into context for processors.
	if cfg.InputHandler != nil {
		ctx = WithInputHandlerContext(ctx, cfg.InputHandler)
	}

	// Build initial messages (system prompt + user memory + history + user input).
	// If ResumeMessages is set (suspend/resume), use those instead.
	var messages []core.ChatMessage
	if len(cfg.ResumeMessages) > 0 {
		messages = cfg.ResumeMessages
	} else {
		initial := cfg.Mem.BuildMessages(ctx, cfg.Name, cfg.SystemPrompt, task)
		// Why a small constant headroom instead of sizing for MaxIter: a
		// typical run makes 0–3 tool calls, while MaxIter-proportional
		// capacity (~100 slots ≈ 14KB at the default MaxIter=25) taxed every
		// Execute on every agent that merely had tools registered. 8 slots
		// absorb the common case (per iteration: one assistant tool-call
		// message + one result message per call); deep loops grow by
		// amortized append doubling, which costs less than the flat tax did.
		preAllocCap := 2
		if len(cfg.Tools) > 0 {
			preAllocCap = 8
		}
		messages = make([]core.ChatMessage, len(initial), len(initial)+preAllocCap)
		copy(messages, initial)
	}

	// Attachment byte budget (0/negative → default 50MB).
	attachByteBudget := cfg.MaxAttachmentBytes
	if attachByteBudget <= 0 {
		attachByteBudget = maxAccumulatedAttachmentBytes
	}

	// Track initial message rune count for compression decisions.
	// Skip when compression is disabled (CompressThreshold == 0) to avoid O(n) scan.
	var messageRuneCount int
	if cfg.CompressThreshold > 0 {
		for _, m := range messages {
			messageRuneCount += utf8.RuneCountInString(m.Content)
		}
	}

	// Detect whether the tool set includes agent_* delegation tools (Network).
	hasAgentTools := false
	for _, t := range cfg.Tools {
		if strings.HasPrefix(t.Name, core.ToolPrefixAgent) {
			hasAgentTools = true
			break
		}
	}

	state := acquireLoopState(messages, messageRuneCount, attachByteBudget, hasAgentTools, cfg.CompressThreshold, ch)
	defer releaseLoopState(state)

	for i := 0; i < cfg.MaxIter; i++ {
		result := runIteration(ctx, cfg, task, ch, state, i)
		if result.outcome == iterDone {
			return result.final, result.err
		}
	}

	return forceSynthesis(ctx, cfg, task, ch, state)
}

// finalizeRun emits EventRunFinish and closes the streaming channel.
func finalizeRun(ctx context.Context, ch chan<- core.StreamEvent, state *loopState, name string, reason core.FinishReason, result AgentResult) {
	if ch != nil {
		ev := core.StreamEvent{
			Type:         core.EventRunFinish,
			Name:         name,
			Content:      result.Output,
			Usage:        result.Usage,
			FinishReason: reason,
			Warnings:     result.Warnings,
			ProviderMeta: result.ProviderMeta,
		}
		if reason == core.FinishSuspended {
			ev.Content = string(result.SuspendPayload)
			ev.Protocol = result.SuspendProtocol
			ev.SuspendPayload = result.SuspendPayload
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			// Best-effort: still close.
		}
	}
	state.safeClose()
}

// forceSynthesis runs the post-loop forced-synthesis tail when runLoop hits
// cfg.MaxIter without a natural termination.
func forceSynthesis(ctx context.Context, cfg *LoopConfig, task AgentTask, ch chan<- core.StreamEvent, state *loopState) (AgentResult, error) {
	cfg.Logger.Warn("max iterations reached, forcing synthesis", "agent", cfg.Name, "iteration", cfg.MaxIter)
	state.messages = append(state.messages, core.UserMessage(
		"You have used all available tool calls. Summarize what you found and respond to the user."))

	// Synthesis span so the forced-response LLM call is visible in traces.
	synthCtx := ctx
	if cfg.Tracer != nil {
		var synthSpan core.Span
		synthCtx, synthSpan = cfg.Tracer.Start(ctx, "agent.loop.synthesis",
			core.IntAttr("iteration", cfg.MaxIter),
			core.BoolAttr("forced", true))
		defer synthSpan.End()
	}

	var resp core.ChatResponse
	var err error
	synthReq := core.ChatRequest{Messages: state.messages, GenerationParams: cfg.GenParams}
	if ch != nil {
		synthCh, wait := newObjectStreamForwarder(ctx, ch, defaultIterChBufSize, state, cfg.ResponseSchema)
		resp, err = cfg.Provider.ChatStream(synthCtx, synthReq, synthCh)
		wait()
	} else {
		resp, err = core.Chat(synthCtx, cfg.Provider, synthReq)
	}
	if err != nil {
		cfg.Logger.Error("synthesis LLM call failed", "agent", cfg.Name, "error", err)
		r := terminateIteration(ctx, cfg, ch, state, core.FinishError, AgentResult{}, err)
		return r.final, r.err
	}
	cfg.Logger.Info("synthesis completed", "agent", cfg.Name,
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens)
	state.totalUsage.InputTokens += resp.Usage.InputTokens
	state.totalUsage.OutputTokens += resp.Usage.OutputTokens

	captureProviderMeta(state, &resp)

	if r, handled := runPostLLMOrHandle(ctx, synthCtx, cfg, task, ch, state, &resp, iterEndParams{}); handled {
		return r.final, r.err
	}

	if resp.Thinking != "" {
		state.lastThinking = resp.Thinking
	}

	cfg.Mem.PersistTurn(synthCtx, cfg.Name, task, task.Input, resp.Content, state.steps)
	result := AgentResult{
		Output:      resp.Content,
		Thinking:    state.lastThinking,
		Attachments: mergeAttachments(state.accumulatedAttachments, resp.Attachments),
	}
	state.patchTerminal(&result, core.FinishMaxIter)
	emitObjectFinish(ctx, ch, cfg.ResponseSchema, resp.Content, &result)
	finalizeRun(ctx, ch, state, cfg.Name, core.FinishMaxIter, result)
	return result, nil
}
