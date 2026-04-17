package oasis

import (
	"context"
	"encoding/json"
	"testing"
)

type stubRegistryTool struct{ name string }

func (s stubRegistryTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: s.name}}
}
func (s stubRegistryTool) Execute(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "ok"}, nil
}

func TestToolRegistry_Remove_Existing(t *testing.T) {
	r := NewToolRegistry()
	r.Add(stubRegistryTool{name: "foo"})
	if err := r.Remove("foo"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	defs := r.AllDefinitions()
	for _, d := range defs {
		if d.Name == "foo" {
			t.Errorf("tool not removed from AllDefinitions")
		}
	}
}

func TestToolRegistry_Remove_NotFound(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Remove("nonexistent"); err == nil {
		t.Errorf("expected error for missing tool")
	}
}

func TestToolRegistry_Remove_IndexCleared(t *testing.T) {
	r := NewToolRegistry()
	r.Add(stubRegistryTool{name: "bar"})
	if err := r.Remove("bar"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Execute should return unknown-tool error, not hit the removed tool.
	result, err := r.Execute(context.Background(), "bar", nil)
	if err != nil {
		t.Fatalf("unexpected error from Execute: %v", err)
	}
	if result.Error == "" {
		t.Errorf("expected unknown-tool error in result, got content=%q", result.Content)
	}
}
