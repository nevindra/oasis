package oasis

import (
	"context"
	"encoding/json"
)

// Tool defines an agent capability with one or more tool functions.
type Tool interface {
	Definitions() []ToolDefinition
	Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
}

// ToolResult is the outcome of a tool execution.
type ToolResult struct {
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// ToolRegistry holds all registered tools and dispatches execution.
type ToolRegistry struct {
	tools []Tool
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{}
}

// Add registers a tool.
func (r *ToolRegistry) Add(t Tool) {
	r.tools = append(r.tools, t)
}

// AllDefinitions returns tool definitions from all registered tools.
func (r *ToolRegistry) AllDefinitions() []ToolDefinition {
	var defs []ToolDefinition
	for _, t := range r.tools {
		defs = append(defs, t.Definitions()...)
	}
	return defs
}

// Execute dispatches a tool call by name.
func (r *ToolRegistry) Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	for _, t := range r.tools {
		for _, d := range t.Definitions() {
			if d.Name == name {
				return t.Execute(ctx, name, args)
			}
		}
	}
	return ToolResult{Error: "unknown tool: " + name}, nil
}
