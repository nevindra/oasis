package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"path/filepath"

	oasis "github.com/nevindra/oasis"
)

// toolImpl wraps a single tool definition and its execute function.
type toolImpl struct {
	def     oasis.ToolDefinition
	execute func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error)
}

func newTool(name, description, schema string, exec func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error)) toolImpl {
	return toolImpl{
		def: oasis.ToolDefinition{
			Name:        name,
			Description: description,
			Parameters:  json.RawMessage(schema),
		},
		execute: exec,
	}
}

func (t toolImpl) Definitions() []oasis.ToolDefinition { return []oasis.ToolDefinition{t.def} }

func (t toolImpl) Execute(ctx context.Context, _ string, args json.RawMessage) (oasis.ToolResult, error) {
	return t.execute(ctx, args)
}

// ToolsOption configures optional sandbox tool capabilities.
type ToolsOption func(*toolsConfig)

type toolsConfig struct {
	delivery FileDelivery
}

// WithFileDelivery enables the deliver_file tool. The provided FileDelivery
// implementation handles persisting files downloaded from the sandbox.
func WithFileDelivery(fd FileDelivery) ToolsOption {
	return func(c *toolsConfig) { c.delivery = fd }
}

// Tools returns Oasis tool implementations backed by the given Sandbox.
func Tools(sb Sandbox, opts ...ToolsOption) []oasis.Tool {
	cfg := &toolsConfig{}
	for _, o := range opts {
		o(cfg)
	}

	tools := []oasis.Tool{
		shellTool(sb),
		executeCodeTool(sb),
		fileReadTool(sb),
		fileWriteTool(sb),
		fileEditTool(sb),
		fileGlobTool(sb),
		fileGrepTool(sb),
		browserTool(sb),
		screenshotTool(sb),
		mcpCallTool(sb),
	}

	if cfg.delivery != nil {
		tools = append(tools, deliverFileTool(sb, cfg.delivery))
	}

	return tools
}

func shellTool(sb Sandbox) toolImpl {
	return newTool("shell", "Execute shell command in the sandbox", `{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "Shell command to execute"},
			"cwd":     {"type": "string", "description": "Working directory"}
		},
		"required": ["command"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		res, err := sb.Shell(ctx, ShellRequest{Command: p.Command, Cwd: p.Cwd})
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		output := res.Output
		if res.ExitCode != 0 {
			output = fmt.Sprintf("exit code %d\n%s", res.ExitCode, output)
		}
		return oasis.ToolResult{Content: output}, nil
	})
}

func executeCodeTool(sb Sandbox) toolImpl {
	return newTool("execute_code", "Execute code in a language runtime", `{
		"type": "object",
		"properties": {
			"code":     {"type": "string", "description": "Code to execute"},
			"language": {"type": "string", "description": "Language runtime (default: python)"}
		},
		"required": ["code"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Code     string `json:"code"`
			Language string `json:"language"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		if p.Language == "" {
			p.Language = "python"
		}
		res, err := sb.ExecCode(ctx, CodeRequest{Code: p.Code, Language: p.Language})
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		if res.Status != "ok" {
			errMsg := res.Stderr
			if errMsg == "" {
				errMsg = res.Stdout
			}
			return oasis.ToolResult{Error: errMsg}, nil
		}
		output := res.Stdout
		if res.Stderr != "" {
			output += "\nstderr: " + res.Stderr
		}
		return oasis.ToolResult{Content: output}, nil
	})
}

func fileReadTool(sb Sandbox) toolImpl {
	return newTool("file_read", "Read file content from the sandbox", `{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "File path to read"}
		},
		"required": ["path"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		fc, err := sb.ReadFile(ctx, p.Path)
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		return oasis.ToolResult{Content: fc.Content}, nil
	})
}

func fileWriteTool(sb Sandbox) toolImpl {
	return newTool("file_write", "Write content to a file in the sandbox", `{
		"type": "object",
		"properties": {
			"path":    {"type": "string", "description": "File path to write"},
			"content": {"type": "string", "description": "File content"}
		},
		"required": ["path", "content"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		if err := sb.WriteFile(ctx, WriteFileRequest{Path: p.Path, Content: p.Content}); err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		return oasis.ToolResult{Content: "wrote to " + p.Path}, nil
	})
}

func fileEditTool(sb Sandbox) toolImpl {
	return newTool("file_edit", "Edit a file by replacing an exact string match with new content. The old string must appear exactly once in the file. This is more efficient than reading and rewriting the entire file.", `{
		"type": "object",
		"properties": {
			"path":       {"type": "string", "description": "Absolute path to the file to edit"},
			"old_string": {"type": "string", "description": "The exact text to find and replace (must be unique in the file)"},
			"new_string": {"type": "string", "description": "The replacement text"}
		},
		"required": ["path", "old_string", "new_string"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		if err := sb.EditFile(ctx, EditFileRequest{Path: p.Path, Old: p.OldString, New: p.NewString}); err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		return oasis.ToolResult{Content: "edited " + p.Path}, nil
	})
}

func fileGlobTool(sb Sandbox) toolImpl {
	return newTool("file_glob", "Find files matching a glob pattern. Supports ** for recursive matching.", `{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Glob pattern to match (e.g., '**/*.py', 'src/**/*.ts')"},
			"path":    {"type": "string", "description": "Base directory to search in (default: working directory)"}
		},
		"required": ["pattern"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		files, err := sb.GlobFiles(ctx, GlobRequest{Pattern: p.Pattern, Path: p.Path})
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		if len(files) == 0 {
			return oasis.ToolResult{Content: "no files matched"}, nil
		}
		var result string
		for i, f := range files {
			if i > 0 {
				result += "\n"
			}
			result += f
		}
		return oasis.ToolResult{Content: result}, nil
	})
}

func fileGrepTool(sb Sandbox) toolImpl {
	return newTool("file_grep", "Search file contents for a regex pattern. Returns matching lines with file paths and line numbers.", `{
		"type": "object",
		"properties": {
			"pattern": {"type": "string", "description": "Regex pattern to search for"},
			"path":    {"type": "string", "description": "Directory or file to search in (default: working directory)"},
			"glob":    {"type": "string", "description": "File pattern filter (e.g., '*.py' to only search Python files)"}
		},
		"required": ["pattern"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
			Glob    string `json:"glob"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		matches, err := sb.GrepFiles(ctx, GrepRequest{Pattern: p.Pattern, Path: p.Path, Glob: p.Glob})
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		if len(matches) == 0 {
			return oasis.ToolResult{Content: "no matches found"}, nil
		}
		var result string
		for i, m := range matches {
			if i > 0 {
				result += "\n"
			}
			result += fmt.Sprintf("%s:%d: %s", m.Path, m.Line, m.Content)
		}
		return oasis.ToolResult{Content: result}, nil
	})
}

func browserTool(sb Sandbox) toolImpl {
	return newTool("browser", "Interact with the sandbox browser", `{
		"type": "object",
		"properties": {
			"action": {"type": "string", "description": "Browser action: navigate, click, type, scroll, key"},
			"url":    {"type": "string", "description": "URL for navigate action"},
			"x":      {"type": "integer", "description": "X coordinate"},
			"y":      {"type": "integer", "description": "Y coordinate"},
			"text":   {"type": "string", "description": "Text to type"},
			"key":    {"type": "string", "description": "Key to press"}
		},
		"required": ["action"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Action string `json:"action"`
			URL    string `json:"url"`
			X      int    `json:"x"`
			Y      int    `json:"y"`
			Text   string `json:"text"`
			Key    string `json:"key"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		if p.Action == "navigate" && p.URL != "" {
			if err := sb.BrowserNavigate(ctx, p.URL); err != nil {
				return oasis.ToolResult{Error: err.Error()}, nil
			}
			return oasis.ToolResult{Content: "navigated to " + p.URL}, nil
		}
		res, err := sb.BrowserAction(ctx, BrowserAction{
			Type: p.Action,
			X:    p.X,
			Y:    p.Y,
			Text: p.Text,
			Key:  p.Key,
		})
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		return oasis.ToolResult{Content: res.Message}, nil
	})
}

func screenshotTool(sb Sandbox) toolImpl {
	return newTool("screenshot", "Take a screenshot of the sandbox browser", `{
		"type": "object",
		"properties": {}
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		data, err := sb.BrowserScreenshot(ctx)
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		return oasis.ToolResult{Content: fmt.Sprintf("screenshot captured (%d bytes)", len(data))}, nil
	})
}

func mcpCallTool(sb Sandbox) toolImpl {
	return newTool("mcp_call", "Invoke an MCP tool on a server in the sandbox", `{
		"type": "object",
		"properties": {
			"server": {"type": "string", "description": "MCP server name"},
			"tool":   {"type": "string", "description": "Tool name"},
			"args":   {"type": "object", "description": "Tool arguments"}
		},
		"required": ["server", "tool"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Server string         `json:"server"`
			Tool   string         `json:"tool"`
			Args   map[string]any `json:"args"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		res, err := sb.MCPCall(ctx, MCPRequest{Server: p.Server, Tool: p.Tool, Args: p.Args})
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		if res.IsError {
			return oasis.ToolResult{Error: res.Content}, nil
		}
		return oasis.ToolResult{Content: res.Content}, nil
	})
}

// maxDeliverFileBytes caps the file size for deliver_file to prevent
// unbounded memory allocation when reading sandbox files into memory.
const maxDeliverFileBytes = 100 * 1024 * 1024 // 100 MB

// deliverFile implements StreamingTool so it can emit a file_attachment event
// on the shared stream channel alongside the normal tool result.
type deliverFile struct {
	def      oasis.ToolDefinition
	sandbox  Sandbox
	delivery FileDelivery
}

func deliverFileTool(sb Sandbox, fd FileDelivery) *deliverFile {
	return &deliverFile{
		def: oasis.ToolDefinition{
			Name: "deliver_file",
			Description: "Deliver a file from the sandbox to the user. The file will appear as a downloadable " +
				"attachment in the conversation. Use this after creating a file the user needs (reports, charts, " +
				"converted documents, generated code, etc).",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path to the file in the sandbox"},
					"name": {"type": "string", "description": "Display name for the download. Defaults to the filename."}
				},
				"required": ["path"]
			}`),
		},
		sandbox:  sb,
		delivery: fd,
	}
}

func (t *deliverFile) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{t.def}
}

func (t *deliverFile) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
	return t.executeDelivery(ctx, args, nil)
}

func (t *deliverFile) ExecuteStream(ctx context.Context, name string, args json.RawMessage, ch chan<- oasis.StreamEvent) (oasis.ToolResult, error) {
	return t.executeDelivery(ctx, args, ch)
}

func (t *deliverFile) executeDelivery(ctx context.Context, args json.RawMessage, ch chan<- oasis.StreamEvent) (oasis.ToolResult, error) {
	var p struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if p.Path == "" {
		return oasis.ToolResult{Error: "path is required"}, nil
	}

	displayName := p.Name
	if displayName == "" {
		displayName = filepath.Base(p.Path)
	}

	// Detect MIME type from file extension; fall back to octet-stream.
	mimeType := mime.TypeByExtension(filepath.Ext(p.Path))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Download file from sandbox.
	rc, err := t.sandbox.DownloadFile(ctx, p.Path)
	if err != nil {
		return oasis.ToolResult{Error: "download failed: " + err.Error()}, nil
	}
	defer rc.Close()

	// Read into memory (bounded) to get size before delivering.
	data, err := io.ReadAll(io.LimitReader(rc, maxDeliverFileBytes+1))
	if err != nil {
		return oasis.ToolResult{Error: "read failed: " + err.Error()}, nil
	}
	if len(data) > maxDeliverFileBytes {
		return oasis.ToolResult{Error: fmt.Sprintf("file too large (max %s)", humanSize(maxDeliverFileBytes))}, nil
	}

	size := int64(len(data))

	// Deliver via the app-provided implementation.
	url, err := t.delivery.Deliver(ctx, displayName, mimeType, size, bytes.NewReader(data))
	if err != nil {
		return oasis.ToolResult{Error: "delivery failed: " + err.Error()}, nil
	}

	// Emit file_attachment event if streaming.
	if ch != nil {
		eventData, _ := json.Marshal(map[string]any{
			"name":      displayName,
			"mime_type": mimeType,
			"size":      size,
			"url":       url,
		})
		select {
		case ch <- oasis.StreamEvent{
			Type:    oasis.EventFileAttachment,
			Name:    "deliver_file",
			Content: string(eventData),
		}:
		case <-ctx.Done():
		}
	}

	return oasis.ToolResult{Content: fmt.Sprintf("delivered %s (%s)", displayName, humanSize(size))}, nil
}

// humanSize formats a byte count as a human-readable string.
func humanSize(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// compile-time checks
var (
	_ oasis.Tool          = (*deliverFile)(nil)
	_ oasis.StreamingTool = (*deliverFile)(nil)
)
