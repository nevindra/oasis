package core

import (
	"context"
	"encoding/json"
	"errors"
)

// mockTool and friends are minimal AnyTool implementations used by
// tool-registry tests.
type mockTool struct{}

func (mockTool) Name() string               { return "greet" }
func (mockTool) Definition() ToolDefinition { return ToolDefinition{Name: "greet", Description: "Say hello"} }
func (mockTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return TextResult("hello from greet"), nil
}

type mockToolCalc struct{}

func (mockToolCalc) Name() string               { return "calc" }
func (mockToolCalc) Definition() ToolDefinition { return ToolDefinition{Name: "calc", Description: "Calculate"} }
func (mockToolCalc) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return TextResult("result from calc"), nil
}

// errTool always returns an error from ExecuteRaw.
type errTool struct{}

func (errTool) Name() string               { return "fail" }
func (errTool) Definition() ToolDefinition { return ToolDefinition{Name: "fail", Description: "Always fails"} }
func (errTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{}, errors.New("tool broken")
}

// readTool and writeTool exercise two-tool registry scenarios.
type readTool struct{}

func (readTool) Name() string               { return "read" }
func (readTool) Definition() ToolDefinition { return ToolDefinition{Name: "read", Description: "Read file"} }
func (readTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return TextResult("did read"), nil
}

type writeTool struct{}

func (writeTool) Name() string               { return "write" }
func (writeTool) Definition() ToolDefinition { return ToolDefinition{Name: "write", Description: "Write file"} }
func (writeTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return TextResult("did write"), nil
}
