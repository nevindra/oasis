package workflow

import (
	"context"
	"encoding/json"

	"github.com/nevindra/oasis/core"
)

// stubAgent is a minimal Agent implementation for workflow tests.
type stubAgent struct {
	name string
	desc string
	fn   func(core.AgentTask) (core.AgentResult, error)
}

func (s *stubAgent) Name() string        { return s.name }
func (s *stubAgent) Description() string { return s.desc }
func (s *stubAgent) Execute(_ context.Context, task core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	return s.fn(task)
}

// mockTool is a minimal AnyTool that returns "hello from <name>".
type mockTool struct{}

func (m mockTool) Name() string { return "greet" }
func (m mockTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{Name: "greet", Description: "Say hello"}
}
func (m mockTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
	return core.TextResult("hello from greet"), nil
}
