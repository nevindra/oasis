package oasis

import (
	"context"
	"encoding/json"
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
