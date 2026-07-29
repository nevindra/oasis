package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	oasis "github.com/nevindra/oasis/core"
)

// decodeContent returns the tool result's text content for assertion.
func decodeContent(_ *testing.T, r oasis.ToolResult) string {
	return r.Content
}

// mockSandbox implements Sandbox for testing tool dispatch.
type mockSandbox struct {
	shellFn        func(ctx context.Context, req ShellRequest) (ShellResult, error)
	execCodeFn     func(ctx context.Context, req CodeRequest) (CodeResult, error)
	readFileFn     func(ctx context.Context, req ReadFileRequest) (FileContent, error)
	writeFileFn    func(ctx context.Context, req WriteFileRequest) error
	editFileFn     func(ctx context.Context, req EditFileRequest) error
	globFilesFn    func(ctx context.Context, req GlobRequest) (GlobResult, error)
	grepFilesFn    func(ctx context.Context, req GrepRequest) (GrepResult, error)
	treeFn         func(ctx context.Context, req TreeRequest) (TreeResult, error)
	browserNavFn   func(ctx context.Context, url string) error
	browserActFn   func(ctx context.Context, action BrowserAction) (BrowserResult, error)
	screenshotFn   func(ctx context.Context) ([]byte, error)
	mcpCallFn      func(ctx context.Context, req MCPRequest) (MCPResult, error)
	downloadFileFn func(ctx context.Context, path string) (io.ReadCloser, error)
	snapshotFn     func(ctx context.Context, opts SnapshotOpts) (PageSnapshot, error)
	browserTextFn  func(ctx context.Context, opts TextOpts) (BrowserTextResult, error)
	browserPDFFn   func(ctx context.Context) ([]byte, error)
	browserWaitFn  func(ctx context.Context, opts BrowserWaitOpts) (BrowserWaitResult, error)
	browserEvalFn  func(ctx context.Context, expression string) (string, error)
	browserFindFn  func(ctx context.Context, query string) (BrowserFindResult, error)
	uploads        map[string][]byte
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
	if m.uploads != nil {
		body, err := io.ReadAll(data)
		if err != nil {
			return err
		}
		m.uploads[path] = body
	}
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

func (m *mockSandbox) BrowserSnapshot(ctx context.Context, opts SnapshotOpts) (PageSnapshot, error) {
	if m.snapshotFn != nil {
		return m.snapshotFn(ctx, opts)
	}
	return PageSnapshot{}, nil
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
	if m.treeFn != nil {
		return m.treeFn(ctx, req)
	}
	return TreeResult{}, nil
}

func (m *mockSandbox) HTTPFetch(ctx context.Context, req HTTPFetchRequest) (HTTPFetchResult, error) {
	return HTTPFetchResult{}, nil
}

func (m *mockSandbox) WorkspaceInfo(ctx context.Context) (WorkspaceInfoResult, error) {
	return WorkspaceInfoResult{}, nil
}

func (m *mockSandbox) BrowserEval(ctx context.Context, expression string) (string, error) {
	if m.browserEvalFn != nil {
		return m.browserEvalFn(ctx, expression)
	}
	return "", nil
}

func (m *mockSandbox) BrowserFind(ctx context.Context, query string) (BrowserFindResult, error) {
	if m.browserFindFn != nil {
		return m.browserFindFn(ctx, query)
	}
	return BrowserFindResult{}, nil
}

func (m *mockSandbox) BrowserWait(ctx context.Context, opts BrowserWaitOpts) (BrowserWaitResult, error) {
	if m.browserWaitFn != nil {
		return m.browserWaitFn(ctx, opts)
	}
	return BrowserWaitResult{}, nil
}

func (m *mockSandbox) WebSearch(ctx context.Context, req WebSearchRequest) (WebSearchResult, error) {
	return WebSearchResult{}, nil
}

func (m *mockSandbox) Close() error { return nil }

// mockSandbox drives a browser, so it satisfies both interfaces.
var (
	_ Sandbox        = (*mockSandbox)(nil)
	_ BrowserSandbox = (*mockSandbox)(nil)
)

// lightSandbox implements only the core Sandbox interface (no browser
// methods) — it models a headless/light runtime. Tools() must NOT register
// the browser_* tool set for it.
type lightSandbox struct{}

func (lightSandbox) Shell(context.Context, ShellRequest) (ShellResult, error) {
	return ShellResult{}, nil
}
func (lightSandbox) ExecCode(context.Context, CodeRequest) (CodeResult, error) {
	return CodeResult{}, nil
}
func (lightSandbox) ReadFile(context.Context, ReadFileRequest) (FileContent, error) {
	return FileContent{}, nil
}
func (lightSandbox) WriteFile(context.Context, WriteFileRequest) error   { return nil }
func (lightSandbox) UploadFile(context.Context, string, io.Reader) error { return nil }
func (lightSandbox) DownloadFile(context.Context, string) (io.ReadCloser, error) {
	return nil, nil
}
func (lightSandbox) MCPCall(context.Context, MCPRequest) (MCPResult, error) {
	return MCPResult{}, nil
}
func (lightSandbox) EditFile(context.Context, EditFileRequest) error { return nil }
func (lightSandbox) GlobFiles(context.Context, GlobRequest) (GlobResult, error) {
	return GlobResult{}, nil
}
func (lightSandbox) GrepFiles(context.Context, GrepRequest) (GrepResult, error) {
	return GrepResult{}, nil
}
func (lightSandbox) Tree(context.Context, TreeRequest) (TreeResult, error) {
	return TreeResult{}, nil
}
func (lightSandbox) HTTPFetch(context.Context, HTTPFetchRequest) (HTTPFetchResult, error) {
	return HTTPFetchResult{}, nil
}
func (lightSandbox) WebSearch(context.Context, WebSearchRequest) (WebSearchResult, error) {
	return WebSearchResult{}, nil
}
func (lightSandbox) WorkspaceInfo(context.Context) (WorkspaceInfoResult, error) {
	return WorkspaceInfoResult{}, nil
}
func (lightSandbox) Close() error { return nil }

var _ Sandbox = lightSandbox{}

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
		def := tool.Definition()

		if def.Name == "shell" {
			found = true
			args := json.RawMessage(`{"command":"ls -la","cwd":"/tmp"}`)
			result, err := tool.ExecuteRaw(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.Command != "ls -la" {
				t.Errorf("command = %q, want %q", captured.Command, "ls -la")
			}
			if captured.Cwd != "/tmp" {
				t.Errorf("cwd = %q, want %q", captured.Cwd, "/tmp")
			}
			if decodeContent(t, result) != "hello world" {
				t.Errorf("content = %q, want %q", decodeContent(t, result), "hello world")
			}
			if result.Error != "" {
				t.Errorf("unexpected error field: %q", result.Error)
			}
		}
		_ = def

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
		def := tool.Definition()

		if def.Name == "shell" {
			args := json.RawMessage(`{"command":"false"}`)
			result, err := tool.ExecuteRaw(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := "exit code 1\nnot found"
			if decodeContent(t, result) != want {
				t.Errorf("content = %q, want %q", decodeContent(t, result), want)
			}
		}
		_ = def

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
		def := tool.Definition()

		if def.Name == "execute_code" {
			args := json.RawMessage(`{"code":"print(42)","language":"python"}`)
			result, err := tool.ExecuteRaw(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.Code != "print(42)" {
				t.Errorf("code = %q, want %q", captured.Code, "print(42)")
			}
			if captured.Language != "python" {
				t.Errorf("language = %q, want %q", captured.Language, "python")
			}
			if decodeContent(t, result) != "42" {
				t.Errorf("content = %q, want %q", decodeContent(t, result), "42")
			}
		}
		_ = def

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
		def := tool.Definition()

		if def.Name == "execute_code" {
			args := json.RawMessage(`{"code":"x = 1"}`)
			_, err := tool.ExecuteRaw(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.Language != "python" {
				t.Errorf("language = %q, want default %q", captured.Language, "python")
			}
		}
		_ = def

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
		def := tool.Definition()

		if def.Name == "execute_code" {
			args := json.RawMessage(`{"code":"print(x)"}`)
			result, err := tool.ExecuteRaw(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Error == "" {
				t.Error("expected error field to be set")
			}
		}
		_ = def

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
		"file_search":    false,
		"http_fetch":     false,
		"workspace_info": false,
		"browser":        false,
		"browser_read":   false,
		"mcp_call":       false,
		"web_search":     false,
	}

	for _, tool := range tools {
		def := tool.Definition()

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
		_ = def

	}

	for name, found := range expected {
		if !found {
			t.Errorf("missing tool: %q", name)
		}
	}

	if len(tools) != 12 {
		t.Errorf("got %d tools, want 12", len(tools))
	}
}

// editMock returns a mockSandbox whose file store holds a single file with
// the given content, plus a pointer to the content the tool wrote back.
func editMock(path, content string) (*mockSandbox, *string) {
	written := new(string)
	sb := &mockSandbox{
		downloadFileFn: func(_ context.Context, p string) (io.ReadCloser, error) {
			if p != path {
				return nil, fmt.Errorf("not found: %s", p)
			}
			return io.NopCloser(strings.NewReader(content)), nil
		},
		writeFileFn: func(_ context.Context, req WriteFileRequest) error {
			*written = req.Content
			return nil
		},
	}
	return sb, written
}

func TestFileEditToolDispatch(t *testing.T) {
	sb, written := editMock("/app/main.py", "import os\nprint('hello')\nprint('bye')\n")

	edit := findToolByName(Tools(sb), "file_edit")
	if edit == nil {
		t.Fatal("file_edit tool not found")
	}
	args := json.RawMessage(`{"path":"/app/main.py","old_string":"print('hello')","new_string":"print('hello world')"}`)
	result, err := edit.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error field: %q", result.Error)
	}
	if *written != "import os\nprint('hello world')\nprint('bye')\n" {
		t.Errorf("written = %q", *written)
	}
	content := decodeContent(t, result)
	if !strings.HasPrefix(content, "edited /app/main.py (1 replacement)") {
		t.Errorf("content should start with edit summary, got %q", content)
	}
	for _, want := range []string{"@@ -1,4 +1,4 @@", "-print('hello')", "+print('hello world')", " import os"} {
		if !strings.Contains(content, want) {
			t.Errorf("diff missing %q in:\n%s", want, content)
		}
	}
}

func TestFileEditToolReplaceAll(t *testing.T) {
	sb, written := editMock("/app/a.txt", "foo\nbar\nfoo\n")

	edit := findToolByName(Tools(sb), "file_edit")
	args := json.RawMessage(`{"path":"/app/a.txt","old_string":"foo","new_string":"baz","replace_all":true}`)
	result, err := edit.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error field: %q", result.Error)
	}
	if *written != "baz\nbar\nbaz\n" {
		t.Errorf("written = %q", *written)
	}
	if !strings.HasPrefix(decodeContent(t, result), "edited /app/a.txt (2 replacements)") {
		t.Errorf("content = %q", decodeContent(t, result))
	}
}

func TestFileEditToolAmbiguousWithoutReplaceAll(t *testing.T) {
	sb, written := editMock("/app/a.txt", "foo\nbar\nfoo\n")

	edit := findToolByName(Tools(sb), "file_edit")
	args := json.RawMessage(`{"path":"/app/a.txt","old_string":"foo","new_string":"baz"}`)
	result, err := edit.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, "2 times") || !strings.Contains(result.Error, "replace_all") {
		t.Errorf("error should report count and suggest replace_all, got %q", result.Error)
	}
	if *written != "" {
		t.Errorf("file should not be written on ambiguity, wrote %q", *written)
	}
}

func TestFileEditToolNotFoundError(t *testing.T) {
	sb, _ := editMock("/app/main.py", "print('hi')\n")

	edit := findToolByName(Tools(sb), "file_edit")
	args := json.RawMessage(`{"path":"/app/main.py","old_string":"missing","new_string":"new"}`)
	result, err := edit.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, "not found") {
		t.Errorf("expected not-found error, got %q", result.Error)
	}
}

func TestEditDiffMergesNearbyReplacements(t *testing.T) {
	before := "a\nfoo1\nfoo2\nb\n"
	// Two occurrences on adjacent lines must merge into one hunk with
	// both replacements applied, not two hunks that each miss the other.
	diff := editDiff(before, "foo", "qux")
	if strings.Count(diff, "@@") != 2 { // one hunk header has two @@
		t.Fatalf("want a single hunk, got:\n%s", diff)
	}
	for _, want := range []string{"-foo1", "-foo2", "+qux1", "+qux2"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
}

func TestFileSearchFilesDispatch(t *testing.T) {
	var captured GlobRequest
	sb := &mockSandbox{
		globFilesFn: func(_ context.Context, req GlobRequest) (GlobResult, error) {
			captured = req
			return GlobResult{Files: []string{"/app/main.py", "/app/lib/utils.py"}}, nil
		},
	}

	search := findToolByName(Tools(sb), "file_search")
	if search == nil {
		t.Fatal("file_search tool not found")
	}
	args := json.RawMessage(`{"target":"files","pattern":"**/*.py","path":"/app"}`)
	result, err := search.ExecuteRaw(context.Background(), args)
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
	if decodeContent(t, result) != want {
		t.Errorf("content = %q, want %q", decodeContent(t, result), want)
	}
	if result.Error != "" {
		t.Errorf("unexpected error field: %q", result.Error)
	}
}

func TestFileSearchFilesNoMatches(t *testing.T) {
	sb := &mockSandbox{
		globFilesFn: func(_ context.Context, req GlobRequest) (GlobResult, error) {
			return GlobResult{}, nil
		},
	}

	search := findToolByName(Tools(sb), "file_search")
	args := json.RawMessage(`{"target":"files","pattern":"**/*.rs"}`)
	result, err := search.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decodeContent(t, result) != "no files matched" {
		t.Errorf("content = %q, want %q", decodeContent(t, result), "no files matched")
	}
}

func TestFileSearchContentDispatch(t *testing.T) {
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

	search := findToolByName(Tools(sb), "file_search")
	if search == nil {
		t.Fatal("file_search tool not found")
	}
	// target omitted → content is the default.
	args := json.RawMessage(`{"pattern":"def main","path":"/app","glob":"*.py"}`)
	result, err := search.ExecuteRaw(context.Background(), args)
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
	if decodeContent(t, result) != want {
		t.Errorf("content = %q, want %q", decodeContent(t, result), want)
	}
	if result.Error != "" {
		t.Errorf("unexpected error field: %q", result.Error)
	}
}

func TestFileSearchContentNoMatches(t *testing.T) {
	sb := &mockSandbox{
		grepFilesFn: func(_ context.Context, req GrepRequest) (GrepResult, error) {
			return GrepResult{}, nil
		},
	}

	search := findToolByName(Tools(sb), "file_search")
	args := json.RawMessage(`{"pattern":"nonexistent"}`)
	result, err := search.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decodeContent(t, result) != "no matches found" {
		t.Errorf("content = %q, want %q", decodeContent(t, result), "no matches found")
	}
}

func TestFileSearchContextLineNumbers(t *testing.T) {
	sb := &mockSandbox{
		grepFilesFn: func(_ context.Context, req GrepRequest) (GrepResult, error) {
			return GrepResult{Matches: []GrepMatch{
				{Path: "a.go", Line: 10, Content: "match one", ContextBefore: []string{"b8", "b9"}, ContextAfter: []string{"a11"}},
				{Path: "a.go", Line: 20, Content: "match two", ContextBefore: []string{"b18", "b19"}},
			}}, nil
		},
	}

	search := findToolByName(Tools(sb), "file_search")
	args := json.RawMessage(`{"pattern":"match","context":2}`)
	result, err := search.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeContent(t, result)
	// Before-context line numbers must count up to the match line for
	// every match, not drift with the match index.
	for _, want := range []string{"a.go:8- b8", "a.go:9- b9", "a.go:10: match one", "a.go:11- a11", "a.go:18- b18", "a.go:19- b19", "a.go:20: match two"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestFileSearchTreeDispatch(t *testing.T) {
	var captured TreeRequest
	sb := &mockSandbox{
		treeFn: func(_ context.Context, req TreeRequest) (TreeResult, error) {
			captured = req
			return TreeResult{Tree: "app/\n  main.py", Files: 1, Dirs: 1}, nil
		},
	}

	search := findToolByName(Tools(sb), "file_search")
	args := json.RawMessage(`{"target":"tree","path":"/app","depth":2}`)
	result, err := search.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Path != "/app" || captured.Depth != 2 {
		t.Errorf("tree request not forwarded: %+v", captured)
	}
	if decodeContent(t, result) != "app/\n  main.py\n\n1 files, 1 directories" {
		t.Errorf("content = %q", decodeContent(t, result))
	}
}

func TestFileSearchRejectsBadTarget(t *testing.T) {
	search := findToolByName(Tools(&mockSandbox{}), "file_search")

	result, err := search.ExecuteRaw(context.Background(), json.RawMessage(`{"target":"bogus","pattern":"x"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, "bogus") {
		t.Errorf("expected unknown-target error, got %q", result.Error)
	}

	result, err = search.ExecuteRaw(context.Background(), json.RawMessage(`{"target":"files"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, "pattern is required") {
		t.Errorf("expected missing-pattern error, got %q", result.Error)
	}
}

func TestFileSearchFilesAcceptsGlobAsPattern(t *testing.T) {
	var captured GlobRequest
	sb := &mockSandbox{
		globFilesFn: func(_ context.Context, req GlobRequest) (GlobResult, error) {
			captured = req
			return GlobResult{Files: []string{"/app/main.py"}}, nil
		},
	}

	search := findToolByName(Tools(sb), "file_search")
	args := json.RawMessage(`{"target":"files","glob":"**/*.py","path":"/app"}`)
	result, err := search.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("glob-as-pattern should be accepted, got error %q", result.Error)
	}
	if captured.Pattern != "**/*.py" {
		t.Errorf("pattern = %q, want glob value forwarded", captured.Pattern)
	}
}

// browserReadTool fetches the registered browser_read tool or fails the test.
func browserReadTestTool(t *testing.T, sb Sandbox) oasis.AnyTool {
	t.Helper()
	tool := findToolByName(Tools(sb), "browser_read")
	if tool == nil {
		t.Fatal("browser_read tool not registered")
	}
	return tool
}

func TestBrowserReadSnapshotDispatch(t *testing.T) {
	var captured SnapshotOpts
	sb := &mockSandbox{
		snapshotFn: func(_ context.Context, opts SnapshotOpts) (PageSnapshot, error) {
			captured = opts
			return PageSnapshot{
				URL:   "https://example.com",
				Title: "Example",
				Nodes: []SnapshotNode{
					{Ref: "e0", Role: "link", Name: "Home"},
					{Ref: "e1", Role: "button", Name: "Submit"},
				},
			}, nil
		},
	}

	tool := browserReadTestTool(t, sb)
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"snapshot","filter":"interactive"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Filter != "interactive" {
		t.Errorf("filter = %q, want %q", captured.Filter, "interactive")
	}
	if !strings.Contains(decodeContent(t, result), "[e0] link \"Home\"") {
		t.Errorf("content missing e0 node: %q", decodeContent(t, result))
	}
	if !strings.Contains(decodeContent(t, result), "[e1] button \"Submit\"") {
		t.Errorf("content missing e1 node: %q", decodeContent(t, result))
	}
	if result.Error != "" {
		t.Errorf("unexpected error: %q", result.Error)
	}
}

func TestBrowserReadTextDispatch(t *testing.T) {
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

	tool := browserReadTestTool(t, sb)
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"text","raw":true,"max_chars":500}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !captured.Raw {
		t.Error("expected raw=true")
	}
	if captured.MaxChars != 500 {
		t.Errorf("max_chars = %d, want 500", captured.MaxChars)
	}
	if decodeContent(t, result) != "Welcome to Example." {
		t.Errorf("content = %q, want %q", decodeContent(t, result), "Welcome to Example.")
	}
}

func TestBrowserReadPDFRequiresPath(t *testing.T) {
	tool := browserReadTestTool(t, &mockSandbox{})
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"pdf"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, "requires 'path'") {
		t.Errorf("error = %q, want path-required (pdf bytes must never be silently discarded)", result.Error)
	}
}

func TestBrowserReadPDFSavesToPath(t *testing.T) {
	sb := &mockSandbox{
		uploads: map[string][]byte{},
		browserPDFFn: func(_ context.Context) ([]byte, error) {
			return []byte("%PDF-1.4-fake"), nil
		},
	}

	tool := browserReadTestTool(t, sb)
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"pdf","path":"/workspace/outputs/page.pdf"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	got := decodeContent(t, result)
	if !strings.Contains(got, "13 bytes") || !strings.Contains(got, "/workspace/outputs/page.pdf") {
		t.Errorf("content = %q, want size + saved path", got)
	}
	if string(sb.uploads["/workspace/outputs/page.pdf"]) != "%PDF-1.4-fake" {
		t.Errorf("uploaded = %q, want raw pdf bytes in sandbox", sb.uploads["/workspace/outputs/page.pdf"])
	}
}

func TestBrowserReadScreenshotDispatch(t *testing.T) {
	sb := &mockSandbox{
		screenshotFn: func(_ context.Context) ([]byte, error) {
			return []byte("fake-png-data"), nil
		},
	}

	tool := browserReadTestTool(t, sb)
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"screenshot"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(decodeContent(t, result), "13 bytes") {
		t.Errorf("content = %q, want size info", decodeContent(t, result))
	}
	if len(result.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 (the PNG must reach the LLM)", len(result.Attachments))
	}
	att := result.Attachments[0]
	if att.MimeType != "image/png" || string(att.Data) != "fake-png-data" {
		t.Errorf("attachment = %q %d bytes, want image/png with raw screenshot bytes", att.MimeType, len(att.Data))
	}
}

func TestBrowserReadScreenshotSavesToPath(t *testing.T) {
	sb := &mockSandbox{
		uploads: map[string][]byte{},
		screenshotFn: func(_ context.Context) ([]byte, error) {
			return []byte("fake-png-data"), nil
		},
	}

	tool := browserReadTestTool(t, sb)
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"screenshot","path":"/workspace/outputs/shot.png"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(decodeContent(t, result), "saved to /workspace/outputs/shot.png") {
		t.Errorf("content = %q, want saved-path mention", decodeContent(t, result))
	}
	if string(sb.uploads["/workspace/outputs/shot.png"]) != "fake-png-data" {
		t.Errorf("uploaded = %q, want raw png bytes in sandbox", sb.uploads["/workspace/outputs/shot.png"])
	}
	// The vision attachment must survive the save.
	if len(result.Attachments) != 1 || result.Attachments[0].MimeType != "image/png" {
		t.Fatalf("attachments = %v, want the PNG still attached", result.Attachments)
	}
}

// A screenshot saved under a writable mount must publish to the mount backend,
// same as file_write — that is what makes it reachable by the host app/user.
func TestBrowserReadScreenshotPublishesToMount(t *testing.T) {
	mount := newFakeMount()
	sb := &mockSandbox{
		uploads: map[string][]byte{},
		screenshotFn: func(_ context.Context) ([]byte, error) {
			return []byte("fake-png-data"), nil
		},
	}

	tool := findToolByName(Tools(sb, WithMounts([]MountSpec{{
		Path:    "/workspace/outputs",
		Backend: mount,
		Mode:    MountReadWrite,
	}}, NewManifest())), "browser_read")
	if tool == nil {
		t.Fatal("browser_read tool not registered")
	}

	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"screenshot","path":"/workspace/outputs/shot.png"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	entry, ok := mount.entries["shot.png"]
	if !ok || string(entry.data) != "fake-png-data" {
		t.Errorf("mount entry = %v %q, want published png bytes", ok, entry.data)
	}
	if entry.mime != "image/png" {
		t.Errorf("mime = %q, want image/png", entry.mime)
	}
}

func TestBrowserReadUnknownActionErrors(t *testing.T) {
	tool := browserReadTestTool(t, &mockSandbox{})
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"dom"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Error, `unknown action "dom"`) {
		t.Errorf("error = %q, want unknown-action", result.Error)
	}
}

func TestBrowserToolEvalDispatch(t *testing.T) {
	var captured string
	sb := &mockSandbox{
		browserEvalFn: func(_ context.Context, expression string) (string, error) {
			captured = expression
			return "Example Title", nil
		},
	}

	tool := findToolByName(Tools(sb), "browser")
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"eval","expression":"document.title"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured != "document.title" {
		t.Errorf("expression = %q", captured)
	}
	if decodeContent(t, result) != "Example Title" {
		t.Errorf("content = %q", decodeContent(t, result))
	}

	missing, _ := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"eval"}`))
	if !strings.Contains(missing.Error, "requires 'expression'") {
		t.Errorf("error = %q, want expression-required", missing.Error)
	}
}

func TestBrowserToolFindDispatch(t *testing.T) {
	var captured string
	sb := &mockSandbox{
		browserFindFn: func(_ context.Context, query string) (BrowserFindResult, error) {
			captured = query
			return BrowserFindResult{Ref: "e7", Confidence: "high", Score: 0.92}, nil
		},
	}

	tool := findToolByName(Tools(sb), "browser")
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"find","query":"submit button"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured != "submit button" {
		t.Errorf("query = %q", captured)
	}
	if got := decodeContent(t, result); !strings.Contains(got, "ref: e7") || !strings.Contains(got, "high") {
		t.Errorf("content = %q", got)
	}
}

// TestBrowserSchemaCarriesActionEnums guards the enum struct tags: the derived
// browser schema must advertise all 13 actions, browser_read all 4.
func TestBrowserSchemaCarriesActionEnums(t *testing.T) {
	type actionSchema struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	for name, wantEnum := range map[string]int{"browser": 13, "browser_read": 4} {
		tool := findToolByName(Tools(&mockSandbox{}), name)
		if tool == nil {
			t.Fatalf("%s not registered", name)
		}
		var s actionSchema
		if err := json.Unmarshal(tool.Definition().Parameters, &s); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		if len(s.Properties.Action.Enum) != wantEnum {
			t.Errorf("%s action enum has %d values, want %d", name, len(s.Properties.Action.Enum), wantEnum)
		}
		if len(s.Required) != 1 || s.Required[0] != "action" {
			t.Errorf("%s required = %v, want [action]", name, s.Required)
		}
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
		def := tool.Definition()

		if def.Name == "browser" {
			args := json.RawMessage(`{"action":"click","ref":"e5"}`)
			result, err := tool.ExecuteRaw(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.Ref != "e5" {
				t.Errorf("ref = %q, want %q", captured.Ref, "e5")
			}
			if captured.Type != "click" {
				t.Errorf("type = %q, want %q", captured.Type, "click")
			}
			if decodeContent(t, result) != "clicked" {
				t.Errorf("content = %q, want %q", decodeContent(t, result), "clicked")
			}
		}
		_ = def

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
		def := tool.Definition()

		if def.Name == "deliver_file" {
			found = true

			// Test ExecuteStream path.
			st, ok := tool.(oasis.StreamingAnyTool)
			if !ok {
				t.Fatal("deliver_file tool does not implement StreamingAnyTool")
			}

			ch := make(chan oasis.StreamEvent, 10)
			args := json.RawMessage(`{"path":"/workspace/report.pdf","name":"My Report.pdf"}`)
			result, err := st.ExecuteStream(context.Background(), args, ch)
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
			if !strings.Contains(decodeContent(t, result), "delivered My Report.pdf") {
				t.Errorf("result content = %q, want to contain %q", decodeContent(t, result), "delivered My Report.pdf")
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
		_ = def

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
		def := tool.Definition()

		if def.Name == "deliver_file" {
			// Call without "name" field — should default to basename of path.
			args := json.RawMessage(`{"path":"/workspace/output/chart.png"}`)
			result, err := tool.ExecuteRaw(context.Background(), args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if capturedName != "chart.png" {
				t.Errorf("delivery name = %q, want %q", capturedName, "chart.png")
			}
			if !strings.Contains(decodeContent(t, result), "delivered chart.png") {
				t.Errorf("result content = %q, want to contain %q", decodeContent(t, result), "delivered chart.png")
			}
		}
		_ = def

	}
}

func TestDeliverFileToolNotRegisteredWithoutDelivery(t *testing.T) {
	sb := &mockSandbox{}
	tools := Tools(sb) // no WithFileDelivery, no WithMounts

	for _, tool := range tools {
		def := tool.Definition()

		if def.Name == "deliver_file" {
			t.Error("deliver_file tool should not be registered without any destination")
		}
		_ = def

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
	res, err := deliver.ExecuteRaw(context.Background(), args)
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
	res, err := deliver.ExecuteRaw(context.Background(), args)
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
	res, err := deliver.ExecuteRaw(context.Background(), args)
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

func findToolByName(tools []oasis.AnyTool, name string) oasis.AnyTool {
	for _, tl := range tools {
		if tl.Definition().Name == name {
			return tl
		}
	}
	return nil
}

func TestTools_WithoutBrowserOmitsBrowserTools(t *testing.T) {
	sb := &mockSandbox{}
	browserNames := map[string]bool{"browser": true, "browser_read": true}

	full := Tools(sb)
	var fullHasBrowser bool
	for _, tl := range full {
		if browserNames[tl.Definition().Name] {
			fullHasBrowser = true
		}
	}
	if !fullHasBrowser {
		t.Fatal("baseline Tools() should include browser tools")
	}

	light := Tools(sb, WithoutBrowser())
	for _, tl := range light {
		if browserNames[tl.Definition().Name] {
			t.Errorf("WithoutBrowser() leaked browser tool %q", tl.Definition().Name)
		}
	}
	var hasShell, hasWebSearch bool
	for _, tl := range light {
		switch tl.Definition().Name {
		case "shell":
			hasShell = true
		case "web_search":
			hasWebSearch = true
		}
	}
	if !hasShell || !hasWebSearch {
		t.Errorf("WithoutBrowser() dropped non-browser tools: shell=%v web_search=%v", hasShell, hasWebSearch)
	}
}

func TestTools_NonBrowserSandboxOmitsBrowserTools(t *testing.T) {
	browserNames := map[string]bool{"browser": true, "browser_read": true}

	tools := Tools(lightSandbox{})
	for _, tl := range tools {
		if browserNames[tl.Definition().Name] {
			t.Errorf("non-browser sandbox registered browser tool %q", tl.Definition().Name)
		}
	}
	// 10 core tools, no browser tools, no deliver_file (no destination).
	if len(tools) != 10 {
		t.Errorf("got %d tools for non-browser sandbox, want 10", len(tools))
	}

	// A browser-capable sandbox still gets the full set.
	if got := len(Tools(&mockSandbox{})); got != 12 {
		t.Errorf("got %d tools for browser sandbox, want 12", got)
	}
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
	res, err := write.ExecuteRaw(context.Background(), args)
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
	res, err := write.ExecuteRaw(context.Background(), args)
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
	res, err := write.ExecuteRaw(context.Background(), args)
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
	res, err := edit.ExecuteRaw(context.Background(), args)
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
	res, err := write.ExecuteRaw(context.Background(), args)
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

func TestBrowserWaitToolDispatch(t *testing.T) {
	var captured BrowserWaitOpts
	sb := &mockSandbox{
		browserWaitFn: func(_ context.Context, opts BrowserWaitOpts) (BrowserWaitResult, error) {
			captured = opts
			return BrowserWaitResult{Satisfied: true, Kind: opts.Kind, ElapsedMs: 840}, nil
		},
	}

	tool := findToolByName(Tools(sb), "browser")
	if tool == nil {
		t.Fatal("browser tool not registered")
	}
	args := json.RawMessage(`{"action":"wait","wait_kind":"selector","wait_value":"#login","timeout_ms":5000,"state":"visible"}`)
	result, err := tool.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured.Kind != "selector" || captured.Value != "#login" ||
		captured.TimeoutMs != 5000 || captured.State != "visible" {
		t.Errorf("opts not forwarded: %+v", captured)
	}
	if got := decodeContent(t, result); got != "condition met (selector) after 840ms" {
		t.Errorf("content = %q", got)
	}
}

func TestBrowserWaitToolRendersTimeout(t *testing.T) {
	sb := &mockSandbox{
		browserWaitFn: func(_ context.Context, opts BrowserWaitOpts) (BrowserWaitResult, error) {
			return BrowserWaitResult{
				Satisfied: false,
				Kind:      opts.Kind,
				ElapsedMs: 10000,
				Detail:    "timeout after 10000ms waiting for selector",
			}, nil
		},
	}

	tool := findToolByName(Tools(sb), "browser")
	if tool == nil {
		t.Fatal("browser tool not registered")
	}
	result, err := tool.ExecuteRaw(context.Background(), json.RawMessage(`{"action":"wait","wait_kind":"selector","wait_value":"#x"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeContent(t, result)
	if !strings.Contains(got, "NOT met") || !strings.Contains(got, "browser_read(action='snapshot')") {
		t.Errorf("content = %q, want NOT met + snapshot hint", got)
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
