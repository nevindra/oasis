package network

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// slowAgent records when its Execute starts so the test can detect overlap.
type slowAgent struct {
	name      string
	desc      string
	startedAt time.Time
	mu        sync.Mutex
}

func (s *slowAgent) Name() string        { return s.name }
func (s *slowAgent) Description() string { return s.desc }
func (s *slowAgent) Execute(ctx context.Context, _ core.AgentTask, opts ...core.RunOption) (core.AgentResult, error) {
	s.mu.Lock()
	s.startedAt = time.Now()
	s.mu.Unlock()

	rcfg := core.ApplyRunOptions(opts...)
	if rcfg.Stream != nil {
		defer close(rcfg.Stream)
	}

	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
	}
	return core.AgentResult{Output: s.name}, nil
}

// fakeParallelRouter returns a mockProvider that emits a single response with
// two agent_* tool calls (one per name), then a finish message.
func fakeParallelRouter(t *testing.T, names ...string) *mockProvider {
	t.Helper()
	calls := make([]core.ToolCall, len(names))
	for i, name := range names {
		args, _ := json.Marshal(map[string]string{"task": "do " + name})
		calls[i] = core.ToolCall{ID: name, Name: "agent_" + name, Args: args}
	}
	return &mockProvider{
		name: "router",
		responses: []core.ChatResponse{
			{ToolCalls: calls},
			{Content: "done"},
		},
	}
}

func TestParallelDispatch_DefaultIsParallel(t *testing.T) {
	a := &slowAgent{name: "a", desc: "agent a"}
	b := &slowAgent{name: "b", desc: "agent b"}
	router := fakeParallelRouter(t, "a", "b")
	net := New("team", "team", router, a, b)
	if _, err := net.Execute(context.Background(), core.AgentTask{Input: "go"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	a.mu.Lock()
	aStart := a.startedAt
	a.mu.Unlock()
	b.mu.Lock()
	bStart := b.startedAt
	b.mu.Unlock()
	gap := bStart.Sub(aStart)
	if gap < 0 {
		gap = -gap
	}
	if gap > 30*time.Millisecond {
		t.Fatalf("default should be parallel; gap between starts was %v (want <30ms)", gap)
	}
}

func TestParallelDispatch_DisabledIsSequential(t *testing.T) {
	a := &slowAgent{name: "a", desc: "agent a"}
	b := &slowAgent{name: "b", desc: "agent b"}
	router := fakeParallelRouter(t, "a", "b")
	net := NewWithOptions("team", "team", router, []core.Agent{a, b},
		WithParallelDispatch(ParallelDisabled),
	)
	if _, err := net.Execute(context.Background(), core.AgentTask{Input: "go"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	a.mu.Lock()
	aStart := a.startedAt
	a.mu.Unlock()
	b.mu.Lock()
	bStart := b.startedAt
	b.mu.Unlock()
	gap := bStart.Sub(aStart)
	if gap < 0 {
		gap = -gap
	}
	if gap < 40*time.Millisecond {
		t.Fatalf("ParallelDisabled should be sequential; gap was %v (want ≥40ms)", gap)
	}
}
