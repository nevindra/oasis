package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// --- AnyTool interface compliance ---

// stubAnyTool is a minimal AnyTool implementation for interface compliance testing.
type stubAnyTool struct {
	name string
}

func (s *stubAnyTool) Name() string              { return s.name }
func (s *stubAnyTool) Definition() ToolDefinition { return ToolDefinition{Name: s.name} }
func (s *stubAnyTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "ok"}, nil
}

func TestAnyTool_InterfaceCompliance(t *testing.T) {
	var _ AnyTool = (*stubAnyTool)(nil) // compile-time check
	tool := &stubAnyTool{name: "stub"}
	if tool.Name() != "stub" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "stub")
	}
	res, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw error: %v", err)
	}
	if res.Content != "ok" {
		t.Errorf("Content = %q, want %q", res.Content, "ok")
	}
}

// --- Tool[In, Out] + Erase ---

type echoInput struct {
	Message string `json:"message" describe:"text to echo back"`
}

type echoOutput struct {
	Echoed string `json:"echoed"`
}

type echoTool struct {
	failOnExecute bool
}

func (e *echoTool) Definition() ToolMeta {
	return ToolMeta{
		Name:        "echo",
		Description: "echoes its input",
	}
}

func (e *echoTool) Execute(_ context.Context, in echoInput) (echoOutput, error) {
	if e.failOnExecute {
		return echoOutput{}, errors.New("execute failed: " + in.Message)
	}
	return echoOutput{Echoed: in.Message}, nil
}

func TestTool_InterfaceCompliance(t *testing.T) {
	var _ Tool[echoInput, echoOutput] = (*echoTool)(nil) // compile-time check
	tool := &echoTool{}
	out, err := tool.Execute(context.Background(), echoInput{Message: "hi"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Echoed != "hi" {
		t.Errorf("Echoed = %q, want %q", out.Echoed, "hi")
	}
}

// TestErase_RoundTrip verifies that erasing a Tool[In, Out] preserves Name,
// Definition, and ExecuteRaw round-trips through JSON.
func TestErase_RoundTrip(t *testing.T) {
	erased := Erase[echoInput, echoOutput](&echoTool{})
	if erased.Name() != "echo" {
		t.Errorf("Name() = %q", erased.Name())
	}
	if erased.Definition().Name != "echo" {
		t.Errorf("Definition.Name = %q", erased.Definition().Name)
	}

	// Phase 1.5: Erase derives the input schema; verify it's wired through.
	def := erased.Definition()
	if len(def.Parameters) == 0 {
		t.Errorf("Definition().Parameters is empty — DeriveSchema should populate it")
	}
	var schema map[string]interface{}
	if err := json.Unmarshal(def.Parameters, &schema); err != nil {
		t.Fatalf("derived schema not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]interface{})
	if _, ok := props["message"]; !ok {
		t.Errorf("derived schema missing properties.message")
	}

	args, _ := json.Marshal(echoInput{Message: "hello"})
	res, err := erased.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("ExecuteRaw Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("ToolResult.Error: %q", res.Error)
	}
	var got echoOutput
	if err := json.Unmarshal([]byte(res.Content), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got.Echoed != "hello" {
		t.Errorf("Echoed = %q, want %q", got.Echoed, "hello")
	}
}

// TestErase_BadArgsLandInToolResult verifies that unmarshal errors are
// surfaced via ToolResult.Error rather than as Go errors — preserving the
// AnyTool contract that Go errors signal infrastructure failures only.
func TestErase_BadArgsLandInToolResult(t *testing.T) {
	erased := Erase[echoInput, echoOutput](&echoTool{})
	res, err := erased.ExecuteRaw(context.Background(), json.RawMessage(`{not json}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error == "" {
		t.Error("expected ToolResult.Error to be set on invalid JSON")
	}
	if !strings.Contains(res.Error, "invalid args") {
		t.Errorf("Error = %q, want it to contain 'invalid args'", res.Error)
	}
}

// TestErase_ExecuteErrorLandsInToolResult verifies that an error returned
// from the typed Execute method lands in ToolResult.Error, not as a Go error.
func TestErase_ExecuteErrorLandsInToolResult(t *testing.T) {
	erased := Erase[echoInput, echoOutput](&echoTool{failOnExecute: true})
	args, _ := json.Marshal(echoInput{Message: "boom"})
	res, err := erased.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error == "" {
		t.Error("expected ToolResult.Error to be set")
	}
	if !strings.Contains(res.Error, "execute failed") {
		t.Errorf("Error = %q, want it to contain 'execute failed'", res.Error)
	}
}

// TestErase_EmptyArgs verifies that an empty args payload is accepted (input
// gets zero value) instead of erroring.
func TestErase_EmptyArgs(t *testing.T) {
	erased := Erase[echoInput, echoOutput](&echoTool{})
	res, err := erased.ExecuteRaw(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteRaw Go error: %v", err)
	}
	if res.Error != "" {
		t.Errorf("expected no error for empty args, got %q", res.Error)
	}
}

// streamingFooIn is a test input.
type streamingFooIn struct {
	Query string `json:"query" describe:"search query"`
}

// streamingFooOut is a test output.
type streamingFooOut struct {
	Hits int `json:"hits"`
}

// streamingFooTool implements StreamingTool[streamingFooIn, streamingFooOut].
type streamingFooTool struct{}

func (streamingFooTool) Definition() ToolMeta {
	return ToolMeta{Name: "foo", Description: "streaming search"}
}
func (streamingFooTool) Execute(ctx context.Context, in streamingFooIn) (streamingFooOut, error) {
	return streamingFooOut{Hits: len(in.Query)}, nil
}
func (streamingFooTool) ExecuteStream(ctx context.Context, in streamingFooIn, ch chan<- StreamEvent) (streamingFooOut, error) {
	ch <- StreamEvent{Type: EventToolProgress, Content: "searching"}
	return streamingFooOut{Hits: len(in.Query)}, nil
}

func TestEraseStreaming(t *testing.T) {
	tool := streamingFooTool{}
	erased := EraseStreaming[streamingFooIn, streamingFooOut](tool)

	// AnyTool contract.
	if erased.Name() != "foo" {
		t.Fatalf("Name() = %q, want foo", erased.Name())
	}

	// Phase 1.5: schema is derived.
	if len(erased.Definition().Parameters) == 0 {
		t.Errorf("Definition().Parameters is empty — DeriveSchema should populate it")
	}

	// StreamingAnyTool contract.
	st, ok := erased.(StreamingAnyTool)
	if !ok {
		t.Fatal("EraseStreaming result does not satisfy StreamingAnyTool")
	}

	ch := make(chan StreamEvent, 4)
	res, err := st.ExecuteStream(context.Background(), json.RawMessage(`{"query":"hi"}`), ch)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("ToolResult.Error = %q, want empty", res.Error)
	}
	close(ch)
	got := 0
	for ev := range ch {
		if ev.Type == EventToolProgress {
			got++
		}
	}
	if got != 1 {
		t.Errorf("EventToolProgress count = %d, want 1", got)
	}
}
