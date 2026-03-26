package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// mockSandbox implements Sandbox for testing tool dispatch.
type mockSandbox struct {
	shellFn        func(ctx context.Context, req ShellRequest) (ShellResult, error)
	execCodeFn     func(ctx context.Context, req CodeRequest) (CodeResult, error)
	readFileFn     func(ctx context.Context, path string) (FileContent, error)
	writeFileFn    func(ctx context.Context, req WriteFileRequest) error
	editFileFn     func(ctx context.Context, req EditFileRequest) error
	globFilesFn    func(ctx context.Context, req GlobRequest) ([]string, error)
	grepFilesFn    func(ctx context.Context, req GrepRequest) ([]GrepMatch, error)
	browserNavFn   func(ctx context.Context, url string) error
	browserActFn   func(ctx context.Context, action BrowserAction) (BrowserResult, error)
	screenshotFn   func(ctx context.Context) ([]byte, error)
	mcpCallFn      func(ctx context.Context, req MCPRequest) (MCPResult, error)
	downloadFileFn func(ctx context.Context, path string) (io.ReadCloser, error)
}

func (m *mockSandbox) Shell(ctx context.Context, req ShellRequest) (ShellResult, error) {
	if m.shellFn != nil {
		return m.shellFn(ctx, req)
	}
	return ShellResult{}, nil
}

func (m *mockSandbox) ExecCode(ctx context.Context, req CodeRequest) (CodeResult, error) {
	if m.execCodeFn != nil {
		return m.execCodeFn(ctx, req)
	}
	return CodeResult{}, nil
}

func (m *mockSandbox) ReadFile(ctx context.Context, path string) (FileContent, error) {
	if m.readFileFn != nil {
		return m.readFileFn(ctx, path)
	}
	return FileContent{}, nil
}

func (m *mockSandbox) WriteFile(ctx context.Context, req WriteFileRequest) error {
	if m.writeFileFn != nil {
		return m.writeFileFn(ctx, req)
	}
	return nil
}

func (m *mockSandbox) EditFile(ctx context.Context, req EditFileRequest) error {
	if m.editFileFn != nil {
		return m.editFileFn(ctx, req)
	}
	return nil
}

func (m *mockSandbox) GlobFiles(ctx context.Context, req GlobRequest) ([]string, error) {
	if m.globFilesFn != nil {
		return m.globFilesFn(ctx, req)
	}
	return nil, nil
}

func (m *mockSandbox) GrepFiles(ctx context.Context, req GrepRequest) ([]GrepMatch, error) {
	if m.grepFilesFn != nil {
		return m.grepFilesFn(ctx, req)
	}
	return nil, nil
}

func (m *mockSandbox) UploadFile(ctx context.Context, path string, data io.Reader) error {
	return nil
}

func (m *mockSandbox) DownloadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	if m.downloadFileFn != nil {
		return m.downloadFileFn(ctx, path)
	}
	return nil, nil
}

func (m *mockSandbox) BrowserNavigate(ctx context.Context, url string) error {
	if m.browserNavFn != nil {
		return m.browserNavFn(ctx, url)
	}
	return nil
}

func (m *mockSandbox) BrowserScreenshot(ctx context.Context) ([]byte, error) {
	if m.screenshotFn != nil {
		return m.screenshotFn(ctx)
	}
	return nil, nil
}

func (m *mockSandbox) BrowserAction(ctx context.Context, action BrowserAction) (BrowserResult, error) {
	if m.browserActFn != nil {
		return m.browserActFn(ctx, action)
	}
	return BrowserResult{}, nil
}

func (m *mockSandbox) MCPCall(ctx context.Context, req MCPRequest) (MCPResult, error) {
	if m.mcpCallFn != nil {
		return m.mcpCallFn(ctx, req)
	}
	return MCPResult{}, nil
}

func (m *mockSandbox) Close() error { return nil }

func TestShellToolDispatch(t *testing.T) {
	var captured ShellRequest
	sb := &mockSandbox{
		shellFn: func(_ context.Context, req ShellRequest) (ShellResult, error) {
			captured = req
			return ShellResult{Output: "hello world", ExitCode: 0}, nil
		},
	}

	tools := Tools(sb)
	var shellTool interface {
		Execute(ctx context.Context, name string, args json.RawMessage) (interface{ Content() string }, error)
	}
	_ = shellTool // suppress unused

	// Find the shell tool.
	var found bool
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "shell" {
				found = true
				args := json.RawMessage(`{"command":"ls -la","cwd":"/tmp"}`)
				result, err := tool.Execute(context.Background(), "shell", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if captured.Command != "ls -la" {
					t.Errorf("command = %q, want %q", captured.Command, "ls -la")
				}
				if captured.Cwd != "/tmp" {
					t.Errorf("cwd = %q, want %q", captured.Cwd, "/tmp")
				}
				if result.Content != "hello world" {
					t.Errorf("content = %q, want %q", result.Content, "hello world")
				}
				if result.Error != "" {
					t.Errorf("unexpected error field: %q", result.Error)
				}
			}
		}
	}
	if !found {
		t.Fatal("shell tool not found")
	}
}

func TestShellToolNonZeroExit(t *testing.T) {
	sb := &mockSandbox{
		shellFn: func(_ context.Context, req ShellRequest) (ShellResult, error) {
			return ShellResult{Output: "not found", ExitCode: 1}, nil
		},
	}

	tools := Tools(sb)
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "shell" {
				args := json.RawMessage(`{"command":"false"}`)
				result, err := tool.Execute(context.Background(), "shell", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				want := "exit code 1\nnot found"
				if result.Content != want {
					t.Errorf("content = %q, want %q", result.Content, want)
				}
			}
		}
	}
}

func TestExecuteCodeToolDispatch(t *testing.T) {
	var captured CodeRequest
	sb := &mockSandbox{
		execCodeFn: func(_ context.Context, req CodeRequest) (CodeResult, error) {
			captured = req
			return CodeResult{Status: "ok", Stdout: "42", Stderr: ""}, nil
		},
	}

	tools := Tools(sb)
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "execute_code" {
				args := json.RawMessage(`{"code":"print(42)","language":"python"}`)
				result, err := tool.Execute(context.Background(), "execute_code", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if captured.Code != "print(42)" {
					t.Errorf("code = %q, want %q", captured.Code, "print(42)")
				}
				if captured.Language != "python" {
					t.Errorf("language = %q, want %q", captured.Language, "python")
				}
				if result.Content != "42" {
					t.Errorf("content = %q, want %q", result.Content, "42")
				}
			}
		}
	}
}

func TestExecuteCodeDefaultLanguage(t *testing.T) {
	var captured CodeRequest
	sb := &mockSandbox{
		execCodeFn: func(_ context.Context, req CodeRequest) (CodeResult, error) {
			captured = req
			return CodeResult{Status: "ok", Stdout: "ok"}, nil
		},
	}

	tools := Tools(sb)
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "execute_code" {
				args := json.RawMessage(`{"code":"x = 1"}`)
				_, err := tool.Execute(context.Background(), "execute_code", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if captured.Language != "python" {
					t.Errorf("language = %q, want default %q", captured.Language, "python")
				}
			}
		}
	}
}

func TestExecuteCodeError(t *testing.T) {
	sb := &mockSandbox{
		execCodeFn: func(_ context.Context, req CodeRequest) (CodeResult, error) {
			return CodeResult{Status: "error", Stdout: "", Stderr: "NameError: x"}, nil
		},
	}

	tools := Tools(sb)
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "execute_code" {
				args := json.RawMessage(`{"code":"print(x)"}`)
				result, err := tool.Execute(context.Background(), "execute_code", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.Error == "" {
					t.Error("expected error field to be set")
				}
			}
		}
	}
}

func TestToolDefinitionsComplete(t *testing.T) {
	sb := &mockSandbox{}
	tools := Tools(sb)

	expected := map[string]bool{
		"shell":        false,
		"execute_code": false,
		"file_read":    false,
		"file_write":   false,
		"file_edit":    false,
		"file_glob":    false,
		"file_grep":    false,
		"browser":      false,
		"screenshot":   false,
		"mcp_call":     false,
	}

	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if _, ok := expected[def.Name]; ok {
				expected[def.Name] = true
			} else {
				t.Errorf("unexpected tool: %q", def.Name)
			}

			// Verify description is non-empty.
			if def.Description == "" {
				t.Errorf("tool %q has empty description", def.Name)
			}

			// Verify parameters is valid JSON Schema.
			var schema map[string]any
			if err := json.Unmarshal(def.Parameters, &schema); err != nil {
				t.Errorf("tool %q has invalid parameters JSON: %v", def.Name, err)
			}
			if schema["type"] != "object" {
				t.Errorf("tool %q parameters type = %v, want %q", def.Name, schema["type"], "object")
			}
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing tool: %q", name)
		}
	}

	if len(tools) != 10 {
		t.Errorf("got %d tools, want 10", len(tools))
	}
}

func TestFileEditToolDispatch(t *testing.T) {
	var captured EditFileRequest
	sb := &mockSandbox{
		editFileFn: func(_ context.Context, req EditFileRequest) error {
			captured = req
			return nil
		},
	}

	tools := Tools(sb)
	var found bool
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "file_edit" {
				found = true
				args := json.RawMessage(`{"path":"/app/main.py","old_string":"print('hello')","new_string":"print('hello world')"}`)
				result, err := tool.Execute(context.Background(), "file_edit", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if captured.Path != "/app/main.py" {
					t.Errorf("path = %q, want %q", captured.Path, "/app/main.py")
				}
				if captured.Old != "print('hello')" {
					t.Errorf("old = %q, want %q", captured.Old, "print('hello')")
				}
				if captured.New != "print('hello world')" {
					t.Errorf("new = %q, want %q", captured.New, "print('hello world')")
				}
				if result.Content != "edited /app/main.py" {
					t.Errorf("content = %q, want %q", result.Content, "edited /app/main.py")
				}
				if result.Error != "" {
					t.Errorf("unexpected error field: %q", result.Error)
				}
			}
		}
	}
	if !found {
		t.Fatal("file_edit tool not found")
	}
}

func TestFileEditToolError(t *testing.T) {
	sb := &mockSandbox{
		editFileFn: func(_ context.Context, req EditFileRequest) error {
			return fmt.Errorf("string not found in file")
		},
	}

	tools := Tools(sb)
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "file_edit" {
				args := json.RawMessage(`{"path":"/app/main.py","old_string":"missing","new_string":"new"}`)
				result, err := tool.Execute(context.Background(), "file_edit", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.Error == "" {
					t.Error("expected error field to be set")
				}
			}
		}
	}
}

func TestFileGlobToolDispatch(t *testing.T) {
	var captured GlobRequest
	sb := &mockSandbox{
		globFilesFn: func(_ context.Context, req GlobRequest) ([]string, error) {
			captured = req
			return []string{"/app/main.py", "/app/lib/utils.py"}, nil
		},
	}

	tools := Tools(sb)
	var found bool
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "file_glob" {
				found = true
				args := json.RawMessage(`{"pattern":"**/*.py","path":"/app"}`)
				result, err := tool.Execute(context.Background(), "file_glob", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if captured.Pattern != "**/*.py" {
					t.Errorf("pattern = %q, want %q", captured.Pattern, "**/*.py")
				}
				if captured.Path != "/app" {
					t.Errorf("path = %q, want %q", captured.Path, "/app")
				}
				want := "/app/main.py\n/app/lib/utils.py"
				if result.Content != want {
					t.Errorf("content = %q, want %q", result.Content, want)
				}
				if result.Error != "" {
					t.Errorf("unexpected error field: %q", result.Error)
				}
			}
		}
	}
	if !found {
		t.Fatal("file_glob tool not found")
	}
}

func TestFileGlobToolNoMatches(t *testing.T) {
	sb := &mockSandbox{
		globFilesFn: func(_ context.Context, req GlobRequest) ([]string, error) {
			return nil, nil
		},
	}

	tools := Tools(sb)
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "file_glob" {
				args := json.RawMessage(`{"pattern":"**/*.rs"}`)
				result, err := tool.Execute(context.Background(), "file_glob", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.Content != "no files matched" {
					t.Errorf("content = %q, want %q", result.Content, "no files matched")
				}
			}
		}
	}
}

func TestFileGrepToolDispatch(t *testing.T) {
	var captured GrepRequest
	sb := &mockSandbox{
		grepFilesFn: func(_ context.Context, req GrepRequest) ([]GrepMatch, error) {
			captured = req
			return []GrepMatch{
				{Path: "/app/main.py", Line: 42, Content: "def main():"},
				{Path: "/app/lib/utils.py", Line: 10, Content: "def main_helper():"},
			}, nil
		},
	}

	tools := Tools(sb)
	var found bool
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "file_grep" {
				found = true
				args := json.RawMessage(`{"pattern":"def main","path":"/app","glob":"*.py"}`)
				result, err := tool.Execute(context.Background(), "file_grep", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if captured.Pattern != "def main" {
					t.Errorf("pattern = %q, want %q", captured.Pattern, "def main")
				}
				if captured.Path != "/app" {
					t.Errorf("path = %q, want %q", captured.Path, "/app")
				}
				if captured.Glob != "*.py" {
					t.Errorf("glob = %q, want %q", captured.Glob, "*.py")
				}
				want := "/app/main.py:42: def main():\n/app/lib/utils.py:10: def main_helper():"
				if result.Content != want {
					t.Errorf("content = %q, want %q", result.Content, want)
				}
				if result.Error != "" {
					t.Errorf("unexpected error field: %q", result.Error)
				}
			}
		}
	}
	if !found {
		t.Fatal("file_grep tool not found")
	}
}

func TestFileGrepToolNoMatches(t *testing.T) {
	sb := &mockSandbox{
		grepFilesFn: func(_ context.Context, req GrepRequest) ([]GrepMatch, error) {
			return nil, nil
		},
	}

	tools := Tools(sb)
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "file_grep" {
				args := json.RawMessage(`{"pattern":"nonexistent"}`)
				result, err := tool.Execute(context.Background(), "file_grep", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if result.Content != "no matches found" {
					t.Errorf("content = %q, want %q", result.Content, "no matches found")
				}
			}
		}
	}
}

// mockFileDelivery implements FileDelivery for testing.
type mockFileDelivery struct {
	deliverFn func(ctx context.Context, name, mimeType string, size int64, data io.Reader) (string, error)
}

func (m *mockFileDelivery) Deliver(ctx context.Context, name, mimeType string, size int64, data io.Reader) (string, error) {
	if m.deliverFn != nil {
		return m.deliverFn(ctx, name, mimeType, size, data)
	}
	return "", nil
}

func TestDeliverFileToolDispatch(t *testing.T) {
	fileContent := []byte("hello world report content")
	var capturedName, capturedMime string
	var capturedSize int64
	var capturedData []byte

	sb := &mockSandbox{
		downloadFileFn: func(_ context.Context, path string) (io.ReadCloser, error) {
			if path != "/workspace/report.pdf" {
				t.Errorf("download path = %q, want %q", path, "/workspace/report.pdf")
			}
			return io.NopCloser(bytes.NewReader(fileContent)), nil
		},
	}

	fd := &mockFileDelivery{
		deliverFn: func(_ context.Context, name, mimeType string, size int64, data io.Reader) (string, error) {
			capturedName = name
			capturedMime = mimeType
			capturedSize = size
			capturedData, _ = io.ReadAll(data)
			return "/api/files/abc123/download", nil
		},
	}

	tools := Tools(sb, WithFileDelivery(fd))

	// Find deliver_file tool and execute via streaming path.
	var found bool
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "deliver_file" {
				found = true

				// Test ExecuteStream path.
				st, ok := tool.(oasis.StreamingTool)
				if !ok {
					t.Fatal("deliver_file tool does not implement StreamingTool")
				}

				ch := make(chan oasis.StreamEvent, 10)
				args := json.RawMessage(`{"path":"/workspace/report.pdf","name":"My Report.pdf"}`)
				result, err := st.ExecuteStream(context.Background(), "deliver_file", args, ch)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				// Verify delivery was called correctly.
				if capturedName != "My Report.pdf" {
					t.Errorf("delivery name = %q, want %q", capturedName, "My Report.pdf")
				}
				if capturedMime != "application/pdf" {
					t.Errorf("delivery mime = %q, want %q", capturedMime, "application/pdf")
				}
				if capturedSize != int64(len(fileContent)) {
					t.Errorf("delivery size = %d, want %d", capturedSize, len(fileContent))
				}
				if !bytes.Equal(capturedData, fileContent) {
					t.Errorf("delivery data mismatch")
				}

				// Verify tool result.
				if !strings.Contains(result.Content, "delivered My Report.pdf") {
					t.Errorf("result content = %q, want to contain %q", result.Content, "delivered My Report.pdf")
				}
				if result.Error != "" {
					t.Errorf("unexpected error field: %q", result.Error)
				}

				// Verify file_attachment event was emitted.
				select {
				case ev := <-ch:
					if ev.Type != oasis.EventFileAttachment {
						t.Errorf("event type = %q, want %q", ev.Type, oasis.EventFileAttachment)
					}
					if !strings.Contains(ev.Content, `"url":"/api/files/abc123/download"`) {
						t.Errorf("event content missing url: %s", ev.Content)
					}
					if !strings.Contains(ev.Content, `"name":"My Report.pdf"`) {
						t.Errorf("event content missing name: %s", ev.Content)
					}
				default:
					t.Error("no file_attachment event emitted")
				}
			}
		}
	}
	if !found {
		t.Fatal("deliver_file tool not found")
	}
}

func TestDeliverFileToolDefaultName(t *testing.T) {
	sb := &mockSandbox{
		downloadFileFn: func(_ context.Context, path string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte("data"))), nil
		},
	}

	var capturedName string
	fd := &mockFileDelivery{
		deliverFn: func(_ context.Context, name, _ string, _ int64, _ io.Reader) (string, error) {
			capturedName = name
			return "/api/files/x/download", nil
		},
	}

	tools := Tools(sb, WithFileDelivery(fd))
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "deliver_file" {
				// Call without "name" field — should default to basename of path.
				args := json.RawMessage(`{"path":"/workspace/output/chart.png"}`)
				result, err := tool.Execute(context.Background(), "deliver_file", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if capturedName != "chart.png" {
					t.Errorf("delivery name = %q, want %q", capturedName, "chart.png")
				}
				if !strings.Contains(result.Content, "delivered chart.png") {
					t.Errorf("result content = %q, want to contain %q", result.Content, "delivered chart.png")
				}
			}
		}
	}
}

func TestDeliverFileToolNotRegisteredWithoutDelivery(t *testing.T) {
	sb := &mockSandbox{}
	tools := Tools(sb) // no WithFileDelivery

	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "deliver_file" {
				t.Error("deliver_file tool should not be registered without WithFileDelivery")
			}
		}
	}
}
