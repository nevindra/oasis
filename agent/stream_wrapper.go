package agent

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/nevindra/oasis/core"
)

const (
	defaultStreamReplayLimit = 256
	maxStreamReplayLimit     = 4096
	defaultSubscriberBufSize = 32
)

// Stream is an opt-in wrapper around ExecuteStream that provides multi-reader
// fan-out, bounded replay, blocking accessors, and event-typed callbacks.
//
// Construction via StartStream / StartStreamWith spawns one goroutine that
// runs the underlying agent and dispatches events to every subscriber.
// Stream is safe for concurrent use by multiple goroutines.
//
// Zero overhead for callers that don't construct a Stream: ExecuteStream
// remains the kernel API.
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

// StartStream runs agent.ExecuteStream in a background goroutine and returns
// a Stream that consumers may subscribe to or query for the final result.
//
// The Stream's lifecycle ends when ExecuteStream returns; at that point Done()
// closes and Result() returns the captured AgentResult and error. Subscribers
// receive the final events before their channels close.
//
// Use StartStreamWith to pass RunOptions (notably StreamReplayLimit).
func StartStream(ctx context.Context, agent StreamingAgent, task AgentTask) *Stream {
	return startStream(ctx, agent, task, nil)
}

// StartStreamWith is the RunOptions-aware constructor. Pass nil opts for
// agent defaults. Honors opts.StreamReplayLimit if set; clamped to
// [1, maxStreamReplayLimit].
func StartStreamWith(ctx context.Context, agent StreamingAgentWithOptions, task AgentTask, opts *RunOptions) *Stream {
	return startStream(ctx, &runOptsAdapter{agent: agent, opts: opts}, task, opts)
}

// runOptsAdapter lets startStream treat a StreamingAgentWithOptions like a
// plain StreamingAgent for the inner ExecuteStream call.
type runOptsAdapter struct {
	agent StreamingAgentWithOptions
	opts  *RunOptions
}

func (a *runOptsAdapter) Name() string        { return a.agent.Name() }
func (a *runOptsAdapter) Description() string { return a.agent.Description() }
func (a *runOptsAdapter) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return a.agent.ExecuteWith(ctx, task, a.opts)
}
func (a *runOptsAdapter) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- core.StreamEvent) (AgentResult, error) {
	return a.agent.ExecuteStreamWith(ctx, task, ch, a.opts)
}

func startStream(ctx context.Context, agent StreamingAgent, task AgentTask, opts *RunOptions) *Stream {
	limit := defaultStreamReplayLimit
	if opts != nil && opts.StreamReplayLimit > 0 {
		limit = opts.StreamReplayLimit
		if limit > maxStreamReplayLimit {
			limit = maxStreamReplayLimit
		}
	}

	s := &Stream{
		replay:      make([]core.StreamEvent, 0, limit),
		replayLimit: limit,
		done:        make(chan struct{}),
	}

	go s.run(ctx, agent, task)

	return s
}

// run drives the underlying agent and dispatches events. Closes Done when
// ExecuteStream returns. Closes every subscriber's channel.
func (s *Stream) run(ctx context.Context, agent StreamingAgent, task AgentTask) {
	defer close(s.done)

	ch := make(chan core.StreamEvent, defaultIterChBufSize)

	type runResult struct {
		res AgentResult
		err error
	}
	resCh := make(chan runResult, 1)
	go func() {
		r, err := agent.ExecuteStream(ctx, task, ch)
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
