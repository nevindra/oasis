package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

type recordingTool struct {
	called *bool
}

func (r *recordingTool) Name() string { return "rec" }
func (r *recordingTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "rec", Description: "test tool"}
}
func (r *recordingTool) ExecuteRaw(ctx context.Context, _ json.RawMessage) (core.ToolResult, error) {
	*r.called = true
	return core.ToolResult{Content: json.RawMessage(`"ok"`)}, nil
}

type mwWrapper struct {
	inner core.AnyTool
	hit   *bool
}

func (w *mwWrapper) Name() string                    { return w.inner.Name() }
func (w *mwWrapper) Definition() core.ToolDefinition { return w.inner.Definition() }
func (w *mwWrapper) ExecuteRaw(ctx context.Context, a json.RawMessage) (core.ToolResult, error) {
	*w.hit = true
	return w.inner.ExecuteRaw(ctx, a)
}

func TestWithToolMiddleware_WrapsRegisteredTools(t *testing.T) {
	called := false
	wrapperCalled := false

	mw := func(inner core.AnyTool) core.AnyTool {
		return &mwWrapper{inner: inner, hit: &wrapperCalled}
	}

	ag := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: &called}),
		WithToolMiddleware(mw),
	)

	// Execute the registered tool via the registry — same code path the
	// loop uses (a.tools.Execute -> ToolRegistry.Execute -> wrapped tool).
	result, err := ag.tools.Execute(context.Background(), "rec", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("tools.Execute err = %v", err)
	}
	if !called {
		t.Error("recordingTool not called via registry")
	}
	if !wrapperCalled {
		t.Error("middleware wrapper not invoked")
	}
	if string(result.Content) != `"ok"` {
		t.Errorf("result.Content = %s, want \"ok\"", result.Content)
	}
}

func TestWithToolMiddleware_NoMiddlewareIsNoop(t *testing.T) {
	called := false
	ag := NewLLMAgent("test", "", &callbackProvider{},
		WithTools(&recordingTool{called: &called}),
	)
	_, execErr := ag.tools.Execute(context.Background(), "rec", json.RawMessage(`{}`))
	if execErr != nil {
		t.Fatalf("tools.Execute err = %v", execErr)
	}
	if !called {
		t.Error("recordingTool not called")
	}
}

func TestLoggingMiddleware_LogsStartAndFinish(t *testing.T) {
	var buf testLogBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	tool := core.ApplyToolMiddleware(
		&recordingTool{called: new(bool)},
		[]core.ToolMiddleware{LoggingMiddleware(logger)},
	)

	_, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw err = %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "tool.start") || !strings.Contains(out, "tool.finish") {
		t.Errorf("expected start+finish in log output, got: %s", out)
	}
}

func TestTransformMiddleware_MutatesResult(t *testing.T) {
	tool := core.ApplyToolMiddleware(
		&recordingTool{called: new(bool)},
		[]core.ToolMiddleware{
			TransformMiddleware(func(name string, r core.ToolResult) core.ToolResult {
				return core.ToolResult{Content: json.RawMessage(`"transformed"`)}
			}),
		},
	)

	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw err = %v", err)
	}
	if got := string(result.Content); got != `"transformed"` {
		t.Errorf("Content = %s, want \"transformed\"", got)
	}
}

func TestTimingMiddleware_PassesThrough(t *testing.T) {
	called := false
	tool := core.ApplyToolMiddleware(
		&recordingTool{called: &called},
		[]core.ToolMiddleware{TimingMiddleware()},
	)
	_, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw err = %v", err)
	}
	if !called {
		t.Error("inner tool not called")
	}
}

// testLogBuffer is a thread-safe buffer for slog output capture.
type testLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *testLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *testLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
