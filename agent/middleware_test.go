package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/nevindra/oasis/core"
)

// wrapAgent returns a core.Agent whose Execute calls fn instead of inner.Execute.
// Used in tests to build lightweight wrappers that track invocation order.
func wrapAgent(inner core.Agent, fn func(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error)) core.Agent {
	return &wrappedAgentStub{inner: inner, fn: fn}
}

type wrappedAgentStub struct {
	inner core.Agent
	fn    func(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error)
}

func (f *wrappedAgentStub) Name() string        { return f.inner.Name() }
func (f *wrappedAgentStub) Description() string { return f.inner.Description() }
func (f *wrappedAgentStub) Execute(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error) {
	return f.fn(ctx, task, opts...)
}

func TestChain_AgentMiddlewaresApplyInOrder(t *testing.T) {
	var order []string

	makeMiddleware := func(name string) Middleware {
		return func(inner core.Agent) core.Agent {
			return wrapAgent(inner, func(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error) {
				order = append(order, name+"-pre")
				res, err := inner.Execute(ctx, task, opts...)
				order = append(order, name+"-post")
				return res, err
			})
		}
	}

	base := &stubAgent{
		name: "base",
		desc: "base agent",
		fn: func(task AgentTask) (AgentResult, error) {
			order = append(order, "base")
			return AgentResult{Output: "done"}, nil
		},
	}

	// Chain(a, b)(base) → a wraps b wraps base.
	// Call-time order: a-pre → b-pre → base → b-post → a-post.
	chained := Chain(makeMiddleware("a"), makeMiddleware("b"))(base)

	_, err := chained.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"a-pre", "b-pre", "base", "b-post", "a-post"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i, v := range want {
		if order[i] != v {
			t.Errorf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestChain_NilMiddlewareSkipped(t *testing.T) {
	base := &stubAgent{
		name: "base",
		desc: "base agent",
		fn:   func(task AgentTask) (AgentResult, error) { return AgentResult{Output: "ok"}, nil },
	}

	// nil entries in Chain must be silently skipped.
	result, err := Chain(nil, nil)(base).Execute(context.Background(), AgentTask{Input: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

func TestChain_NoMiddlewaresIsIdentity(t *testing.T) {
	base := &stubAgent{
		name: "base",
		desc: "base agent",
		fn:   func(task AgentTask) (AgentResult, error) { return AgentResult{Output: "identity"}, nil },
	}

	result, err := Chain()(base).Execute(context.Background(), AgentTask{Input: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "identity" {
		t.Errorf("Output = %q, want %q", result.Output, "identity")
	}
}

func TestWithMiddleware_AppliedToAgent(t *testing.T) {
	var calls atomic.Int32

	counter := Middleware(func(inner core.Agent) core.Agent {
		return wrapAgent(inner, func(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error) {
			calls.Add(1)
			return inner.Execute(ctx, task, opts...)
		})
	})

	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{Content: "hello"},
		},
	}

	a := New("mw-agent", "middleware test agent", provider,
		WithMiddleware(counter),
	)

	_, err := a.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 1 {
		t.Errorf("middleware called %d times, want 1", calls.Load())
	}
}

func TestWithMiddleware_CalledOnEachExecute(t *testing.T) {
	var calls atomic.Int32

	counter := Middleware(func(inner core.Agent) core.Agent {
		return wrapAgent(inner, func(ctx context.Context, task AgentTask, opts ...core.RunOption) (AgentResult, error) {
			calls.Add(1)
			return inner.Execute(ctx, task, opts...)
		})
	})

	provider := &mockProvider{
		name: "test",
		responses: []core.ChatResponse{
			{Content: "first"},
			{Content: "second"},
		},
	}

	a := New("mw-agent2", "middleware test agent 2", provider,
		WithMiddleware(counter),
	)

	for i := 0; i < 2; i++ {
		_, err := a.Execute(context.Background(), AgentTask{Input: "call"})
		if err != nil {
			t.Fatalf("Execute #%d: %v", i, err)
		}
	}

	// Middleware must fire once per Execute call.
	if calls.Load() != 2 {
		t.Errorf("middleware called %d times across 2 Execute calls, want 2", calls.Load())
	}
}
