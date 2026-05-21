package agent

import (
	"context"
	"encoding/json"
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
