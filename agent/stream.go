package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/internal/runtime"
)

// The StreamEvent struct, StreamEventType, and Event* constants live in
// github.com/nevindra/oasis/core and are re-exported as aliases in
// types_aliases.go. The helpers below stay at root because they depend on the
// Agent / AgentResult / AgentTask abstractions that live here.

// --- Constants ---

// defaultIterChBufSize is the per-iteration StreamEvent forwarder buffer.
// Holding at 64 (the pre-Phase-4 value) until a real-workload measurement
// justifies a reduction. Phase 4 finding 4.1.a originally proposed dropping
// this to 16-32 based on observed fill, but that decision needs deployment
// telemetry (e.g. instrumenting `cmd/bot_example` against live LLM streaming),
// which is out of reach in current dev environments. BenchmarkIterChStreaming
// in loop_bench_test.go is the regression guard for any future change.
const defaultIterChBufSize = 64

const (
	defaultStreamReplayLimit = 256
	maxStreamReplayLimit     = 4096
	defaultSubscriberBufSize = 32
)

// --- HTTP/SSE helpers ---

// ServeSSE streams an agent's response as Server-Sent Events over HTTP.
//
// It validates that w implements [http.Flusher], sets SSE headers, creates a
// buffered [StreamEvent] channel, runs the agent in a background goroutine,
// and writes each event as:
//
//	event: <event-type>
//	data: <json-encoded StreamEvent>
//
// The stream emits [EventRunStart] as the first event and [EventRunFinish] as
// the last event inside the channel loop. [EventRunFinish] carries the
// [FinishReason] and any provider warnings or metadata. After the channel
// closes, a final "done" SSE event is written for legacy clients that wait on
// it. New clients should read [EventRunFinish] for structured completion data.
//
// On completion it sends a final "done" event. If the agent returns an error,
// it is sent as an "error" event before returning.
//
// Client disconnection propagates via ctx cancellation to the agent.
// Callers typically pass r.Context() as ctx.
func ServeSSE(ctx context.Context, w http.ResponseWriter, agent core.Agent, task AgentTask) (AgentResult, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return AgentResult{}, fmt.Errorf("ResponseWriter does not implement http.Flusher")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan core.StreamEvent, 64)
	safeClose := onceClose(ch)

	type execResult struct {
		result AgentResult
		err    error
	}
	resultCh := make(chan execResult, 1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				// Ensure ch is closed so the for-range loop below
				// doesn't block forever, then signal the error.
				// Use sync.Once because ExecuteStream may have already
				// closed ch before the panic site.
				safeClose()
				resultCh <- execResult{AgentResult{}, fmt.Errorf("agent panic: %v", p)}
				return
			}
		}()
		r, err := agent.Execute(ctx, task, core.WithStream(ch))
		resultCh <- execResult{r, err}
	}()

	for ev := range ch {
		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
		flusher.Flush()
	}

	res := <-resultCh

	if res.err != nil {
		errData, _ := json.Marshal(map[string]string{"error": res.err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", errData)
		flusher.Flush()
		return res.result, res.err
	}

	doneData, _ := json.Marshal(res.result)
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", doneData)
	flusher.Flush()

	return res.result, nil
}

// WriteSSEEvent writes a single Server-Sent Event to w and flushes.
// It validates that w implements [http.Flusher], JSON-marshals data into
// the SSE data field, and flushes immediately. eventType is the SSE event
// name (e.g. "text-delta", "done").
//
// Use this to compose custom SSE loops with [core.Agent.Execute]:
//
//	ch := make(chan oasis.StreamEvent, 64)
//	go agent.Execute(ctx, task, core.WithStream(ch))
//	for ev := range ch {
//	    oasis.WriteSSEEvent(w, string(ev.Type), ev)
//	}
//	oasis.WriteSSEEvent(w, "done", customPayload)
func WriteSSEEvent(w http.ResponseWriter, eventType string, data any) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("ResponseWriter does not implement http.Flusher")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal sse data: %w", err)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, encoded)
	flusher.Flush()
	return nil
}

// --- Context sink helpers ---

// contextWithStreamSink stores ch on ctx so middleware that needs to emit
// stream events can recover it. Returns a derived context.
//
// The agent dispatch path is responsible for calling this before invoking
// dispatch when a stream channel is configured (non-nil). Callers may pass
// a nil channel to clear an inherited sink — the helper checks for nil
// before storing.
//
// Delegates to runtime.ContextWithStreamSink so that all middleware in the
// runtime package can retrieve the sink via the same context key.
func contextWithStreamSink(ctx context.Context, ch chan<- core.StreamEvent) context.Context {
	return runtime.ContextWithStreamSink(ctx, ch)
}

// streamSinkFromContext returns the StreamEvent sink stored on ctx by
// contextWithStreamSink, or nil if none is set.
//
// Delegates to runtime.StreamSinkFromContext so that the same key is used
// everywhere.
func streamSinkFromContext(ctx context.Context) chan<- core.StreamEvent {
	return runtime.StreamSinkFromContext(ctx)
}

// --- Internal forwarders ---

// newStreamForwarder creates an intermediate StreamEvent channel and spawns a
// goroutine that forwards events from it to dest until the channel is closed
// by the producer (typically provider.ChatStream's defer-close) or ctx is
// cancelled.
//
// Returns (iterCh, wait). Callers pass iterCh to the producer (provider) and
// MUST call wait() after the producer returns to ensure the forwarder
// finishes drain-and-exit before subsequent code observes dest.
//
// The forwarder pattern centralizes three previously duplicated sites in
// runLoop and ensures the producer's defer-close doesn't touch dest directly
// (the caller's safeCloseCh is the sole closer of dest).
func newStreamForwarder(ctx context.Context, dest chan<- core.StreamEvent, bufSize int) (chan<- core.StreamEvent, func()) {
	iterCh := make(chan core.StreamEvent, bufSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range iterCh {
			select {
			case dest <- ev:
			case <-ctx.Done():
				// Drain remaining events so the producer can close iterCh.
				for range iterCh {
				}
				return
			}
		}
	}()
	return iterCh, wg.Wait
}

// newCapturingStreamForwarder is like newStreamForwarder but also captures
// EventFileAttachment events into state.files. Used for provider streaming paths
// where the provider may emit EventFileAttachment alongside text deltas.
func newCapturingStreamForwarder(ctx context.Context, dest chan<- core.StreamEvent, bufSize int, state *loopState) (chan<- core.StreamEvent, func()) {
	iterCh := make(chan core.StreamEvent, bufSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range iterCh {
			// Task 3.4: Capture file attachments into state.files.
			captureFileEvent(ev, state)
			select {
			case dest <- ev:
			case <-ctx.Done():
				// Drain remaining events so the producer can close iterCh.
				for range iterCh {
				}
				return
			}
		}
	}()
	return iterCh, wg.Wait
}

// newFileCapturingSink creates an intermediate StreamEvent channel for tool
// dispatch. Events are forwarded to dest; EventFileAttachment events are also
// decoded and appended to state.files so that AgentResult.Files is populated.
//
// Returns (sinkCh, wait). The caller MUST close sinkCh after all tools have
// finished writing, then call wait() to ensure the forwarder has drained.
//
// When dest is nil (non-streaming Execute path), returns (nil, func(){}) so
// contextWithStreamSink can safely receive nil and skip sink registration.
func newFileCapturingSink(ctx context.Context, dest chan<- core.StreamEvent, state *loopState) (chan core.StreamEvent, func()) {
	if dest == nil {
		return nil, func() {}
	}
	sinkCh := make(chan core.StreamEvent, defaultIterChBufSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range sinkCh {
			// Task 3.4: Capture file attachments into state.files.
			captureFileEvent(ev, state)
			select {
			case dest <- ev:
			case <-ctx.Done():
				// Drain so tools can finish writing.
				for range sinkCh {
				}
				return
			}
		}
	}()
	return sinkCh, wg.Wait
}

// captureFileEvent checks whether ev is an EventFileAttachment and, if so,
// decodes the file metadata from ev.Content and appends it to state.files.
// The event Content carries a JSON object: {"name":"...","mime_type":"...","size":N,"url":"..."}.
func captureFileEvent(ev core.StreamEvent, state *loopState) {
	if ev.Type != core.EventFileAttachment {
		return
	}
	var att core.Attachment
	if err := json.Unmarshal([]byte(ev.Content), &att); err == nil {
		state.files = append(state.files, att)
	}
}

// --- Object-stream extensions ---

// newObjectStreamForwarder is like newCapturingStreamForwarder but also emits
// EventObjectDelta snapshots (via core.PartialJSON) as text deltas arrive when
// cfg.responseSchema is set. For top-level array schemas it additionally emits
// EventElementDelta once per completed array element.
//
// Returns (iterCh, wait). Callers pass iterCh to the provider and MUST call
// wait() after the provider returns to ensure the forwarder finishes draining.
//
// When dest is nil (non-streaming Execute path), falls back to
// newCapturingStreamForwarder (which no-ops on nil dest).
func newObjectStreamForwarder(ctx context.Context, dest chan<- core.StreamEvent, bufSize int, state *loopState, schema *core.ResponseSchema) (chan<- core.StreamEvent, func()) {
	if dest == nil || schema == nil {
		return newCapturingStreamForwarder(ctx, dest, bufSize, state)
	}

	// Detect whether the schema's top-level type is "array".
	isArraySchema := false
	{
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(schema.Schema, &probe)
		isArraySchema = probe.Type == "array"
	}

	iterCh := make(chan core.StreamEvent, bufSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()

		var (
			buf         []byte          // accumulates text deltas
			lastEmit    []byte          // last snapshot sent as EventObjectDelta (for dedup)
			elemTracker *elementTracker // non-nil only for top-level array schemas
		)
		if isArraySchema {
			elemTracker = newElementTracker()
		}

		for ev := range iterCh {
			// Capture file attachments (same as newCapturingStreamForwarder).
			captureFileEvent(ev, state)

			if ev.Type == core.EventTextDelta {
				buf = append(buf, ev.Content...)

				if isArraySchema && elemTracker != nil {
					// Feed new bytes to element tracker and emit any completed elements.
					newElems := elemTracker.feed(buf)
					for _, elemBytes := range newElems {
						select {
						case dest <- core.StreamEvent{Type: core.EventElementDelta, Object: elemBytes}:
						case <-ctx.Done():
							// drain and exit
							for range iterCh {
							}
							return
						}
					}
				}

				// Emit EventObjectDelta snapshot (deduplicated).
				if snap, ok := core.PartialJSON(buf); ok && !bytes.Equal(snap, lastEmit) {
					lastEmit = append(lastEmit[:0], snap...)
					select {
					case dest <- core.StreamEvent{Type: core.EventObjectDelta, Object: snap}:
					case <-ctx.Done():
						for range iterCh {
						}
						return
					}
				}
			}

			// Forward the original event.
			select {
			case dest <- ev:
			case <-ctx.Done():
				for range iterCh {
				}
				return
			}
		}
	}()
	return iterCh, wg.Wait
}

// emitObjectFinish emits an EventObjectFinish event and populates result.Object
// when the schema is configured and resp.Content is valid JSON.
func emitObjectFinish(ctx context.Context, ch chan<- core.StreamEvent, schema *core.ResponseSchema, content string, result *AgentResult) {
	if ch == nil || schema == nil || len(content) == 0 {
		return
	}
	b := []byte(content)
	if !json.Valid(b) {
		return
	}
	result.Object = b
	select {
	case ch <- core.StreamEvent{Type: core.EventObjectFinish, Object: b}:
	case <-ctx.Done():
	}
}

// elementTracker detects completed top-level array elements in a streaming
// JSON buffer. It tracks brace/bracket depth (skipping inside strings) and
// fires once per element as it closes at depth 1 (inside the top-level array).
//
// Call feed(buf) with the full accumulated buffer each time new bytes arrive.
// It remembers how far it has scanned and returns any newly completed element
// byte slices (ready to JSON-unmarshal individually).
type elementTracker struct {
	scanned   int  // bytes already processed in previous calls
	depth     int  // current nesting depth
	inString  bool // currently inside a JSON string
	escape    bool // last char was backslash inside a string
	elemStart int  // byte offset where the current element started (-1 if none)
}

func newElementTracker() *elementTracker {
	return &elementTracker{elemStart: -1}
}

// feed processes bytes from buf[t.scanned:] and returns slices (from buf) for
// any newly completed top-level array elements. The returned slices are valid
// subslices of buf — callers should copy them if they need long-lived data.
func (t *elementTracker) feed(buf []byte) []json.RawMessage {
	var completed []json.RawMessage

	for i := t.scanned; i < len(buf); i++ {
		b := buf[i]

		if t.inString {
			if t.escape {
				t.escape = false
				continue
			}
			if b == '\\' {
				t.escape = true
				continue
			}
			if b == '"' {
				t.inString = false
			}
			continue
		}

		switch b {
		case '"':
			t.inString = true
		case '{', '[':
			if t.depth == 1 && t.elemStart == -1 {
				// Start of a new element inside the top-level array.
				t.elemStart = i
			}
			t.depth++
		case '}', ']':
			t.depth--
			if t.depth == 1 && t.elemStart != -1 {
				// Completed an element at depth 1.
				elem := make([]byte, i+1-t.elemStart)
				copy(elem, buf[t.elemStart:i+1])
				completed = append(completed, json.RawMessage(elem))
				t.elemStart = -1
			}
			if t.depth == 0 {
				// Closed the top-level array — done.
				t.scanned = i + 1
				return completed
			}
		case ',':
			// At depth 1, commas separate elements. Scalar elements (strings,
			// numbers, booleans) would have already closed at depth reduction;
			// this handles literal/scalar top-level elements if they exist.
			if t.depth == 1 && t.elemStart != -1 {
				// Scalar element ended.
				elem := make([]byte, i-t.elemStart)
				copy(elem, buf[t.elemStart:i])
				// Trim trailing whitespace.
				trimmed := bytes.TrimRight(elem, " \t\n\r")
				if len(trimmed) > 0 && json.Valid(trimmed) {
					completed = append(completed, json.RawMessage(trimmed))
				}
				t.elemStart = -1
			} else if t.depth == 1 && t.elemStart == -1 {
				// Between elements at depth 1 — nothing to do.
			}
		default:
			// Non-whitespace, non-structure character at depth 1 = start of
			// scalar element (string already handled by '"' case above).
			if t.depth == 1 && t.elemStart == -1 && b != ' ' && b != '\t' && b != '\n' && b != '\r' {
				t.elemStart = i
			}
		}
	}
	t.scanned = len(buf)
	return completed
}

// --- Public Stream type + Subscribe ---

// Stream is an opt-in wrapper around the Subscribe API that provides
// multi-reader fan-out, bounded replay, blocking accessors, and event-typed
// callbacks.
//
// Construction via Subscribe spawns one goroutine that runs the underlying
// agent and dispatches events to every subscriber.
// Stream is safe for concurrent use by multiple goroutines.
type Stream struct {
	mu          sync.Mutex
	subscribers []*subscriber
	replay      []core.StreamEvent
	replayLimit int
	closed      bool

	done   chan struct{}
	result AgentResult
	err    error
}

type subscriber struct {
	ch       chan core.StreamEvent
	filter   core.StreamEventType // "" means catch-all
	callback func(core.StreamEvent)
	dropped  bool
}

// Subscribe runs ag.Execute in a background goroutine with WithStream wired
// up, and returns a Stream the caller may subscribe to or query for the
// final result. Pass additional core.RunOption values to layer overrides
// (e.g. agent.WithOverrides) or deadlines onto the call.
func Subscribe(ctx context.Context, ag core.Agent, task AgentTask, opts ...core.RunOption) *Stream {
	return startStream(ctx, ag, task, opts...)
}

func startStream(ctx context.Context, agent core.Agent, task AgentTask, opts ...core.RunOption) *Stream {
	limit := defaultStreamReplayLimit
	cfg := core.ApplyRunOptions(opts...)
	if ro, ok := cfg.Overrides.(*RunOptions); ok && ro != nil && ro.StreamReplayLimit > 0 {
		limit = ro.StreamReplayLimit
		if limit > maxStreamReplayLimit {
			limit = maxStreamReplayLimit
		}
	}

	s := &Stream{
		replay:      make([]core.StreamEvent, 0, limit),
		replayLimit: limit,
		done:        make(chan struct{}),
	}

	go s.run(ctx, agent, task, opts...)

	return s
}

// run drives the underlying agent and dispatches events. Closes Done when
// Execute returns. Closes every subscriber's channel.
func (s *Stream) run(ctx context.Context, agent core.Agent, task AgentTask, opts ...core.RunOption) {
	defer close(s.done)

	ch := make(chan core.StreamEvent, defaultIterChBufSize)

	type runResult struct {
		res AgentResult
		err error
	}
	resCh := make(chan runResult, 1)
	go func() {
		// Append WithStream to caller's options. Caller's options come first so
		// explicit caller WithStream (rare) would be replaced.
		callOpts := append(opts, core.WithStream(ch))
		r, err := agent.Execute(ctx, task, callOpts...)
		resCh <- runResult{r, err}
	}()

	for ev := range ch {
		s.dispatch(ev)
	}

	r := <-resCh
	s.finalize(r.res, r.err)
}

func (s *Stream) dispatch(ev core.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Append to replay ring buffer. Oldest events drop when full.
	if len(s.replay) >= s.replayLimit {
		s.replay = append(s.replay[1:], ev)
	} else {
		s.replay = append(s.replay, ev)
	}

	// Fan out to subscribers. Collect drops to close after the loop so we
	// don't mutate the loop's slice mid-iteration.
	var dropped []*subscriber
	for _, sub := range s.subscribers {
		if sub.dropped {
			continue
		}
		if !s.pushTo(sub, ev) {
			sub.dropped = true
			dropped = append(dropped, sub)
		}
	}

	// Notify and close dropped subscribers.
	for _, sub := range dropped {
		warn := core.StreamEvent{Type: core.EventStreamWarning, Content: "subscriber-dropped"}
		select {
		case sub.ch <- warn:
		default:
		}
		close(sub.ch)
	}
}

// pushTo writes ev to sub.ch non-blockingly. Returns false if the channel is
// full (slow subscriber). Callback subscribers are invoked synchronously and
// always return true (callback panics are recovered).
func (s *Stream) pushTo(sub *subscriber, ev core.StreamEvent) bool {
	if sub.callback != nil {
		func() {
			defer func() { _ = recover() }()
			if sub.filter == "" || sub.filter == ev.Type {
				sub.callback(ev)
			}
		}()
		return true
	}
	// Channel subscriber.
	select {
	case sub.ch <- ev:
		return true
	default:
		return false
	}
}

func (s *Stream) finalize(res AgentResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = res
	s.err = err
	s.closed = true
	for _, sub := range s.subscribers {
		if !sub.dropped && sub.callback == nil {
			close(sub.ch)
		}
	}
	s.subscribers = nil
}

// Events returns a new channel that receives a copy of every stream event.
// Late subscribers (after some events have been dispatched) receive a replay
// of buffered history first, then live events.
//
// The returned channel is closed when the underlying agent finishes. If the
// subscriber's channel fills (slow consumer), the wrapper emits a single
// EventStreamWarning{Content:"subscriber-dropped"} into the channel and closes
// it. Other subscribers are unaffected.
//
// Buffer size is fixed at defaultSubscriberBufSize (32). For larger needs,
// pull from a goroutine that forwards into your own buffered channel.
func (s *Stream) Events() <-chan core.StreamEvent {
	return s.subscribe("", nil)
}

// subscribe registers a new subscriber. filter is the event type to match
// (empty string = catch-all); callback is non-nil for OnXxx callbacks (no
// channel allocated in that case). Returns the channel for channel
// subscribers, nil for callback subscribers.
func (s *Stream) subscribe(filter core.StreamEventType, callback func(core.StreamEvent)) chan core.StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	var ch chan core.StreamEvent
	if callback == nil {
		ch = make(chan core.StreamEvent, defaultSubscriberBufSize)
		// Replay history non-blockingly. If the subscriber is slow before it
		// even starts reading, we treat it the same as a runtime drop.
		for _, ev := range s.replay {
			select {
			case ch <- ev:
			default:
				warn := core.StreamEvent{Type: core.EventStreamWarning, Content: "subscriber-dropped"}
				select {
				case ch <- warn:
				default:
				}
				close(ch)
				return ch
			}
		}
		// If the stream is already closed, deliver replay then close.
		if s.closed {
			close(ch)
			return ch
		}
	}

	s.subscribers = append(s.subscribers, &subscriber{
		ch:       ch,
		filter:   filter,
		callback: callback,
	})
	return ch
}

// Done returns a channel that closes when the underlying agent finishes.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Result blocks until the underlying agent finishes, then returns the final
// AgentResult and the error returned by ExecuteStream.
func (s *Stream) Result() (AgentResult, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, s.err
}

// Text blocks until completion and returns Result().Output.
func (s *Stream) Text() string {
	r, _ := s.Result()
	return r.Output
}

// Reasoning blocks until completion and returns Result().Thinking.
func (s *Stream) Reasoning() string {
	r, _ := s.Result()
	return r.Thinking
}

// Usage blocks until completion and returns Result().Usage.
func (s *Stream) Usage() core.Usage {
	r, _ := s.Result()
	return r.Usage
}

// ToolCalls blocks until completion and returns Result().ToolCalls().
func (s *Stream) ToolCalls() []core.ToolCall {
	r, _ := s.Result()
	return r.ToolCalls()
}

// ToolResults blocks until completion and returns Result().ToolResults().
func (s *Stream) ToolResults() []core.ToolResult {
	r, _ := s.Result()
	return r.ToolResults()
}

// FinishReason blocks until completion and returns Result().FinishReason.
func (s *Stream) FinishReason() core.FinishReason {
	r, _ := s.Result()
	return r.FinishReason
}

// Sources blocks until completion and returns Result().Sources.
func (s *Stream) Sources() []core.Source {
	r, _ := s.Result()
	return r.Sources
}

// Files blocks until completion and returns Result().Files.
func (s *Stream) Files() []core.Attachment {
	r, _ := s.Result()
	return r.Files
}

// Warnings blocks until completion and returns Result().Warnings.
func (s *Stream) Warnings() []string {
	r, _ := s.Result()
	return r.Warnings
}

// ProviderMeta blocks until completion and returns Result().ProviderMeta.
func (s *Stream) ProviderMeta() json.RawMessage {
	r, _ := s.Result()
	return r.ProviderMeta
}

// SuspendPayload blocks until completion and returns Result().SuspendPayload.
func (s *Stream) SuspendPayload() json.RawMessage {
	r, _ := s.Result()
	return r.SuspendPayload
}

// Suspended blocks until completion and returns Result().Suspended().
// Reports whether the run paused awaiting human input.
func (s *Stream) Suspended() bool {
	r, _ := s.Result()
	return r.Suspended()
}

// SuspendedProtocol blocks until completion and returns Result().SuspendedProtocol().
// Returns the typed protocol tag, or empty for untyped/non-suspended runs.
func (s *Stream) SuspendedProtocol() string {
	r, _ := s.Result()
	return r.SuspendedProtocol()
}

// Iterations blocks until completion and returns Result().Iterations.
func (s *Stream) Iterations() []core.IterationTrace {
	r, _ := s.Result()
	return r.Iterations
}

// OnEvent registers a catch-all callback invoked for every event in order.
// The callback runs on the dispatcher goroutine — keep it fast. Panics in
// the callback are recovered and ignored.
//
// Callbacks registered after subscription start receive only future events,
// not replay history. Subscribe via Events() if replay is needed.
func (s *Stream) OnEvent(fn func(core.StreamEvent)) {
	s.subscribe("", fn)
}

// OnTextDelta registers a callback invoked for every EventTextDelta event.
// fn receives the Content string directly.
func (s *Stream) OnTextDelta(fn func(string)) {
	s.subscribe(core.EventTextDelta, func(ev core.StreamEvent) {
		fn(ev.Content)
	})
}

// OnReasoningDelta registers a callback invoked for every EventReasoningDelta
// event. fn receives the Content string directly.
func (s *Stream) OnReasoningDelta(fn func(string)) {
	s.subscribe(core.EventReasoningDelta, func(ev core.StreamEvent) {
		fn(ev.Content)
	})
}

// OnToolCall registers a callback invoked when the LLM emits a tool call
// (EventToolCallStart). fn receives the reconstructed ToolCall.
func (s *Stream) OnToolCall(fn func(core.ToolCall)) {
	s.subscribe(core.EventToolCallStart, func(ev core.StreamEvent) {
		fn(core.ToolCall{ID: ev.ID, Name: ev.Name, Args: ev.Args})
	})
}

// OnToolResult registers a callback invoked when a tool returns
// (EventToolCallResult). fn receives a synthesized ToolResult with the raw
// Content. To inspect Usage or Duration, use OnEvent and read the
// StreamEvent directly.
func (s *Stream) OnToolResult(fn func(core.ToolResult)) {
	s.subscribe(core.EventToolCallResult, func(ev core.StreamEvent) {
		fn(core.ToolResult{Content: []byte(ev.Content)})
	})
}
