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
	readFileFn     func(ctx context.Context, req ReadFileRequest) (FileContent, error)
	writeFileFn    func(ctx context.Context, req WriteFileRequest) error
	editFileFn     func(ctx context.Context, req EditFileRequest) error
	globFilesFn    func(ctx context.Context, req GlobRequest) (GlobResult, error)
	grepFilesFn    func(ctx context.Context, req GrepRequest) (GrepResult, error)
	browserNavFn   func(ctx context.Context, url string) error
	browserActFn   func(ctx context.Context, action BrowserAction) (BrowserResult, error)
	screenshotFn   func(ctx context.Context) ([]byte, error)
	mcpCallFn      func(ctx context.Context, req MCPRequest) (MCPResult, error)
	downloadFileFn func(ctx context.Context, path string) (io.ReadCloser, error)
	snapshotFn     func(ctx context.Context, opts SnapshotOpts) (BrowserSnapshot, error)
	browserTextFn  func(ctx context.Context, opts TextOpts) (BrowserTextResult, error)
	browserPDFFn   func(ctx context.Context) ([]byte, error)
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

func (m *mockSandbox) ReadFile(ctx context.Context, req ReadFileRequest) (FileContent, error) {
	if m.readFileFn != nil {
		return m.readFileFn(ctx, req)
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

func (m *mockSandbox) GlobFiles(ctx context.Context, req GlobRequest) (GlobResult, error) {
	if m.globFilesFn != nil {
		return m.globFilesFn(ctx, req)
	}
	return GlobResult{}, nil
}

func (m *mockSandbox) GrepFiles(ctx context.Context, req GrepRequest) (GrepResult, error) {
	if m.grepFilesFn != nil {
		return m.grepFilesFn(ctx, req)
	}
	return GrepResult{}, nil
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

func (m *mockSandbox) BrowserSnapshot(ctx context.Context, opts SnapshotOpts) (BrowserSnapshot, error) {
	if m.snapshotFn != nil {
		return m.snapshotFn(ctx, opts)
	}
	return BrowserSnapshot{}, nil
}

func (m *mockSandbox) BrowserText(ctx context.Context, opts TextOpts) (BrowserTextResult, error) {
	if m.browserTextFn != nil {
		return m.browserTextFn(ctx, opts)
	}
	return BrowserTextResult{}, nil
}

func (m *mockSandbox) BrowserPDF(ctx context.Context) ([]byte, error) {
	if m.browserPDFFn != nil {
		return m.browserPDFFn(ctx)
	}
	return nil, nil
}

func (m *mockSandbox) Tree(ctx context.Context, req TreeRequest) (TreeResult, error) {
	return TreeResult{}, nil
}

func (m *mockSandbox) HTTPFetch(ctx context.Context, req HTTPFetchRequest) (HTTPFetchResult, error) {
	return HTTPFetchResult{}, nil
}

func (m *mockSandbox) WorkspaceInfo(ctx context.Context) (WorkspaceInfoResult, error) {
	return WorkspaceInfoResult{}, nil
}

func (m *mockSandbox) BrowserEval(ctx context.Context, expression string) (string, error) {
	return "", nil
}

func (m *mockSandbox) BrowserFind(ctx context.Context, query string) (BrowserFindResult, error) {
	return BrowserFindResult{}, nil
}

func (m *mockSandbox) WebSearch(ctx context.Context, req WebSearchRequest) (WebSearchResult, error) {
	return WebSearchResult{}, nil
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
		"shell":          false,
		"execute_code":   false,
		"file_read":      false,
		"file_write":     false,
		"file_edit":      false,
		"file_glob":      false,
		"file_grep":      false,
		"file_tree":      false,
		"http_fetch":     false,
		"workspace_info": false,
		"browser":        false,
		"screenshot":     false,
		"mcp_call":       false,
		"snapshot":       false,
		"page_text":      false,
		"export_pdf":     false,
		"browser_eval":   false,
		"browser_find":   false,
		"web_search":     false,
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

	if len(tools) != 19 {
		t.Errorf("got %d tools, want 19", len(tools))
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
		globFilesFn: func(_ context.Context, req GlobRequest) (GlobResult, error) {
			captured = req
			return GlobResult{Files: []string{"/app/main.py", "/app/lib/utils.py"}}, nil
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
		globFilesFn: func(_ context.Context, req GlobRequest) (GlobResult, error) {
			return GlobResult{}, nil
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
		grepFilesFn: func(_ context.Context, req GrepRequest) (GrepResult, error) {
			captured = req
			return GrepResult{Matches: []GrepMatch{
				{Path: "/app/main.py", Line: 42, Content: "def main():"},
				{Path: "/app/lib/utils.py", Line: 10, Content: "def main_helper():"},
			}}, nil
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
		grepFilesFn: func(_ context.Context, req GrepRequest) (GrepResult, error) {
			return GrepResult{}, nil
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

func TestSnapshotToolDispatch(t *testing.T) {
	var captured SnapshotOpts
	sb := &mockSandbox{
		snapshotFn: func(_ context.Context, opts SnapshotOpts) (BrowserSnapshot, error) {
			captured = opts
			return BrowserSnapshot{
				URL:   "https://example.com",
				Title: "Example",
				Nodes: []SnapshotNode{
					{Ref: "e0", Role: "link", Name: "Home"},
					{Ref: "e1", Role: "button", Name: "Submit"},
				},
			}, nil
		},
	}

	tools := Tools(sb)
	var found bool
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "snapshot" {
				found = true
				args := json.RawMessage(`{"filter":"interactive"}`)
				result, err := tool.Execute(context.Background(), "snapshot", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if captured.Filter != "interactive" {
					t.Errorf("filter = %q, want %q", captured.Filter, "interactive")
				}
				if !strings.Contains(result.Content, "[e0] link \"Home\"") {
					t.Errorf("content missing e0 node: %q", result.Content)
				}
				if !strings.Contains(result.Content, "[e1] button \"Submit\"") {
					t.Errorf("content missing e1 node: %q", result.Content)
				}
				if result.Error != "" {
					t.Errorf("unexpected error: %q", result.Error)
				}
			}
		}
	}
	if !found {
		t.Fatal("snapshot tool not found")
	}
}

func TestPageTextToolDispatch(t *testing.T) {
	var captured TextOpts
	sb := &mockSandbox{
		browserTextFn: func(_ context.Context, opts TextOpts) (BrowserTextResult, error) {
			captured = opts
			return BrowserTextResult{
				URL:   "https://example.com",
				Title: "Example",
				Text:  "Welcome to Example.",
			}, nil
		},
	}

	tools := Tools(sb)
	var found bool
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "page_text" {
				found = true
				args := json.RawMessage(`{"raw":true,"max_chars":500}`)
				result, err := tool.Execute(context.Background(), "page_text", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !captured.Raw {
					t.Error("expected raw=true")
				}
				if captured.MaxChars != 500 {
					t.Errorf("max_chars = %d, want 500", captured.MaxChars)
				}
				if result.Content != "Welcome to Example." {
					t.Errorf("content = %q, want %q", result.Content, "Welcome to Example.")
				}
			}
		}
	}
	if !found {
		t.Fatal("page_text tool not found")
	}
}

func TestExportPDFToolDispatch(t *testing.T) {
	sb := &mockSandbox{
		browserPDFFn: func(_ context.Context) ([]byte, error) {
			return []byte("%PDF-1.4-fake"), nil
		},
	}

	tools := Tools(sb)
	var found bool
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "export_pdf" {
				found = true
				args := json.RawMessage(`{}`)
				result, err := tool.Execute(context.Background(), "export_pdf", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(result.Content, "13 bytes") {
					t.Errorf("content = %q, want size info", result.Content)
				}
			}
		}
	}
	if !found {
		t.Fatal("export_pdf tool not found")
	}
}

func TestBrowserToolWithRef(t *testing.T) {
	var captured BrowserAction
	sb := &mockSandbox{
		browserActFn: func(_ context.Context, action BrowserAction) (BrowserResult, error) {
			captured = action
			return BrowserResult{Success: true, Message: "clicked"}, nil
		},
	}

	tools := Tools(sb)
	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "browser" {
				args := json.RawMessage(`{"action":"click","ref":"e5"}`)
				result, err := tool.Execute(context.Background(), "browser", args)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if captured.Ref != "e5" {
					t.Errorf("ref = %q, want %q", captured.Ref, "e5")
				}
				if captured.Type != "click" {
					t.Errorf("type = %q, want %q", captured.Type, "click")
				}
				if result.Content != "clicked" {
					t.Errorf("content = %q, want %q", result.Content, "clicked")
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
	tools := Tools(sb) // no WithFileDelivery, no WithMounts

	for _, tool := range tools {
		for _, def := range tool.Definitions() {
			if def.Name == "deliver_file" {
				t.Error("deliver_file tool should not be registered without any destination")
			}
		}
	}
}

func TestDeliverFileRoutesThroughMount(t *testing.T) {
	mount := newFakeMount()
	sb := newRecordingSandbox()
	sb.files["/workspace/output/chart.png"] = []byte("PNG-DATA")

	tools := Tools(sb, WithMounts([]MountSpec{{
		Path:    "/workspace/output",
		Backend: mount,
		Mode:    MountReadWrite,
	}}, NewManifest()))

	deliver := findToolByName(tools, "deliver_file")
	if deliver == nil {
		t.Fatal("deliver_file tool not registered when WithMounts has writeable mount")
	}

	args := json.RawMessage(`{"path":"/workspace/output/chart.png"}`)
	res, err := deliver.Execute(context.Background(), "deliver_file", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if string(mount.entries["chart.png"].data) != "PNG-DATA" {
		t.Errorf("backend chart.png = %q", mount.entries["chart.png"].data)
	}
}

func TestDeliverFileLegacyFileDeliveryShim(t *testing.T) {
	// WithFileDelivery should continue to work and produce a registered
	// deliver_file tool that publishes via the legacy interface.
	delivered := struct {
		body []byte
	}{}
	fd := &mockFileDelivery{
		deliverFn: func(ctx context.Context, name, mime string, size int64, data io.Reader) (string, error) {
			body, _ := io.ReadAll(data)
			delivered.body = body
			return "/api/files/x", nil
		},
	}

	sb := newRecordingSandbox()
	sb.files["/foo/bar.txt"] = []byte("legacy content")

	tools := Tools(sb, WithFileDelivery(fd))
	deliver := findToolByName(tools, "deliver_file")
	if deliver == nil {
		t.Fatal("deliver_file tool missing under WithFileDelivery")
	}

	args := json.RawMessage(`{"path":"/foo/bar.txt"}`)
	res, err := deliver.Execute(context.Background(), "deliver_file", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if string(delivered.body) != "legacy content" {
		t.Errorf("delivered body = %q, want %q", delivered.body, "legacy content")
	}
}

func TestDeliverFileErrorsWithoutDestination(t *testing.T) {
	mount := newFakeMount()
	sb := newRecordingSandbox()
	sb.files["/somewhere/else.txt"] = []byte("orphan")

	// Mount only covers /workspace/output; the path is outside.
	tools := Tools(sb, WithMounts([]MountSpec{{
		Path:    "/workspace/output",
		Backend: mount,
		Mode:    MountReadWrite,
	}}, NewManifest()))
	deliver := findToolByName(tools, "deliver_file")
	if deliver == nil {
		t.Fatal("deliver_file should still be registered when there's at least one writeable mount")
	}

	args := json.RawMessage(`{"path":"/somewhere/else.txt"}`)
	res, err := deliver.Execute(context.Background(), "deliver_file", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected error for path outside any mount with no FileDelivery fallback")
	}
}

func TestFindMountForPath(t *testing.T) {
	mounts := []MountSpec{
		{Path: "/workspace/inputs", Mode: MountReadOnly},
		{Path: "/workspace/output", Mode: MountReadWrite},
	}

	cases := []struct {
		path string
		want string // expected mount path, or "" for no match
		key  string // expected relative key (when matched)
	}{
		{"/workspace/inputs/data.csv", "/workspace/inputs", "data.csv"},
		{"/workspace/output/report.md", "/workspace/output", "report.md"},
		{"/workspace/output/sub/dir/x.txt", "/workspace/output", "sub/dir/x.txt"},
		{"/tmp/scratch", "", ""},
		{"/workspace/other.txt", "", ""},
		{"/workspace/inputs2/x", "", ""}, // not under /workspace/inputs
	}
	for _, c := range cases {
		got, key := findMountForPath(mounts, c.path)
		if c.want == "" {
			if got != nil {
				t.Errorf("findMountForPath(%q) = %v, want nil", c.path, got)
			}
			continue
		}
		if got == nil || got.Path != c.want {
			t.Errorf("findMountForPath(%q) = %v, want %s", c.path, got, c.want)
			continue
		}
		if key != c.key {
			t.Errorf("findMountForPath(%q) key = %q, want %q", c.path, key, c.key)
		}
	}
}

func findToolByName(tools []oasis.Tool, name string) oasis.Tool {
	for _, tl := range tools {
		for _, def := range tl.Definitions() {
			if def.Name == name {
				return tl
			}
		}
	}
	return nil
}

func TestFileWriteToolPublishesUnderWriteMount(t *testing.T) {
	mount := newFakeMount()
	sb := newRecordingSandbox()

	manifest := NewManifest()
	specs := []MountSpec{{
		Path:    "/workspace/output",
		Backend: mount,
		Mode:    MountReadWrite,
	}}

	tools := Tools(sb, WithMounts(specs, manifest))
	write := findToolByName(tools, "file_write")
	if write == nil {
		t.Fatal("file_write tool not found")
	}

	args := json.RawMessage(`{"path":"/workspace/output/report.md","content":"hello"}`)
	res, err := write.Execute(context.Background(), "file_write", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool returned error: %s", res.Error)
	}

	if string(mount.entries["report.md"].data) != "hello" {
		t.Errorf("backend report.md = %q, want %q", mount.entries["report.md"].data, "hello")
	}
	if v, _ := manifest.Version("/workspace/output", "report.md"); v == "" {
		t.Error("manifest should have recorded a version after publish")
	}
}

func TestFileWriteToolNoPublishOutsideMount(t *testing.T) {
	mount := newFakeMount()
	sb := newRecordingSandbox()

	tools := Tools(sb, WithMounts([]MountSpec{{
		Path:    "/workspace/output",
		Backend: mount,
		Mode:    MountReadWrite,
	}}, NewManifest()))

	write := findToolByName(tools, "file_write")

	args := json.RawMessage(`{"path":"/tmp/scratch.txt","content":"junk"}`)
	res, err := write.Execute(context.Background(), "file_write", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool returned error: %s", res.Error)
	}
	if len(mount.entries) != 0 {
		t.Errorf("mount should be empty for /tmp write, has %d entries", len(mount.entries))
	}
}

func TestFileWriteToolConflictReturnsError(t *testing.T) {
	mount := newFakeMount()
	mount.seed("report.md", "remote", "v2")
	sb := newRecordingSandbox()

	manifest := NewManifest()
	manifest.Record("/workspace/output", "report.md", MountEntry{Key: "report.md", Version: "v1"})

	tools := Tools(sb, WithMounts([]MountSpec{{
		Path:    "/workspace/output",
		Backend: mount,
		Mode:    MountReadWrite,
	}}, manifest))

	write := findToolByName(tools, "file_write")

	args := json.RawMessage(`{"path":"/workspace/output/report.md","content":"local"}`)
	res, err := write.Execute(context.Background(), "file_write", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected tool error on conflict, got success")
	}
	if !strings.Contains(res.Error, "version") && !strings.Contains(res.Error, "mismatch") {
		t.Errorf("error %q should mention version mismatch", res.Error)
	}
}

func TestFileEditToolPublishesUnderWriteMount(t *testing.T) {
	mount := newFakeMount()
	mount.seed("report.md", "first line\nsecond", "v1")
	sb := newRecordingSandbox()
	sb.files["/workspace/output/report.md"] = []byte("first line\nsecond")

	manifest := NewManifest()
	manifest.Record("/workspace/output", "report.md", MountEntry{Key: "report.md", Version: "v1"})

	tools := Tools(sb, WithMounts([]MountSpec{{
		Path:    "/workspace/output",
		Backend: mount,
		Mode:    MountReadWrite,
	}}, manifest))

	edit := findToolByName(tools, "file_edit")
	if edit == nil {
		t.Fatal("file_edit tool not found")
	}

	args := json.RawMessage(`{"path":"/workspace/output/report.md","old_string":"second","new_string":"second updated"}`)
	res, err := edit.Execute(context.Background(), "file_edit", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if string(mount.entries["report.md"].data) != "first line\nsecond updated" {
		t.Errorf("backend report.md = %q", mount.entries["report.md"].data)
	}
}

func TestFileWriteToolReadOnlyMountSilentlyAbsorbsLocally(t *testing.T) {
	mount := newFakeMount()
	sb := newRecordingSandbox()

	tools := Tools(sb, WithMounts([]MountSpec{{
		Path:    "/workspace/inputs",
		Backend: mount,
		Mode:    MountReadOnly,
	}}, NewManifest()))

	write := findToolByName(tools, "file_write")
	args := json.RawMessage(`{"path":"/workspace/inputs/scratch.txt","content":"local"}`)
	res, err := write.Execute(context.Background(), "file_write", args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("tool error: %s", res.Error)
	}
	if string(sb.files["/workspace/inputs/scratch.txt"]) != "local" {
		t.Error("local sandbox file should be written")
	}
	if len(mount.entries) != 0 {
		t.Errorf("read-only mount should not publish, has %d entries", len(mount.entries))
	}
}

func TestFindMountForPathPrefersDeepest(t *testing.T) {
	mounts := []MountSpec{
		{Path: "/workspace", Mode: MountReadWrite},
		{Path: "/workspace/output", Mode: MountWriteOnly},
	}
	got, key := findMountForPath(mounts, "/workspace/output/report.md")
	if got == nil || got.Path != "/workspace/output" {
		t.Errorf("got = %v, want /workspace/output", got)
	}
	if key != "report.md" {
		t.Errorf("key = %q, want %q", key, "report.md")
	}
}
