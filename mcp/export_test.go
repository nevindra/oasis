// export_test.go exposes unexported symbols so that package mcp_test files
// (external test package, which can import mcptest without a cycle) can still
// exercise registry internals.
package mcp

import (
	"log/slog"
	"os"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// NewTestRegistry builds a Registry with a stderr logger. Used by mcp_test
// package tests.
func NewTestRegistry(t *testing.T) *Registry {
	t.Helper()
	return NewRegistry(WithLogger(slog.New(slog.NewTextHandler(os.Stderr, nil))))
}

// SetDeferredModeForTest exposes setDeferredMode for external tests.
func (r *Registry) SetDeferredModeForTest(enabled bool, alwaysOn bool, exclude map[string]bool) {
	r.setDeferredMode(&deferConfig{enabled: enabled, alwaysOn: alwaysOn, exclude: exclude})
}

// ToolDefinitionsForTest exposes toolDefinitionsForTest for external tests.
func (r *Registry) ToolDefinitionsForTest() []oasis.ToolDefinition {
	return r.toolDefinitionsForTest()
}

// AddToolForTest exposes addTool for external tests.
func (r *Registry) AddToolForTest(t oasis.AnyTool) bool {
	return r.addTool(t)
}

// NewToolSearchToolForTest exposes newToolSearchTool for external tests.
// Returns oasis.AnyTool so the unexported *toolSearchTool type doesn't
// leak across the package boundary.
func NewToolSearchToolForTest(r *Registry) oasis.AnyTool {
	return newToolSearchTool(r)
}

// TokenizeQueryForTest exposes tokenizeQuery for external tests.
func TokenizeQueryForTest(query string) []string {
	return tokenizeQuery(query)
}

// ScoreToolMatchForTest exposes scoreToolMatch for external tests.
func ScoreToolMatchForTest(queryWords []string, toolName, description string) float64 {
	return scoreToolMatch(queryWords, toolName, description)
}
