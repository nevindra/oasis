package network

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
)

// SupervisorPolicy wraps a child Agent with restart/fallback/quorum/
// circuit-breaker behavior. Plan B ships four built-in policies. Compose
// multiple via Chain.
type SupervisorPolicy interface {
	Wrap(child core.Agent) core.Agent
}

// ErrCircuitOpen is returned by a CircuitBreaker-wrapped agent when the
// circuit is open (too many recent failures).
var ErrCircuitOpen = errors.New("network: circuit breaker open")

// WithSupervisor applies a SupervisorPolicy to every child agent at
// construction time. Per-child policies (WithSupervisorFor) compose on top.
func WithSupervisor(p SupervisorPolicy) Option {
	return func(n *Network) { n.supervisor = p }
}

// WithSupervisorFor applies a SupervisorPolicy to a specific child by name.
// Multiple calls for the same name compose via Chain in registration order.
func WithSupervisorFor(name string, p SupervisorPolicy) Option {
	return func(n *Network) {
		if n.supervisorPerChild == nil {
			n.supervisorPerChild = make(map[string]SupervisorPolicy)
		}
		existing := n.supervisorPerChild[name]
		if existing != nil {
			n.supervisorPerChild[name] = Chain(existing, p)
		} else {
			n.supervisorPerChild[name] = p
		}
	}
}

// --- RestartOnFail ---

// RestartOnFail retries the child up to maxRestarts times before propagating
// the failure. A maxRestarts of 0 means no retries (one attempt total).
func RestartOnFail(maxRestarts int, delay ...time.Duration) SupervisorPolicy {
	var d time.Duration
	if len(delay) > 0 {
		d = delay[0]
	}
	return &restartPolicy{max: maxRestarts, delay: d}
}

type restartPolicy struct {
	max   int
	delay time.Duration
}

func (p *restartPolicy) Wrap(child core.Agent) core.Agent {
	return &restartAgent{Agent: child, max: p.max, delay: p.delay}
}

type restartAgent struct {
	core.Agent
	max   int
	delay time.Duration
}

// Unwrap returns the wrapped child so topology and dispatch can see through
// the supervisor layer (e.g. classify a wrapped Network as KindNetwork).
func (r *restartAgent) Unwrap() core.Agent { return r.Agent }

func (r *restartAgent) Execute(ctx context.Context, task core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	var lastErr error
	for attempt := 0; attempt <= r.max; attempt++ {
		if ctx.Err() != nil {
			return core.AgentResult{}, ctx.Err()
		}
		res, err := r.Agent.Execute(ctx, task, opts...)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if r.delay > 0 && attempt < r.max {
			select {
			case <-time.After(r.delay):
			case <-ctx.Done():
				return core.AgentResult{}, ctx.Err()
			}
		}
	}
	return core.AgentResult{}, lastErr
}

// --- Fallback ---

// Fallback tries the child first; on error, runs backup and returns its result.
func Fallback(backup core.Agent) SupervisorPolicy {
	return &fallbackPolicy{backup: backup}
}

type fallbackPolicy struct{ backup core.Agent }

func (p *fallbackPolicy) Wrap(child core.Agent) core.Agent {
	return &fallbackAgent{primary: child, backup: p.backup}
}

type fallbackAgent struct {
	primary, backup core.Agent
}

func (f *fallbackAgent) Name() string        { return f.primary.Name() }
func (f *fallbackAgent) Description() string { return f.primary.Description() }

// Unwrap returns the primary agent so callers (topology, dispatch) can see
// through the supervisor layer to the underlying child.
func (f *fallbackAgent) Unwrap() core.Agent { return f.primary }

func (f *fallbackAgent) Execute(ctx context.Context, task core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	res, err := f.primary.Execute(ctx, task, opts...)
	if err == nil {
		return res, nil
	}
	return f.backup.Execute(ctx, task, opts...)
}

// --- Quorum ---

// Quorum asks askN agents in parallel and returns the result that at least
// takeMajorityOfN of them agree on. Agreement is byte-equality on Output.
// The wrapped child's Execute is NOT called when Quorum is applied — the
// quorum members run instead, and the child supplies only Name/Description.
func Quorum(askN, takeMajorityOfN int, members ...core.Agent) SupervisorPolicy {
	if len(members) != askN {
		panic("network.Quorum: len(members) must equal askN")
	}
	if takeMajorityOfN < 1 || takeMajorityOfN > askN {
		panic("network: Quorum threshold must be in [1, askN]")
	}
	return &quorumPolicy{askN: askN, threshold: takeMajorityOfN, members: members}
}

type quorumPolicy struct {
	askN, threshold int
	members         []core.Agent
}

func (p *quorumPolicy) Wrap(child core.Agent) core.Agent {
	return &quorumAgent{
		child:     child,
		members:   p.members,
		threshold: p.threshold,
	}
}

// quorumAgent retains child so topology and dispatch can Unwrap to the
// underlying agent. The child's Execute is not invoked — the quorum members
// run instead — but the child still supplies Name/Description and identity
// for classification.
type quorumAgent struct {
	child     core.Agent
	members   []core.Agent
	threshold int
}

func (q *quorumAgent) Name() string        { return q.child.Name() }
func (q *quorumAgent) Description() string { return q.child.Description() }

// Unwrap returns the wrapped child so callers (topology, dispatch) can see
// through the supervisor layer to the underlying agent.
func (q *quorumAgent) Unwrap() core.Agent { return q.child }

func (q *quorumAgent) Execute(ctx context.Context, task core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	type vote struct {
		res core.AgentResult
		err error
	}
	// Child context lets us cancel in-flight members once the threshold is
	// reached, actually realising the LLM round-trip savings the comment
	// below describes. defer cancel() fires on every return path.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Buffered to len(members) so worker goroutines never block on send and
	// can finish even after the caller returns on ctx.Done() or after an
	// early threshold-reached return.
	votes := make(chan vote, len(q.members))
	for _, m := range q.members {
		m := m
		go func() {
			r, err := m.Execute(ctx, task, opts...)
			votes <- vote{r, err}
		}()
	}
	tally := map[string]int{}
	var errs []error
	for i := 0; i < len(q.members); i++ {
		select {
		case v := <-votes:
			if v.err != nil {
				errs = append(errs, v.err)
				continue
			}
			tally[v.res.Output]++
			// Why: return as soon as any output reaches the threshold.
			// Waiting for the remaining members would burn extra LLM round-trips
			// and recovery latency for a decision that's already settled.
			// Remaining workers send into the buffered channel and exit cleanly.
			// Usage of in-flight members is necessarily dropped — collecting it
			// would defeat the early-exit. Only the deciding vote's Usage flows
			// to the caller.
			if tally[v.res.Output] >= q.threshold {
				return core.AgentResult{Output: v.res.Output, Usage: v.res.Usage}, nil
			}
		case <-ctx.Done():
			return core.AgentResult{}, ctx.Err()
		}
	}
	// Why: if every member failed, the caller is looking at an infrastructure
	// outage, not a legitimate quorum miss. Return the joined member errors so
	// callers can inspect them rather than masking the failure behind a generic
	// threshold-miss message that looks like a business logic disagreement.
	if len(errs) == len(q.members) {
		return core.AgentResult{}, errors.Join(errs...)
	}
	return core.AgentResult{}, fmt.Errorf("network.Quorum: no result reached threshold %d/%d", q.threshold, len(q.members))
}

// --- CircuitBreaker ---

// CircuitBreaker opens the circuit after `threshold` consecutive failures;
// while open, calls return ErrCircuitOpen without invoking the child.
// The circuit closes again after `cooldown` has elapsed since opening.
//
// Panics at construction time if threshold <= 0: a non-positive threshold
// silently degrades the breaker to a no-op (the `fails >= threshold` check
// is always true but `openedAt` is never set), so failing fast at wiring
// time prevents a misconfiguration that would otherwise hide all protection.
// Mirrors the Quorum constructor's validation.
func CircuitBreaker(threshold int, cooldown time.Duration) SupervisorPolicy {
	if threshold <= 0 {
		panic("network.CircuitBreaker: threshold must be > 0")
	}
	return &breakerPolicy{threshold: threshold, cooldown: cooldown}
}

type breakerPolicy struct {
	threshold int
	cooldown  time.Duration
}

func (p *breakerPolicy) Wrap(child core.Agent) core.Agent {
	return &breakerAgent{Agent: child, threshold: p.threshold, cooldown: p.cooldown}
}

type breakerAgent struct {
	core.Agent
	threshold int
	cooldown  time.Duration
	mu        sync.Mutex
	fails     int
	openedAt  time.Time
	// probing is true while exactly one goroutine is executing a trial call
	// after the cooldown elapses. All other goroutines must see ErrCircuitOpen
	// until the prober reports success or failure.
	probing bool
}

// Unwrap returns the wrapped child so callers (topology, dispatch) can see
// through the supervisor layer to the underlying agent.
func (b *breakerAgent) Unwrap() core.Agent { return b.Agent }

func (b *breakerAgent) Execute(ctx context.Context, task core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	// Why: track "am I the prober" locally rather than reading b.probing after
	// Execute returns. A concurrent normal-path goroutine that started while
	// the circuit was closed could finish its Execute and re-acquire the lock
	// while another goroutine is still mid-probe — reading b.probing=true in
	// that goroutine's post-Execute path would wrongly route it through the
	// probe-recovery branch and clear the flag out from under the real prober.
	imProbing := false
	b.mu.Lock()
	if b.fails >= b.threshold {
		if time.Since(b.openedAt) < b.cooldown {
			b.mu.Unlock()
			return core.AgentResult{}, ErrCircuitOpen
		}
		// Why: only one goroutine may probe the recovering downstream at a time.
		// Without this guard, every concurrent caller that races past the cooldown
		// check would all call Agent.Execute simultaneously — the thundering-herd
		// problem. The first caller sets probing=true and becomes the sole prober;
		// all others still see ErrCircuitOpen until the probe resolves.
		if b.probing {
			b.mu.Unlock()
			return core.AgentResult{}, ErrCircuitOpen
		}
		b.probing = true
		imProbing = true
	}
	b.mu.Unlock()

	res, err := b.Agent.Execute(ctx, task, opts...)

	b.mu.Lock()
	defer b.mu.Unlock()
	if imProbing {
		// We were the prober — clear the flag regardless of outcome.
		b.probing = false
		if err == nil {
			// Probe succeeded: circuit is healthy again.
			b.fails = 0
		} else {
			// Probe failed: re-open the circuit with a fresh cooldown window.
			b.fails++
			b.openedAt = time.Now()
		}
		return res, err
	}
	// Normal (non-probe) path: circuit was closed when we entered.
	if err != nil {
		b.fails++
		if b.fails == b.threshold {
			b.openedAt = time.Now()
		}
		return res, err
	}
	b.fails = 0
	return res, nil
}

// --- Chain ---

// Chain composes multiple policies into one. Earlier policies wrap closer to
// the child; later policies wrap further out. So Chain(restart, fallback)
// means: first try restart; if all restarts fail, then fallback.
func Chain(policies ...SupervisorPolicy) SupervisorPolicy {
	return &chainPolicy{policies: policies}
}

type chainPolicy struct{ policies []SupervisorPolicy }

func (p *chainPolicy) Wrap(child core.Agent) core.Agent {
	wrapped := child
	for _, policy := range p.policies {
		wrapped = policy.Wrap(wrapped)
	}
	return wrapped
}
