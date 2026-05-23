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
	net := NewWithOptions("team", "team", router, []core.Agent{f},
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
