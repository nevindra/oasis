package agent

import (
	"context"
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

// dispatch appends to replay buffer and pushes to subscribers.
// Fan-out lands in the next task — for now it just appends to replay.
func (s *Stream) dispatch(ev core.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.replay) >= s.replayLimit {
		s.replay = append(s.replay[1:], ev)
	} else {
		s.replay = append(s.replay, ev)
	}
}

func (s *Stream) finalize(res AgentResult, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result = res
	s.err = err
	s.closed = true
	for _, sub := range s.subscribers {
		close(sub.ch)
	}
	s.subscribers = nil
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
