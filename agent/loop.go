package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
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
	suspendCount        *int64           // nil = no budget tracking; guarded by suspendMu
	suspendBytes        *int64           // guarded by suspendMu
	suspendMu           *sync.Mutex      // guards suspendCount/suspendBytes (Phase 4 finding 4.1.g)
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
	// maxSteps bounds AgentResult.Steps; 0 = unbounded; oldest entry dropped when exceeded.
	maxSteps    int
	prepareStep         PrepareStep         // optional; called before each LLM call
	onError             OnError             // optional; called on non-graceful LLM/tool errors
	onIterationComplete OnIterationComplete // optional; called after each iteration completes
	// lookupTool resolves a registered tool by name. When non-nil, the agent
	// loop checks each dispatched tool for core.Sourced and aggregates its
	// sources onto AgentResult.Sources. nil = no source aggregation.
	lookupTool func(string) (core.AnyTool, bool)
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

// runLoop is the shared tool-calling orchestrator used by both LLMAgent and
// Network. When ch is nil, it operates in blocking mode (Execute). When ch is
// non-nil, it emits StreamEvent values and closes ch when done (ExecuteStream).
//
// Iteration body lives in runIteration (iteration.go); the post-loop
// forced-synthesis tail lives in forceSynthesis below.
func runLoop(ctx context.Context, cfg LoopConfig, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	if cfg.logger == nil {
		cfg.logger = nopLogger
	}

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
	// Factor 8 per iteration: empirical 1 assistant + ~5-7 tool result messages; factor
	// 4 hit capacity at iteration 2 and realloc'd 6-8 times in 25-iter runs. Ceiling
	// guards against pathologically large maxIter pre-allocating megabytes upfront.
	const preAllocPer = 8
	const preAllocCeil = 2000
	var messages []ChatMessage
	if len(cfg.resumeMessages) > 0 {
		messages = cfg.resumeMessages
	} else {
		initial := cfg.mem.BuildMessages(ctx, cfg.name, cfg.systemPrompt, task)
		preAllocCap := cfg.maxIter * preAllocPer
		if preAllocCap > preAllocCeil {
			preAllocCap = preAllocCeil
		}
		messages = make([]ChatMessage, len(initial), len(initial)+preAllocCap)
		copy(messages, initial)
	}

	// Attachment byte budget (0/negative → default 50MB).
	attachByteBudget := cfg.maxAttachmentBytes
	if attachByteBudget <= 0 {
		attachByteBudget = maxAccumulatedAttachmentBytes
	}

	// Track initial message rune count for compression decisions.
	var messageRuneCount int
	for _, m := range messages {
		messageRuneCount += utf8.RuneCountInString(m.Content)
	}

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

	state := &loopState{
		messages:          messages,
		messageRuneCount:  messageRuneCount,
		attachByteBudget:  attachByteBudget,
		hasAgentTools:     hasAgentTools,
		compressThreshold: cfg.compressThreshold,
		safeCloseCh:       safeCloseCh,
	}

	for i := 0; i < cfg.maxIter; i++ {
		result := runIteration(ctx, cfg, task, ch, state, i)
		if result.outcome == iterDone {
			return result.final, result.err
		}
	}

	return forceSynthesis(ctx, cfg, task, ch, state)
}

// finalizeRun emits EventRunFinish with the supplied FinishReason and result
// metadata, then closes the streaming channel. Idempotent via state.safeCloseCh.
// Pass nil ch in non-streaming mode (Execute path); the function still invokes
// safeCloseCh so that non-streaming callers can share this helper.
func finalizeRun(ctx context.Context, ch chan<- StreamEvent, state *loopState, name string, reason FinishReason, result AgentResult) {
	if ch != nil {
		ev := StreamEvent{
			Type:         EventRunFinish,
			Name:         name,
			Content:      result.Output,
			Usage:        result.Usage,
			FinishReason: reason,
			Warnings:     result.Warnings,
			ProviderMeta: result.ProviderMeta,
		}
		if reason == FinishSuspended {
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
	state.safeCloseCh()
}

// forceSynthesis runs the post-loop forced-synthesis tail when runLoop hits
// cfg.maxIter without a natural termination. Previously emitted EventMaxIterReached
// for UIs; now collapses into FinishReason=FinishMaxIter on EventRunFinish.
func forceSynthesis(ctx context.Context, cfg LoopConfig, task AgentTask, ch chan<- StreamEvent, state *loopState) (AgentResult, error) {
	// EventMaxIterReached is collapsed into FinishReason=FinishMaxIter on
	// EventRunFinish (emitted by finalizeRun at the end of this function).
	// Log only so UIs that read EventRunFinish.FinishReason still get the info.
	cfg.logger.Warn("max iterations reached, forcing synthesis", "agent", cfg.name, "iteration", cfg.maxIter)
	state.messages = append(state.messages, UserMessage(
		"You have used all available tool calls. Summarize what you found and respond to the user."))

	// Synthesis span so the forced-response LLM call is visible in traces.
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
	synthReq := ChatRequest{Messages: state.messages, GenerationParams: cfg.generationParams}
	if ch != nil {
		// Intermediate channel so the provider's defer-close doesn't touch ch
		// directly. safeCloseCh remains the sole closer of ch.
		// newObjectStreamForwarder intercepts EventFileAttachment events into
		// state.files and emits EventObjectDelta snapshots when a schema is set.
		synthCh, wait := newObjectStreamForwarder(ctx, ch, defaultIterChBufSize, state, cfg.responseSchema)
		resp, err = cfg.provider.ChatStream(synthCtx, synthReq, synthCh)
		wait()
	} else {
		resp, err = core.Chat(synthCtx, cfg.provider, synthReq)
	}
	if err != nil {
		cfg.logger.Error("synthesis LLM call failed", "agent", cfg.name, "error", err)
		r := terminateIteration(ctx, cfg, ch, state, FinishError, AgentResult{}, err)
		return r.final, r.err
	}
	cfg.logger.Info("synthesis completed", "agent", cfg.name,
		"input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens)
	state.totalUsage.InputTokens += resp.Usage.InputTokens
	state.totalUsage.OutputTokens += resp.Usage.OutputTokens

	captureProviderMeta(state, &resp)

	// PostProcessor hook. endIter=nil because forceSynthesis has no iteration
	// span — the synthesis span is managed via defer above.
	if r, handled := runPostLLMOrHandle(ctx, synthCtx, cfg, task, ch, state, &resp, nil); handled {
		return r.final, r.err
	}

	// Capture thinking from the synthesis response.
	if resp.Thinking != "" {
		state.lastThinking = resp.Thinking
	}

	cfg.mem.PersistMessages(synthCtx, cfg.name, task, task.Input, resp.Content, state.steps)
	result := AgentResult{
		Output:       resp.Content,
		Thinking:     state.lastThinking,
		Attachments:  mergeAttachments(state.accumulatedAttachments, resp.Attachments),
		Usage:        state.totalUsage,
		Steps:        state.steps,
		FinishReason: FinishMaxIter,
		Warnings:     state.lastWarnings,
		ProviderMeta: state.lastProviderMeta,
		Files:        state.files,
		Iterations:   state.iterations,
		Sources:      state.sources,
	}
	emitObjectFinish(ctx, ch, cfg.responseSchema, resp.Content, &result)
	finalizeRun(ctx, ch, state, cfg.name, FinishMaxIter, result)
	return result, nil
}
