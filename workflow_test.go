package oasis

import (
	"context"
	"encoding/json"
	"testing"
)

// --- WorkflowContext tests ---

func TestWorkflowContextGetSet(t *testing.T) {
	ctx := newWorkflowContext(AgentTask{Input: "hello"})

	if ctx.Input() != "hello" {
		t.Errorf("Input() = %q, want %q", ctx.Input(), "hello")
	}

	// Get on missing key.
	v, ok := ctx.Get("missing")
	if ok || v != nil {
		t.Errorf("Get(missing) = (%v, %v), want (nil, false)", v, ok)
	}

	// Set and Get.
	ctx.Set("key", "value")
	v, ok = ctx.Get("key")
	if !ok || v != "value" {
		t.Errorf("Get(key) = (%v, %v), want (value, true)", v, ok)
	}

	// Overwrite.
	ctx.Set("key", 42)
	v, _ = ctx.Get("key")
	if v != 42 {
		t.Errorf("Get(key) after overwrite = %v, want 42", v)
	}
}

func TestWorkflowContextAddUsage(t *testing.T) {
	ctx := newWorkflowContext(AgentTask{})
	ctx.addUsage(Usage{InputTokens: 10, OutputTokens: 5})
	ctx.addUsage(Usage{InputTokens: 20, OutputTokens: 15})

	v, ok := ctx.Get("_usage")
	if !ok {
		t.Fatal("expected _usage in context")
	}
	u := v.(Usage)
	if u.InputTokens != 30 || u.OutputTokens != 20 {
		t.Errorf("usage = %+v, want {InputTokens:30 OutputTokens:20}", u)
	}
}

// --- NewWorkflow validation tests ---

func TestNewWorkflowDuplicateStep(t *testing.T) {
	_, err := NewWorkflow("test", "test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
	)
	if err == nil {
		t.Fatal("expected error for duplicate step name")
	}
	if want := `workflow test: duplicate step name "a"`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestNewWorkflowUnknownDependency(t *testing.T) {
	_, err := NewWorkflow("test", "test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("b", func(_ context.Context, _ *WorkflowContext) error { return nil }, After("c")),
	)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
	if want := `workflow test: step "b" depends on unknown step "c"`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestNewWorkflowCycleDetection(t *testing.T) {
	_, err := NewWorkflow("test", "test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }, After("b")),
		Step("b", func(_ context.Context, _ *WorkflowContext) error { return nil }, After("a")),
	)
	if err == nil {
		t.Fatal("expected error for cycle")
	}
	if want := "workflow test: cycle detected in step dependencies"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestNewWorkflowThreeNodeCycle(t *testing.T) {
	noop := func(_ context.Context, _ *WorkflowContext) error { return nil }
	_, err := NewWorkflow("test", "test",
		Step("a", noop, After("c")),
		Step("b", noop, After("a")),
		Step("c", noop, After("b")),
	)
	if err == nil {
		t.Fatal("expected error for 3-node cycle")
	}
}

func TestNewWorkflowValidGraph(t *testing.T) {
	noop := func(_ context.Context, _ *WorkflowContext) error { return nil }
	wf, err := NewWorkflow("test", "test",
		Step("a", noop),
		Step("b", noop, After("a")),
		Step("c", noop, After("a")),
		Step("d", noop, After("b", "c")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Name() != "test" {
		t.Errorf("Name() = %q, want %q", wf.Name(), "test")
	}
	if wf.Description() != "test" {
		t.Errorf("Description() = %q, want %q", wf.Description(), "test")
	}
}

// --- Agent interface compliance ---

func TestWorkflowImplementsAgent(t *testing.T) {
	wf, err := NewWorkflow("test", "test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	var _ Agent = wf
}

// --- Input propagation ---

func TestWorkflowInputPropagation(t *testing.T) {
	wf, err := NewWorkflow("input", "input test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "input was: "+wCtx.Input())
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "input was: hello world" {
		t.Errorf("Output = %q, want %q", result.Output, "input was: hello world")
	}
}

// --- Empty workflow ---

func TestWorkflowEmptySteps(t *testing.T) {
	wf, err := NewWorkflow("empty", "empty workflow")
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "" {
		t.Errorf("Output = %q, want empty", result.Output)
	}
}

// --- Resolve tests ---

func TestWorkflowContextResolve(t *testing.T) {
	tests := []struct {
		name     string
		template string
		values   map[string]any
		want     string
	}{
		{"no placeholders", "hello world", nil, "hello world"},
		{"single placeholder", "hello {{name}}", map[string]any{"name": "Alice"}, "hello Alice"},
		{"multiple placeholders", "{{a}} and {{b}}", map[string]any{"a": "X", "b": "Y"}, "X and Y"},
		{"missing key", "hello {{unknown}}", nil, "hello "},
		{"numeric value", "count: {{n}}", map[string]any{"n": 42}, "count: 42"},
		{"empty template", "", nil, ""},
		{"adjacent placeholders", "{{a}}{{b}}", map[string]any{"a": "1", "b": "2"}, "12"},
		{"unclosed brace", "hello {{name", nil, "hello {{name"},
		{"whitespace in key", "{{ name }}", map[string]any{"name": "Bob"}, "Bob"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wCtx := newWorkflowContext(AgentTask{})
			for k, v := range tt.values {
				wCtx.Set(k, v)
			}
			got := wCtx.Resolve(tt.template)
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}

func TestWorkflowContextResolveJSON(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{})
	wCtx.Set("name", "Alice")
	wCtx.Set("data", map[string]any{"x": 1, "y": 2})

	// Single placeholder with string value -> JSON string.
	got := string(wCtx.ResolveJSON("{{name}}"))
	if got != `"Alice"` {
		t.Errorf("ResolveJSON(string) = %s, want %q", got, `"Alice"`)
	}

	// Single placeholder with structured value -> JSON object.
	got = string(wCtx.ResolveJSON("{{data}}"))
	if got != `{"x":1,"y":2}` {
		t.Errorf("ResolveJSON(map) = %s, want %s", got, `{"x":1,"y":2}`)
	}

	// Mixed text -> JSON string.
	got = string(wCtx.ResolveJSON("hello {{name}}"))
	if got != `"hello Alice"` {
		t.Errorf("ResolveJSON(mixed) = %s, want %q", got, `"hello Alice"`)
	}

	// Missing key -> null.
	got = string(wCtx.ResolveJSON("{{missing}}"))
	if got != "null" {
		t.Errorf("ResolveJSON(missing) = %s, want null", got)
	}
}

// --- stringifyValue tests ---

func TestStringifyValue(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want string
	}{
		{"string", "hello", "hello"},
		{"int", 42, "42"},
		{"float", 3.14, "3.14"},
		{"bool", true, "true"},
		{"nil", nil, "<nil>"},
		{"slice", []int{1, 2}, "[1 2]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringifyValue(tt.val)
			if got != tt.want {
				t.Errorf("stringifyValue(%v) = %q, want %q", tt.val, got, tt.want)
			}
		})
	}
}

// --- newWorkflowContext tests ---

func TestNewWorkflowContextSetsInput(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{Input: "test input"})

	if wCtx.Input() != "test input" {
		t.Errorf("Input() = %q, want %q", wCtx.Input(), "test input")
	}

	// Input should also be available as "input" key in values.
	v, ok := wCtx.Get("input")
	if !ok || v != "test input" {
		t.Errorf("Get(\"input\") = (%v, %v), want (%q, true)", v, ok, "test input")
	}
}

func TestNewWorkflowContextEmptyInput(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{})

	if wCtx.Input() != "" {
		t.Errorf("Input() = %q, want empty", wCtx.Input())
	}

	// Empty input should NOT be stored in values.
	_, ok := wCtx.Get("input")
	if ok {
		t.Error("empty input should not be stored in values map")
	}
}

// --- ResolveJSON edge case ---

func TestResolveJSONMarshalError(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{})
	// json.Marshal cannot marshal channels.
	wCtx.Set("bad", make(chan int))

	got := string(wCtx.ResolveJSON("{{bad}}"))
	if got != "null" {
		t.Errorf("ResolveJSON(unmarshalable) = %s, want null", got)
	}
}

// --- WorkflowError tests ---

func TestWorkflowErrorFormat(t *testing.T) {
	err := &WorkflowError{
		StepName: "fetch",
		Err:      json.Unmarshal([]byte("not json"), nil),
	}

	got := err.Error()
	if got == "" {
		t.Fatal("expected non-empty error string")
	}
	want := `workflow step "fetch" failed:`
	if len(got) < len(want) || got[:len(want)] != want {
		t.Errorf("Error() = %q, want prefix %q", got, want)
	}
}

func TestWorkflowErrorUnwrap(t *testing.T) {
	inner := context.DeadlineExceeded
	err := &WorkflowError{StepName: "a", Err: inner}

	if err.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), inner)
	}
}
