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
func RestartOnFail(maxRestarts int) SupervisorPolicy {
	return &restartPolicy{max: maxRestarts}
}

type restartPolicy struct{ max int }

func (p *restartPolicy) Wrap(child core.Agent) core.Agent {
	return &restartAgent{Agent: child, max: p.max}
}

type restartAgent struct {
	core.Agent
	max int
}

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
	return &quorumPolicy{askN: askN, threshold: takeMajorityOfN, members: members}
}

type quorumPolicy struct {
	askN, threshold int
	members         []core.Agent
}

func (p *quorumPolicy) Wrap(child core.Agent) core.Agent {
	return &quorumAgent{
		name:        child.Name(),
		description: child.Description(),
		members:     p.members,
		threshold:   p.threshold,
	}
}

type quorumAgent struct {
	name, description string
	members           []core.Agent
	threshold         int
}

func (q *quorumAgent) Name() string        { return q.name }
func (q *quorumAgent) Description() string { return q.description }
func (q *quorumAgent) Execute(ctx context.Context, task core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	type vote struct {
		res core.AgentResult
		err error
	}
	votes := make([]vote, len(q.members))
	var wg sync.WaitGroup
	wg.Add(len(q.members))
	for i, m := range q.members {
		i, m := i, m
		go func() {
			defer wg.Done()
			r, err := m.Execute(ctx, task, opts...)
			votes[i] = vote{r, err}
		}()
	}
	wg.Wait()
	tally := map[string]int{}
	for _, v := range votes {
		if v.err != nil {
			continue
		}
		tally[v.res.Output]++
	}
	for out, count := range tally {
		if count >= q.threshold {
			return core.AgentResult{Output: out}, nil
		}
	}
	return core.AgentResult{}, fmt.Errorf("network.Quorum: no result reached threshold %d/%d", q.threshold, len(q.members))
}

// --- CircuitBreaker ---

// CircuitBreaker opens the circuit after `threshold` consecutive failures;
// while open, calls return ErrCircuitOpen without invoking the child.
// The circuit closes again after `cooldown` has elapsed since opening.
func CircuitBreaker(threshold int, cooldown time.Duration) SupervisorPolicy {
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
}

func (b *breakerAgent) Execute(ctx context.Context, task core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	b.mu.Lock()
	if b.fails >= b.threshold {
		if time.Since(b.openedAt) < b.cooldown {
			b.mu.Unlock()
			return core.AgentResult{}, ErrCircuitOpen
		}
		b.fails = 0
	}
	b.mu.Unlock()

	res, err := b.Agent.Execute(ctx, task, opts...)
	b.mu.Lock()
	defer b.mu.Unlock()
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
