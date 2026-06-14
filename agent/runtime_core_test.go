package agent

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/internal/runtime"
	"github.com/nevindra/oasis/memory"
)

// Tests that exercise runtime.Runtime (formerly AgentCore) behavior directly.
// Kept in package agent so they can access BuildConfig, WithPrompt, etc.

func TestInitCoreWiresAllFields(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithPrompt("test prompt"),
		WithLimits(Limits{MaxIter: 42}),
		WithGeneration(Generation{Temperature: ptrA(0.7)}),
	})

	var c runtime.Runtime
	p := &mockProvider{name: "test"}
	runtime.Init(&c, "myagent", "does stuff", p, cfg)

	if c.Name() != "myagent" {
		t.Errorf("name = %q, want %q", c.Name(), "myagent")
	}
	if c.Description() != "does stuff" {
		t.Errorf("description = %q, want %q", c.Description(), "does stuff")
	}
	// provider is unexported; verify via ResolvePromptAndProvider which uses it.
	_, resolvedProvider := c.ResolvePromptAndProvider(context.Background(), AgentTask{})
	if resolvedProvider != p {
		t.Error("provider not wired")
	}
	if c.SystemPrompt != "test prompt" {
		t.Errorf("systemPrompt = %q, want %q", c.SystemPrompt, "test prompt")
	}
	if c.MaxIter != 42 {
		t.Errorf("maxIter = %d, want 42", c.MaxIter)
	}
	if c.GenParams == nil || c.GenParams.Temperature == nil || *c.GenParams.Temperature != 0.7 {
		t.Error("generationParams.Temperature not wired")
	}
	if c.Tools() == nil {
		t.Error("tools registry not initialized")
	}
	// processors is unexported; verify it is initialized via a no-op check.
	// (Direct field access removed — processors is runtime-only internal state.)
}

func TestInitCoreDefaultMaxIter(t *testing.T) {
	cfg := BuildConfig(nil)
	var c runtime.Runtime
	runtime.Init(&c, "a", "d", &mockProvider{name: "p"}, cfg)

	if c.MaxIter != 25 { // defaultMaxIter = 25
		t.Errorf("maxIter = %d, want default 25", c.MaxIter)
	}
}

func TestDefaultMaxIterIs25(t *testing.T) {
	cfg := BuildConfig(nil)
	var c runtime.Runtime
	runtime.Init(&c, "t", "", &mockProvider{name: "p"}, cfg)
	if c.MaxIter != 25 {
		t.Errorf("expected defaultMaxIter 25, got %d", c.MaxIter)
	}
}

func TestInitCoreMemoryFieldsWired(t *testing.T) {
	// Verifies that memory options wire through runtime.Init without panicking.
	// Deep field verification is done via integration tests in memory_test.go.
	store := &stubStore{}
	cfg := BuildConfig([]AgentOption{
		WithMemory(memory.WithStore(store), memory.WithHistory(memory.HistoryConfig{MaxMessages: 25, MaxTokens: 5000})),
	})

	var c runtime.Runtime
	// Should not panic.
	runtime.Init(&c, "a", "d", &mockProvider{name: "p"}, cfg)
	// Close should be safe after Init (even without any executions).
	if err := c.Memory().Close(); err != nil {
		t.Errorf("mem.Close error: %v", err)
	}
}

func TestRuntimeNameDescriptionClose(t *testing.T) {
	var c runtime.Runtime
	runtime.Init(&c, "core", "core desc", &mockProvider{name: "p"}, BuildConfig(nil))

	if c.Name() != "core" {
		t.Errorf("Name() = %q, want %q", c.Name(), "core")
	}
	if c.Description() != "core desc" {
		t.Errorf("Description() = %q, want %q", c.Description(), "core desc")
	}
	// Close should not panic on zero-state memory.
	if err := c.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
}

func TestCacheBuiltinToolDefs(t *testing.T) {
	var c runtime.Runtime
	runtime.Init(&c, "a", "d", &mockProvider{name: "p"}, BuildConfig(nil))

	// No builtins configured: should return input unchanged.
	defs := c.CacheBuiltinToolDefs(nil, nil, nil)
	if len(defs) != 0 {
		t.Errorf("got %d defs, want 0", len(defs))
	}

	// With all builtins.
	c.InputHandler = &mockInputHandler{response: InputResponse{Value: "ok"}}
	c.PlanExecution = true
	askDef := core.ToolDefinition{Name: "ask_user"}
	planDef := core.ToolDefinition{Name: "execute_plan"}
	defs = c.CacheBuiltinToolDefs([]core.ToolDefinition{{Name: "existing"}}, &askDef, &planDef)
	if len(defs) != 3 { // existing + ask_user + execute_plan
		t.Errorf("got %d defs, want 3", len(defs))
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"existing", "ask_user", "execute_plan"} {
		if !names[want] {
			t.Errorf("missing tool def %q", want)
		}
	}
}

func TestResolvePromptAndProvider(t *testing.T) {
	base := &mockProvider{name: "base"}
	override := &mockProvider{name: "override"}

	var c runtime.Runtime
	runtime.Init(&c, "a", "d", base, BuildConfig([]AgentOption{
		WithPrompt("static prompt"),
	}))

	task := AgentTask{Input: "test"}

	// Static path.
	prompt, prov := c.ResolvePromptAndProvider(context.Background(), task)
	if prompt != "static prompt" {
		t.Errorf("prompt = %q, want %q", prompt, "static prompt")
	}
	if prov != base {
		t.Error("provider should be base")
	}

	// Dynamic overrides.
	c.DynamicPrompt = func(_ context.Context, _ AgentTask) string { return "dynamic prompt" }
	c.DynamicModel = func(_ context.Context, _ AgentTask) core.Provider { return override }

	prompt, prov = c.ResolvePromptAndProvider(context.Background(), task)
	if prompt != "dynamic prompt" {
		t.Errorf("prompt = %q, want %q", prompt, "dynamic prompt")
	}
	if prov != override {
		t.Error("provider should be override")
	}
}

func TestResolveDynamicToolsNil(t *testing.T) {
	var c runtime.Runtime
	runtime.Init(&c, "a", "d", &mockProvider{name: "p"}, BuildConfig(nil))

	defs, exec, execStream := c.ResolveDynamicTools(context.Background(), AgentTask{})
	if defs != nil || exec != nil || execStream != nil {
		t.Error("expected nil when dynamicTools not set")
	}
}
