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

// Tool provides file operations within a sandboxed workspace.
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
		{
			Name:        "file_list",
			Description: "List files and directories in a workspace directory. Returns one entry per line with type prefix (file/dir) and name.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory path relative to workspace (empty or '.' for root)"}}}`),
		},
		{
			Name:        "file_delete",
			Description: "Delete a file or empty directory from the workspace.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File or directory path relative to workspace"}},"required":["path"]}`),
		},
		{
			Name:        "file_stat",
			Description: "Get metadata for a file or directory in the workspace. Returns name, size, type, and modification time.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File or directory path relative to workspace"}},"required":["path"]}`),
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

	path := params.Path
	if path == "" {
		path = "."
	}
	resolved, err := t.resolvePath(path)
	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}

	switch name {
	case "file_read":
		return t.read(resolved)
	case "file_write":
		return t.write(resolved, params.Content)
	case "file_list":
		return t.list(resolved)
	case "file_delete":
		return t.remove(resolved)
	case "file_stat":
		return t.stat(resolved)
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

func (t *Tool) list(path string) (oasis.ToolResult, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return oasis.ToolResult{Error: "list error: " + err.Error()}, nil
	}
	var b strings.Builder
	for _, e := range entries {
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		}
		fmt.Fprintf(&b, "%s\t%s\n", kind, e.Name())
	}
	return oasis.ToolResult{Content: b.String()}, nil
}

func (t *Tool) remove(path string) (oasis.ToolResult, error) {
	if err := os.Remove(path); err != nil {
		return oasis.ToolResult{Error: "delete error: " + err.Error()}, nil
	}
	return oasis.ToolResult{Content: fmt.Sprintf("Deleted %s", filepath.Base(path))}, nil
}

func (t *Tool) stat(path string) (oasis.ToolResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return oasis.ToolResult{Error: "stat error: " + err.Error()}, nil
	}
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	out, _ := json.Marshal(map[string]any{
		"name":     info.Name(),
		"size":     info.Size(),
		"type":     kind,
		"modified": info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
	})
	return oasis.ToolResult{Content: string(out)}, nil
}
