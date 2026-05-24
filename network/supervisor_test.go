package network

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// flakeyAgent fails its first failFor calls, then succeeds with output "ok".
type flakeyAgent struct {
	calls   atomic.Int32
	failFor int32
}

func (f *flakeyAgent) Name() string        { return "flakey" }
func (f *flakeyAgent) Description() string { return "fails N times then succeeds" }
func (f *flakeyAgent) Execute(_ context.Context, _ core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	n := f.calls.Add(1)
	if n <= f.failFor {
		return core.AgentResult{}, errors.New("flaky")
	}
	return core.AgentResult{Output: "ok"}, nil
}

// fixedAgent is a tiny test helper that returns a fixed output.
type fixedAgent struct {
	name, desc, out string
	err             error
}

func (s *fixedAgent) Name() string        { return s.name }
func (s *fixedAgent) Description() string { return s.desc }
func (s *fixedAgent) Execute(_ context.Context, _ core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	if s.err != nil {
		return core.AgentResult{}, s.err
	}
	return core.AgentResult{Output: s.out}, nil
}

func TestRestartOnFail_RetriesUntilLimit(t *testing.T) {
	f := &flakeyAgent{failFor: 2}
	wrapped := RestartOnFail(3).Wrap(f)
	res, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err != nil {
		t.Fatalf("expected success after 2 failures + 1 retry, got %v", err)
	}
	if res.Output != "ok" {
		t.Fatalf("Output: %q", res.Output)
	}
	if got := f.calls.Load(); got != 3 {
		t.Fatalf("expected 3 calls (2 fails + 1 success), got %d", got)
	}
}

func TestRestartOnFail_GivesUpAfterLimit(t *testing.T) {
	f := &flakeyAgent{failFor: 5}
	wrapped := RestartOnFail(2).Wrap(f)
	_, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err == nil {
		t.Fatal("expected error after exhausting restarts")
	}
	if got := f.calls.Load(); got != 3 { // initial + 2 restarts
		t.Fatalf("expected 3 calls (initial + 2 restarts), got %d", got)
	}
}

func TestFallback_UsesBackupOnError(t *testing.T) {
	primary := &fixedAgent{name: "primary", err: errors.New("boom")}
	backup := &fixedAgent{name: "backup", out: "rescued"}
	wrapped := Fallback(backup).Wrap(primary)
	res, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "rescued" {
		t.Fatalf("Output: got %q want rescued", res.Output)
	}
}

func TestFallback_KeepsPrimaryOnSuccess(t *testing.T) {
	primary := &fixedAgent{name: "primary", out: "primary-ok"}
	backup := &fixedAgent{name: "backup", out: "rescued"}
	wrapped := Fallback(backup).Wrap(primary)
	res, _ := wrapped.Execute(context.Background(), core.AgentTask{})
	if res.Output != "primary-ok" {
		t.Fatalf("Output: got %q want primary-ok", res.Output)
	}
}

func TestQuorum_TakesMajorityResult(t *testing.T) {
	a := &fixedAgent{name: "a", out: "yes"}
	b := &fixedAgent{name: "b", out: "yes"}
	c := &fixedAgent{name: "c", out: "no"}
	policy := Quorum(3, 2, a, b, c)
	primary := &fixedAgent{name: "primary", out: "ignored"}
	wrapped := policy.Wrap(primary)
	res, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "yes" {
		t.Fatalf("Output: got %q want yes", res.Output)
	}
}

func TestQuorum_NoMajorityErrors(t *testing.T) {
	a := &fixedAgent{name: "a", out: "x"}
	b := &fixedAgent{name: "b", out: "y"}
	c := &fixedAgent{name: "c", out: "z"}
	policy := Quorum(3, 2, a, b, c)
	wrapped := policy.Wrap(&fixedAgent{name: "primary", out: "ignored"})
	_, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err == nil {
		t.Fatal("expected error when no majority")
	}
}

// slowCancellableAgent blocks until its context is cancelled (or a deadline
// fires), then records whether cancellation was the cause. Used to assert
// that quorum cancels in-flight members once the threshold is reached.
type slowCancellableAgent struct {
	name        string
	out         string
	delay       time.Duration
	ctxCancelled atomic.Bool
}

func (s *slowCancellableAgent) Name() string        { return s.name }
func (s *slowCancellableAgent) Description() string { return "slow cancellable agent" }
func (s *slowCancellableAgent) Execute(ctx context.Context, _ core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	select {
	case <-ctx.Done():
		s.ctxCancelled.Store(true)
		return core.AgentResult{}, ctx.Err()
	case <-time.After(s.delay):
		return core.AgentResult{Output: s.out}, nil
	}
}

func TestQuorum_CancelsRemainingMembersOnThreshold(t *testing.T) {
	// members[0] and members[1] agree immediately; members[2] is slow and
	// should have its ctx cancelled once the threshold of 2 is reached.
	a := &fixedAgent{name: "a", out: "yes"}
	b := &fixedAgent{name: "b", out: "yes"}
	slow := &slowCancellableAgent{name: "slow", out: "yes", delay: 5 * time.Second}

	policy := Quorum(3, 2, a, b, slow)
	primary := &fixedAgent{name: "primary", out: "ignored"}
	wrapped := policy.Wrap(primary)

	res, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "yes" {
		t.Fatalf("Output: got %q want yes", res.Output)
	}

	// Give the slow goroutine a moment to observe the cancellation.
	deadline := time.Now().Add(200 * time.Millisecond)
	for !slow.ctxCancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !slow.ctxCancelled.Load() {
		t.Fatal("slow member's ctx was not cancelled after quorum threshold was reached")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	f := &flakeyAgent{failFor: 10}
	wrapped := CircuitBreaker(3, 100*time.Millisecond).Wrap(f)
	for i := 0; i < 3; i++ {
		_, _ = wrapped.Execute(context.Background(), core.AgentTask{})
	}
	before := f.calls.Load()
	_, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err == nil || !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
	if f.calls.Load() != before {
		t.Fatal("child should not be called when circuit is open")
	}
}

func TestCircuitBreaker_RecoversAfterCooldown(t *testing.T) {
	f := &flakeyAgent{failFor: 3}
	wrapped := CircuitBreaker(3, 20*time.Millisecond).Wrap(f)
	for i := 0; i < 3; i++ {
		_, _ = wrapped.Execute(context.Background(), core.AgentTask{})
	}
	time.Sleep(40 * time.Millisecond)
	res, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err != nil || res.Output != "ok" {
		t.Fatalf("expected ok after cooldown, got %v / %q", err, res.Output)
	}
}

func TestChain_AppliesInOrder(t *testing.T) {
	// Inner first: restart 1. Outer: fallback. So: try once → fail → retry once → fail → use backup.
	f := &flakeyAgent{failFor: 5}
	backup := &fixedAgent{name: "backup", out: "rescued"}
	policy := Chain(RestartOnFail(1), Fallback(backup))
	wrapped := policy.Wrap(f)
	res, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "rescued" {
		t.Fatalf("Output: got %q want rescued", res.Output)
	}
	if got := f.calls.Load(); got != 2 {
		t.Fatalf("expected 2 flakey calls (initial + 1 restart), got %d", got)
	}
}

// slowCountingAgent counts Execute calls; the probe call (call #succeedFrom+1)
// blocks until released via a channel so concurrent callers arrive while
// probing == true and must be bounced by ErrCircuitOpen.
type slowCountingAgent struct {
	calls       atomic.Int32
	succeedFrom int32
	// releaseProbe is closed by the test to unblock the probe call.
	releaseProbe chan struct{}
}

func (c *slowCountingAgent) Name() string        { return "slow-counting" }
func (c *slowCountingAgent) Description() string { return "counts calls, probe is slow" }
func (c *slowCountingAgent) Execute(_ context.Context, _ core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	n := c.calls.Add(1)
	if n <= c.succeedFrom {
		return core.AgentResult{}, errors.New("not yet")
	}
	// Probe call: block until the test releases it so other goroutines arrive
	// while probing == true and must see ErrCircuitOpen.
	<-c.releaseProbe
	return core.AgentResult{Output: "ok"}, nil
}

func TestCircuitBreaker_SingleProberOnRecovery(t *testing.T) {
	// Open the breaker: fail 3 times (threshold = 3).
	inner := &slowCountingAgent{succeedFrom: 3, releaseProbe: make(chan struct{})}
	wrapped := CircuitBreaker(3, 20*time.Millisecond).Wrap(inner)
	for i := 0; i < 3; i++ {
		_, _ = wrapped.Execute(context.Background(), core.AgentTask{})
	}
	// Circuit is now open. Wait for cooldown to elapse.
	time.Sleep(40 * time.Millisecond)

	const N = 20
	type result struct{ err error }
	results := make(chan result, N)

	// Launch the probe goroutine first; it will block inside Execute until we
	// close releaseProbe, keeping probing == true for all subsequent callers.
	go func() {
		_, err := wrapped.Execute(context.Background(), core.AgentTask{})
		results <- result{err}
	}()

	// Give the probe goroutine time to acquire the lock and set probing = true
	// before the remaining callers start.
	time.Sleep(10 * time.Millisecond)

	// Now fire N-1 concurrent callers — they must all see ErrCircuitOpen while
	// the probe is in-flight.
	for i := 0; i < N-1; i++ {
		go func() {
			_, err := wrapped.Execute(context.Background(), core.AgentTask{})
			results <- result{err}
		}()
	}
	// Wait briefly for all N-1 callers to hit Execute, then release the probe.
	time.Sleep(10 * time.Millisecond)
	close(inner.releaseProbe)

	var (
		successes  int
		circuitErr int
	)
	for i := 0; i < N; i++ {
		r := <-results
		if r.err == nil {
			successes++
		} else if errors.Is(r.err, ErrCircuitOpen) {
			circuitErr++
		} else {
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	// Exactly one goroutine (the prober) should succeed.
	// All N-1 concurrent callers must see ErrCircuitOpen — the thundering-herd guard.
	if successes != 1 {
		t.Errorf("expected exactly 1 successful probe, got %d successes and %d ErrCircuitOpen", successes, circuitErr)
	}
	if circuitErr != N-1 {
		t.Errorf("expected %d ErrCircuitOpen, got %d", N-1, circuitErr)
	}
	// The inner agent must have been called exactly once during the probe window
	// (3 initial failures + 1 probe = 4 total).
	if got := inner.calls.Load(); got != 4 {
		t.Errorf("expected 4 calls (3 open + 1 probe), got %d", got)
	}
}

var (
	errMemberA = errors.New("member-a: unavailable")
	errMemberB = errors.New("member-b: timeout")
	errMemberC = errors.New("member-c: internal error")
)

func TestQuorum_AllErrorsReturnJoined(t *testing.T) {
	a := &fixedAgent{name: "a", err: errMemberA}
	b := &fixedAgent{name: "b", err: errMemberB}
	c := &fixedAgent{name: "c", err: errMemberC}
	policy := Quorum(3, 2, a, b, c)
	wrapped := policy.Wrap(&fixedAgent{name: "primary", out: "ignored"})

	_, err := wrapped.Execute(context.Background(), core.AgentTask{})
	if err == nil {
		t.Fatal("expected an error when all members fail")
	}
	if !errors.Is(err, errMemberA) {
		t.Errorf("expected errors.Is(err, errMemberA) to be true; err = %v", err)
	}
	if !errors.Is(err, errMemberB) {
		t.Errorf("expected errors.Is(err, errMemberB) to be true; err = %v", err)
	}
	if !errors.Is(err, errMemberC) {
		t.Errorf("expected errors.Is(err, errMemberC) to be true; err = %v", err)
	}
}

func TestNetwork_SupervisorAppliedToChildren(t *testing.T) {
	f := &flakeyAgent{failFor: 1}
	routerArgs, _ := json.Marshal(map[string]string{"task": "do"})
	router := &mockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: []core.ToolCall{{ID: "1", Name: "agent_flakey", Args: routerArgs}}},
			{Content: "done"},
		},
	}
	net := New("team", "team", router,
		WithChildren(f),
		WithSupervisor(RestartOnFail(2)),
	)
	res, err := net.Execute(context.Background(), core.AgentTask{Input: "go"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output == "" {
		t.Fatal("expected non-empty Output (Network completed after retry)")
	}
	if f.calls.Load() < 2 {
		t.Fatalf("expected at least 2 calls (1 fail + 1 retry), got %d", f.calls.Load())
	}
}
