package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type mockTool struct{}

func (m mockTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "greet", Description: "Say hello"}}
}

func (m mockTool) Execute(_ context.Context, name string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "hello from " + name}, nil
}

func TestToolRegistry(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(mockTool{})

	defs := reg.AllDefinitions()
	if len(defs) != 1 || defs[0].Name != "greet" {
		t.Fatalf("expected 1 definition 'greet', got %v", defs)
	}

	res, err := reg.Execute(context.Background(), "greet", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello from greet" {
		t.Errorf("expected 'hello from greet', got %q", res.Content)
	}

	res, _ = reg.Execute(context.Background(), "nonexistent", nil)
	if res.Error == "" {
		t.Error("expected error for unknown tool")
	}
}

// --- Additional tool mocks ---

type mockToolCalc struct{}

func (m mockToolCalc) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "calc", Description: "Calculate"}}
}
func (m mockToolCalc) Execute(_ context.Context, name string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "result from " + name}, nil
}

type errTool struct{}

func (e errTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "fail", Description: "Always fails"}}
}
func (e errTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{}, errors.New("tool broken")
}

type multiTool struct{}

func (m multiTool) Definitions() []ToolDefinition {
	return []ToolDefinition{
		{Name: "read", Description: "Read file"},
		{Name: "write", Description: "Write file"},
	}
}
func (m multiTool) Execute(_ context.Context, name string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "did " + name}, nil
}

// --- Registry edge case tests ---

func TestToolRegistryEmpty(t *testing.T) {
	reg := NewToolRegistry()

	defs := reg.AllDefinitions()
	if len(defs) != 0 {
		t.Errorf("expected 0 definitions, got %d", len(defs))
	}

	res, _ := reg.Execute(context.Background(), "anything", nil)
	if res.Error == "" {
		t.Error("expected error for empty registry")
	}
}

func TestToolRegistryMultipleTools(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(mockTool{})
	reg.Add(mockToolCalc{})

	defs := reg.AllDefinitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}

	// Dispatch to correct tool
	res, err := reg.Execute(context.Background(), "greet", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello from greet" {
		t.Errorf("greet: got %q", res.Content)
	}

	res, err = reg.Execute(context.Background(), "calc", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "result from calc" {
		t.Errorf("calc: got %q", res.Content)
	}
}

func TestToolRegistryExecuteError(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(errTool{})

	_, err := reg.Execute(context.Background(), "fail", nil)
	if err == nil {
		t.Fatal("expected error from failing tool")
	}
	if err.Error() != "tool broken" {
		t.Errorf("error = %q, want %q", err.Error(), "tool broken")
	}
}

func TestToolRegistryMultiDefinitionTool(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(multiTool{})

	defs := reg.AllDefinitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}

	res, err := reg.Execute(context.Background(), "read", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "did read" {
		t.Errorf("read: got %q", res.Content)
	}

	res, err = reg.Execute(context.Background(), "write", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "did write" {
		t.Errorf("write: got %q", res.Content)
	}
}
