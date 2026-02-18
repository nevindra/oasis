package oasis

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockAgent is a test Agent with configurable behavior.
type mockAgent struct {
	name   string
	desc   string
	result AgentResult
	err    error
	delay  time.Duration // simulate work
}

func (m *mockAgent) Name() string        { return m.name }
func (m *mockAgent) Description() string { return m.desc }
func (m *mockAgent) Execute(ctx context.Context, _ AgentTask) (AgentResult, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}
	return m.result, m.err
}

func TestSpawnSuccess(t *testing.T) {
	want := AgentResult{Output: "done", Usage: Usage{InputTokens: 10, OutputTokens: 5}}
	agent := &mockAgent{name: "test", result: want}

	h := Spawn(context.Background(), agent, AgentTask{Input: "go"})

	result, err := h.Await(context.Background())
	if err != nil {
		t.Fatalf("Await returned unexpected error: %v", err)
	}
	if result.Output != want.Output {
		t.Errorf("Output = %q, want %q", result.Output, want.Output)
	}
	if result.Usage != want.Usage {
		t.Errorf("Usage = %+v, want %+v", result.Usage, want.Usage)
	}
	if h.State() != StateCompleted {
		t.Errorf("State = %v, want %v", h.State(), StateCompleted)
	}
}

func TestSpawnFailure(t *testing.T) {
	wantErr := errors.New("agent failed")
	agent := &mockAgent{name: "test", err: wantErr}

	h := Spawn(context.Background(), agent, AgentTask{Input: "go"})

	_, err := h.Await(context.Background())
	if !errors.Is(err, wantErr) {
		t.Errorf("Await error = %v, want %v", err, wantErr)
	}
	if h.State() != StateFailed {
		t.Errorf("State = %v, want %v", h.State(), StateFailed)
	}
}

func TestSpawnCancel(t *testing.T) {
	agent := &mockAgent{name: "slow", delay: 5 * time.Second}

	h := Spawn(context.Background(), agent, AgentTask{Input: "go"})

	// Wait a moment for the goroutine to start.
	time.Sleep(10 * time.Millisecond)
	if h.State() != StateRunning {
		t.Errorf("State before cancel = %v, want %v", h.State(), StateRunning)
	}

	h.Cancel()

	_, err := h.Await(context.Background())
	if err == nil {
		t.Fatal("Await should return error after cancel")
	}
	if h.State() != StateCancelled {
		t.Errorf("State = %v, want %v", h.State(), StateCancelled)
	}
}

func TestSpawnParentContextCancel(t *testing.T) {
	agent := &mockAgent{name: "slow", delay: 5 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	h := Spawn(ctx, agent, AgentTask{Input: "go"})

	time.Sleep(10 * time.Millisecond)
	cancel() // cancel parent context

	<-h.Done()
	if h.State() != StateCancelled {
		t.Errorf("State = %v, want %v", h.State(), StateCancelled)
	}
}

func TestSpawnAwaitContextCancel(t *testing.T) {
	agent := &mockAgent{name: "slow", delay: 5 * time.Second}

	h := Spawn(context.Background(), agent, AgentTask{Input: "go"})

	// Await with a context that gets cancelled quickly.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := h.Await(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Await error = %v, want context.DeadlineExceeded", err)
	}

	// Agent is still running (we only cancelled the await context, not the agent).
	if h.State() != StateRunning {
		t.Errorf("State = %v, want %v (agent still running)", h.State(), StateRunning)
	}

	// Clean up.
	h.Cancel()
	<-h.Done()
}

func TestSpawnDoneChannel(t *testing.T) {
	agent := &mockAgent{name: "fast", result: AgentResult{Output: "ok"}}

	h := Spawn(context.Background(), agent, AgentTask{Input: "go"})

	select {
	case <-h.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("Done channel not closed after completion")
	}

	result, err := h.Result()
	if err != nil {
		t.Fatalf("Result returned unexpected error: %v", err)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

func TestSpawnResultBeforeCompletion(t *testing.T) {
	agent := &mockAgent{name: "slow", delay: 5 * time.Second}

	h := Spawn(context.Background(), agent, AgentTask{Input: "go"})
	defer h.Cancel()

	time.Sleep(10 * time.Millisecond)

	result, err := h.Result()
	if err != nil {
		t.Errorf("Result before completion should return nil error, got %v", err)
	}
	if result.Output != "" {
		t.Errorf("Result before completion should return zero AgentResult, got %+v", result)
	}
}

func TestSpawnID(t *testing.T) {
	agent := &mockAgent{name: "test", result: AgentResult{Output: "ok"}}

	h1 := Spawn(context.Background(), agent, AgentTask{Input: "a"})
	h2 := Spawn(context.Background(), agent, AgentTask{Input: "b"})
	defer func() { <-h1.Done(); <-h2.Done() }()

	if h1.ID() == "" {
		t.Error("ID should not be empty")
	}
	if h1.ID() == h2.ID() {
		t.Errorf("IDs should be unique, got %q for both", h1.ID())
	}
}

func TestSpawnAgent(t *testing.T) {
	agent := &mockAgent{name: "test", result: AgentResult{Output: "ok"}}

	h := Spawn(context.Background(), agent, AgentTask{Input: "go"})
	<-h.Done()

	if h.Agent().Name() != "test" {
		t.Errorf("Agent().Name() = %q, want %q", h.Agent().Name(), "test")
	}
}

func TestSpawnMultiplexSelect(t *testing.T) {
	fast := &mockAgent{name: "fast", result: AgentResult{Output: "fast-result"}, delay: 10 * time.Millisecond}
	slow := &mockAgent{name: "slow", result: AgentResult{Output: "slow-result"}, delay: 5 * time.Second}

	h1 := Spawn(context.Background(), fast, AgentTask{Input: "go"})
	h2 := Spawn(context.Background(), slow, AgentTask{Input: "go"})
	defer h2.Cancel()

	select {
	case <-h1.Done():
		result, _ := h1.Result()
		if result.Output != "fast-result" {
			t.Errorf("Output = %q, want %q", result.Output, "fast-result")
		}
	case <-h2.Done():
		t.Fatal("slow agent should not finish first")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fast agent")
	}

	<-h2.Done()
}

func TestAgentStateString(t *testing.T) {
	tests := []struct {
		state AgentState
		want  string
	}{
		{StatePending, "pending"},
		{StateRunning, "running"},
		{StateCompleted, "completed"},
		{StateFailed, "failed"},
		{StateCancelled, "cancelled"},
		{AgentState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("AgentState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestAgentStateIsTerminal(t *testing.T) {
	tests := []struct {
		state    AgentState
		terminal bool
	}{
		{StatePending, false},
		{StateRunning, false},
		{StateCompleted, true},
		{StateFailed, true},
		{StateCancelled, true},
	}
	for _, tt := range tests {
		if got := tt.state.IsTerminal(); got != tt.terminal {
			t.Errorf("AgentState(%d).IsTerminal() = %v, want %v", tt.state, got, tt.terminal)
		}
	}
}
