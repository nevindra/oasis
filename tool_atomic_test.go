package oasis

import (
	"context"
	"encoding/json"
	"testing"
)

// stubAnyTool is a minimal AnyTool implementation for interface compliance testing.
type stubAnyTool struct {
	name string
}

func (s *stubAnyTool) Name() string               { return s.name }
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
