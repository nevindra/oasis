package core

import (
	"context"
	"encoding/json"
	"testing"
)

type stubTool struct {
	name string
	hits *int
}

func (s *stubTool) Name() string             { return s.name }
func (s *stubTool) Definition() ToolDefinition {
	return ToolDefinition{Name: s.name, Description: "stub"}
}
func (s *stubTool) ExecuteRaw(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
	*s.hits++
	return ToolResult{Content: json.RawMessage(`"ok"`)}, nil
}

func incrementMiddleware(counter *int) ToolMiddleware {
	return func(t AnyTool) AnyTool {
		return &countingWrapper{inner: t, counter: counter}
	}
}

type countingWrapper struct {
	inner   AnyTool
	counter *int
}

func (c *countingWrapper) Name() string                   { return c.inner.Name() }
func (c *countingWrapper) Definition() ToolDefinition     { return c.inner.Definition() }
func (c *countingWrapper) ExecuteRaw(ctx context.Context, a json.RawMessage) (ToolResult, error) {
	*c.counter++
	return c.inner.ExecuteRaw(ctx, a)
}

func TestApplyToolMiddleware_OrderInnermostFirst(t *testing.T) {
	innerCount := 0
	outerCount := 0
	toolHits := 0

	tool := &stubTool{name: "t", hits: &toolHits}
	wrapped := ApplyToolMiddleware(tool, []ToolMiddleware{
		incrementMiddleware(&innerCount), // applied first → innermost
		incrementMiddleware(&outerCount), // applied last → outermost
	})

	_, err := wrapped.ExecuteRaw(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteRaw err = %v", err)
	}

	// Both middlewares run; the tool also runs once.
	if outerCount != 1 || innerCount != 1 || toolHits != 1 {
		t.Errorf("counters: outer=%d, inner=%d, tool=%d", outerCount, innerCount, toolHits)
	}
}

func TestApplyToolMiddleware_EmptyNoOp(t *testing.T) {
	hits := 0
	tool := &stubTool{name: "t", hits: &hits}
	got := ApplyToolMiddleware(tool, nil)
	if got != AnyTool(tool) {
		t.Errorf("ApplyToolMiddleware(nil) should return tool unchanged")
	}
}

func TestApplyToolMiddleware_NilMiddlewareSkipped(t *testing.T) {
	hits := 0
	tool := &stubTool{name: "t", hits: &hits}
	got := ApplyToolMiddleware(tool, []ToolMiddleware{nil, nil})
	if got != AnyTool(tool) {
		t.Errorf("ApplyToolMiddleware with all nils should return tool unchanged")
	}
}

func TestApplyToolMiddleware_NilReturnPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when middleware returns nil")
		}
	}()
	tool := &stubTool{name: "t", hits: new(int)}
	ApplyToolMiddleware(tool, []ToolMiddleware{
		func(AnyTool) AnyTool { return nil },
	})
}

// tagMiddleware records "<label>-pre" before and "<label>-post" after
// delegating to the inner tool.
func tagMiddleware(label string, log *[]string) ToolMiddleware {
	return func(t AnyTool) AnyTool {
		return &tagWrapper{inner: t, label: label, log: log}
	}
}

type tagWrapper struct {
	inner AnyTool
	label string
	log   *[]string
}

func (w *tagWrapper) Name() string             { return w.inner.Name() }
func (w *tagWrapper) Definition() ToolDefinition { return w.inner.Definition() }
func (w *tagWrapper) ExecuteRaw(ctx context.Context, a json.RawMessage) (ToolResult, error) {
	*w.log = append(*w.log, w.label+"-pre")
	res, err := w.inner.ExecuteRaw(ctx, a)
	*w.log = append(*w.log, w.label+"-post")
	return res, err
}

func TestChainToolMiddleware_AppliesInOrder(t *testing.T) {
	var log []string
	hits := 0
	tool := &stubTool{name: "t", hits: &hits}

	// a is outermost, b is innermost → expected: a-pre, b-pre, b-post, a-post
	wrapped := ChainToolMiddleware(
		tagMiddleware("a", &log),
		tagMiddleware("b", &log),
	)(tool)

	_, err := wrapped.ExecuteRaw(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteRaw err = %v", err)
	}

	want := []string{"a-pre", "b-pre", "b-post", "a-post"}
	if len(log) != len(want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
	for i, entry := range want {
		if log[i] != entry {
			t.Errorf("log[%d] = %q, want %q (full log: %v)", i, log[i], entry, log)
		}
	}
	if hits != 1 {
		t.Errorf("tool hits = %d, want 1", hits)
	}
}
