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
	Message string `json:"message"`
}

type echoOutput struct {
	Echoed string `json:"echoed"`
}

type echoTool struct {
	failOnExecute bool
}

func (e *echoTool) Name() string { return "echo" }

func (e *echoTool) Definition() ToolDefinition {
	return ToolDefinition{
		Name:        "echo",
		Description: "echoes its input",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
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
	any := Erase[echoInput, echoOutput](&echoTool{})
	if any.Name() != "echo" {
		t.Errorf("Name() = %q", any.Name())
	}
	if any.Definition().Name != "echo" {
		t.Errorf("Definition.Name = %q", any.Definition().Name)
	}

	args, _ := json.Marshal(echoInput{Message: "hello"})
	res, err := any.ExecuteRaw(context.Background(), args)
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
	any := Erase[echoInput, echoOutput](&echoTool{})
	res, err := any.ExecuteRaw(context.Background(), json.RawMessage(`{not json}`))
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
	any := Erase[echoInput, echoOutput](&echoTool{failOnExecute: true})
	args, _ := json.Marshal(echoInput{Message: "boom"})
	res, err := any.ExecuteRaw(context.Background(), args)
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
	any := Erase[echoInput, echoOutput](&echoTool{})
	res, err := any.ExecuteRaw(context.Background(), nil)
	if err != nil {
		t.Fatalf("ExecuteRaw Go error: %v", err)
	}
	if res.Error != "" {
		t.Errorf("expected no error for empty args, got %q", res.Error)
	}
}
