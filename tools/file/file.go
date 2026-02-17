package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// Tool provides file read/write within a sandboxed workspace.
type Tool struct {
	workspacePath string
}

// New creates a FileTool restricted to workspacePath.
func New(workspacePath string) *Tool {
	return &Tool{workspacePath: workspacePath}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{
		{
			Name:        "file_read",
			Description: "Read a file from the workspace. Returns the file content (truncated to 8000 chars if large).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path relative to workspace"}},"required":["path"]}`),
		},
		{
			Name:        "file_write",
			Description: "Write content to a file in the workspace. Creates parent directories if needed.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path relative to workspace"},"content":{"type":"string","description":"Content to write"}},"required":["path","content"]}`),
		},
	}
}

func (t *Tool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
	var params struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}

	resolved, err := t.resolvePath(params.Path)
	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}

	switch name {
	case "file_read":
		return t.read(resolved)
	case "file_write":
		return t.write(resolved, params.Content)
	default:
		return oasis.ToolResult{Error: "unknown file tool: " + name}, nil
	}
}

func (t *Tool) resolvePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute paths not allowed: %s", path)
	}
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("path traversal not allowed: %s", path)
	}
	resolved := filepath.Join(t.workspacePath, path)
	// Double-check it's still within workspace
	if !strings.HasPrefix(resolved, t.workspacePath) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}
	return resolved, nil
}

func (t *Tool) read(path string) (oasis.ToolResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return oasis.ToolResult{Error: "read error: " + err.Error()}, nil
	}
	content := string(data)
	if len(content) > 8000 {
		content = content[:8000] + "\n... (truncated)"
	}
	return oasis.ToolResult{Content: content}, nil
}

func (t *Tool) write(path, content string) (oasis.ToolResult, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return oasis.ToolResult{Error: "mkdir error: " + err.Error()}, nil
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return oasis.ToolResult{Error: "write error: " + err.Error()}, nil
	}
	return oasis.ToolResult{Content: fmt.Sprintf("Written %d bytes to %s", len(content), filepath.Base(path))}, nil
}
