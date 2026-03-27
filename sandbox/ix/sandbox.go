package ix

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nevindra/oasis/sandbox"
)

// IXSandbox implements sandbox.Sandbox by proxying each method call to an
// ix daemon container via its REST + SSE API.
type IXSandbox struct {
	id           string
	containerID  string
	baseURL      string
	client       *ixClient
	networkID    string
	createdAt    time.Time
	expiresAt    time.Time
	failCount    int
	restartCount int
	closed       atomic.Int32
}

// errClosed is returned when a method is called on a closed sandbox.
var errClosed = fmt.Errorf("sandbox closed")

func (s *IXSandbox) checkClosed() error {
	if s.closed.Load() != 0 {
		return errClosed
	}
	return nil
}

// Shell executes a shell command inside the sandbox container via SSE stream.
func (s *IXSandbox) Shell(ctx context.Context, req sandbox.ShellRequest) (sandbox.ShellResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.ShellResult{}, err
	}
	body := map[string]any{
		"command": req.Command,
	}
	if req.Cwd != "" {
		body["cwd"] = req.Cwd
	}
	if req.Timeout > 0 {
		body["timeout"] = req.Timeout
	}

	reader, err := s.client.postSSE(ctx, "/v1/shell/exec", body)
	if err != nil {
		return sandbox.ShellResult{}, fmt.Errorf("shell exec: %w", err)
	}
	defer reader.Close()

	var output strings.Builder
	for reader.Next() {
		switch reader.Event() {
		case "stdout", "stderr":
			var text struct {
				Text string `json:"text"`
			}
			json.Unmarshal([]byte(reader.Data()), &text)
			output.WriteString(text.Text)
		case "error":
			var errData struct {
				Text string `json:"text"`
				Code int    `json:"code"`
			}
			json.Unmarshal([]byte(reader.Data()), &errData)
			return sandbox.ShellResult{Output: errData.Text, ExitCode: errData.Code}, nil
		case "complete":
			var complete struct {
				ExitCode  int `json:"exit_code"`
				ElapsedMs int `json:"elapsed_ms"`
			}
			json.Unmarshal([]byte(reader.Data()), &complete)
			return sandbox.ShellResult{Output: output.String(), ExitCode: complete.ExitCode}, nil
		}
	}
	if reader.Err() != nil {
		return sandbox.ShellResult{}, fmt.Errorf("sse stream: %w", reader.Err())
	}
	return sandbox.ShellResult{Output: output.String()}, nil
}

// ExecCode executes code in a language-specific runtime via SSE stream.
func (s *IXSandbox) ExecCode(ctx context.Context, req sandbox.CodeRequest) (sandbox.CodeResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.CodeResult{}, err
	}
	body := map[string]any{
		"language": req.Language,
		"code":     req.Code,
	}
	if req.Timeout > 0 {
		body["timeout"] = req.Timeout
	}

	reader, err := s.client.postSSE(ctx, "/v1/code/execute", body)
	if err != nil {
		return sandbox.CodeResult{}, fmt.Errorf("exec code: %w", err)
	}
	defer reader.Close()

	var stdout, stderr strings.Builder
	for reader.Next() {
		switch reader.Event() {
		case "stdout":
			var text struct {
				Text string `json:"text"`
			}
			json.Unmarshal([]byte(reader.Data()), &text)
			stdout.WriteString(text.Text)
		case "stderr":
			var text struct {
				Text string `json:"text"`
			}
			json.Unmarshal([]byte(reader.Data()), &text)
			stderr.WriteString(text.Text)
		case "error":
			var errData struct {
				Text string `json:"text"`
				Code int    `json:"code"`
			}
			json.Unmarshal([]byte(reader.Data()), &errData)
			return sandbox.CodeResult{
				Status: "error",
				Stdout: stdout.String(),
				Stderr: errData.Text,
			}, nil
		case "complete":
			var complete struct {
				ExitCode  int `json:"exit_code"`
				ElapsedMs int `json:"elapsed_ms"`
			}
			json.Unmarshal([]byte(reader.Data()), &complete)
			status := "ok"
			if complete.ExitCode != 0 {
				status = "error"
			}
			return sandbox.CodeResult{
				Status: status,
				Stdout: stdout.String(),
				Stderr: stderr.String(),
			}, nil
		}
	}
	if reader.Err() != nil {
		return sandbox.CodeResult{}, fmt.Errorf("sse stream: %w", reader.Err())
	}
	return sandbox.CodeResult{
		Status: "ok",
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}, nil
}

// ReadFile reads the content of a file inside the sandbox with line numbers.
func (s *IXSandbox) ReadFile(ctx context.Context, req sandbox.ReadFileRequest) (sandbox.FileContent, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.FileContent{}, err
	}
	body := map[string]any{
		"path": req.Path,
	}
	if req.Offset > 0 {
		body["offset"] = req.Offset
	}
	if req.Limit > 0 {
		body["limit"] = req.Limit
	}
	var resp struct {
		Content    string `json:"content"`
		Path       string `json:"path"`
		TotalLines int    `json:"total_lines"`
	}
	if err := s.client.post(ctx, "/v1/file/read", body, &resp); err != nil {
		return sandbox.FileContent{}, fmt.Errorf("read file: %w", err)
	}
	return sandbox.FileContent{
		Content:    resp.Content,
		Path:       resp.Path,
		TotalLines: resp.TotalLines,
	}, nil
}

// WriteFile writes text content to a file inside the sandbox.
func (s *IXSandbox) WriteFile(ctx context.Context, req sandbox.WriteFileRequest) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	body := map[string]string{
		"path":    req.Path,
		"content": req.Content,
	}
	var resp struct {
		BytesWritten int `json:"bytes_written"`
	}
	if err := s.client.post(ctx, "/v1/file/write", body, &resp); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// UploadFile uploads binary data to a file path inside the sandbox.
func (s *IXSandbox) UploadFile(ctx context.Context, path string, data io.Reader) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if err := s.client.upload(ctx, "/v1/file/upload", path, data); err != nil {
		return fmt.Errorf("upload file: %w", err)
	}
	return nil
}

// DownloadFile returns a reader for a file inside the sandbox.
func (s *IXSandbox) DownloadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	rc, err := s.client.getRaw(ctx, "/v1/file/download?path="+url.QueryEscape(path))
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	return rc, nil
}

// BrowserNavigate navigates the sandbox browser to a URL.
func (s *IXSandbox) BrowserNavigate(ctx context.Context, targetURL string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	body := map[string]string{"url": targetURL}
	var resp struct {
		TabID string `json:"tabId"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := s.client.post(ctx, "/v1/browser/navigate", body, &resp); err != nil {
		return fmt.Errorf("browser navigate: %w", err)
	}
	return nil
}

// BrowserScreenshot captures a screenshot of the sandbox browser.
func (s *IXSandbox) BrowserScreenshot(ctx context.Context) ([]byte, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	rc, err := s.client.getRaw(ctx, "/v1/browser/screenshot")
	if err != nil {
		return nil, fmt.Errorf("browser screenshot: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read screenshot: %w", err)
	}
	return data, nil
}

// BrowserAction sends an interaction to the sandbox browser.
func (s *IXSandbox) BrowserAction(ctx context.Context, action sandbox.BrowserAction) (sandbox.BrowserResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.BrowserResult{}, err
	}
	body := map[string]any{
		"kind": action.Type,
	}
	if action.Ref != "" {
		body["ref"] = action.Ref
	}
	if action.X != 0 || action.Y != 0 {
		body["x"] = action.X
		body["y"] = action.Y
	}
	if action.Text != "" {
		body["text"] = action.Text
	}
	if action.Key != "" {
		body["key"] = action.Key
	}
	var resp struct {
		Success bool `json:"success"`
		Result  struct {
			Success bool `json:"success"`
		} `json:"result"`
	}
	if err := s.client.post(ctx, "/v1/browser/action", body, &resp); err != nil {
		return sandbox.BrowserResult{}, fmt.Errorf("browser action: %w", err)
	}
	return sandbox.BrowserResult{
		Success: resp.Success,
	}, nil
}

// MCPCall invokes a tool on an MCP server running inside the sandbox.
func (s *IXSandbox) MCPCall(ctx context.Context, req sandbox.MCPRequest) (sandbox.MCPResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.MCPResult{}, err
	}
	path := fmt.Sprintf("/v1/mcp/%s/tools/%s", req.Server, req.Tool)
	var resp struct {
		Content string `json:"content"`
		IsError bool   `json:"is_error"`
	}
	if err := s.client.post(ctx, path, req.Args, &resp); err != nil {
		return sandbox.MCPResult{}, fmt.Errorf("mcp call: %w", err)
	}
	return sandbox.MCPResult{
		Content: resp.Content,
	}, nil
}

// EditFile performs a surgical string replacement in a file.
func (s *IXSandbox) EditFile(ctx context.Context, req sandbox.EditFileRequest) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	body := map[string]string{
		"path": req.Path,
		"old":  req.Old,
		"new":  req.New,
	}
	var resp struct {
		Applied bool   `json:"applied"`
		Path    string `json:"path"`
	}
	if err := s.client.post(ctx, "/v1/file/edit", body, &resp); err != nil {
		return fmt.Errorf("edit file: %w", err)
	}
	return nil
}

// GlobFiles finds files matching a glob pattern.
func (s *IXSandbox) GlobFiles(ctx context.Context, req sandbox.GlobRequest) (sandbox.GlobResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.GlobResult{}, err
	}
	body := map[string]any{
		"pattern": req.Pattern,
	}
	if req.Path != "" {
		body["path"] = req.Path
	}
	if len(req.Exclude) > 0 {
		body["exclude"] = req.Exclude
	}
	if req.Limit > 0 {
		body["limit"] = req.Limit
	}
	var resp struct {
		Files     []string `json:"files"`
		Truncated bool     `json:"truncated"`
	}
	if err := s.client.post(ctx, "/v1/file/glob", body, &resp); err != nil {
		return sandbox.GlobResult{}, fmt.Errorf("glob files: %w", err)
	}
	return sandbox.GlobResult{Files: resp.Files, Truncated: resp.Truncated}, nil
}

// GrepFiles searches file contents for a regex pattern.
func (s *IXSandbox) GrepFiles(ctx context.Context, req sandbox.GrepRequest) (sandbox.GrepResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.GrepResult{}, err
	}
	body := map[string]any{
		"pattern": req.Pattern,
	}
	if req.Path != "" {
		body["path"] = req.Path
	}
	if req.Glob != "" {
		body["glob"] = req.Glob
	}
	if req.Context > 0 {
		body["context"] = req.Context
	}
	if req.Limit > 0 {
		body["limit"] = req.Limit
	}
	var resp struct {
		Matches []struct {
			Path          string   `json:"path"`
			Line          int      `json:"line"`
			Content       string   `json:"content"`
			ContextBefore []string `json:"context_before"`
			ContextAfter  []string `json:"context_after"`
		} `json:"matches"`
		Truncated bool `json:"truncated"`
	}
	if err := s.client.post(ctx, "/v1/file/grep", body, &resp); err != nil {
		return sandbox.GrepResult{}, fmt.Errorf("grep files: %w", err)
	}
	matches := make([]sandbox.GrepMatch, len(resp.Matches))
	for i, m := range resp.Matches {
		matches[i] = sandbox.GrepMatch{
			Path:          m.Path,
			Line:          m.Line,
			Content:       m.Content,
			ContextBefore: m.ContextBefore,
			ContextAfter:  m.ContextAfter,
		}
	}
	return sandbox.GrepResult{Matches: matches, Truncated: resp.Truncated}, nil
}

// Tree returns a recursive directory listing.
func (s *IXSandbox) Tree(ctx context.Context, req sandbox.TreeRequest) (sandbox.TreeResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.TreeResult{}, err
	}
	body := map[string]any{}
	if req.Path != "" {
		body["path"] = req.Path
	}
	if req.Depth > 0 {
		body["depth"] = req.Depth
	}
	if len(req.Exclude) > 0 {
		body["exclude"] = req.Exclude
	}
	var resp struct {
		Tree  string `json:"tree"`
		Files int    `json:"files"`
		Dirs  int    `json:"dirs"`
	}
	if err := s.client.post(ctx, "/v1/file/tree", body, &resp); err != nil {
		return sandbox.TreeResult{}, fmt.Errorf("tree: %w", err)
	}
	return sandbox.TreeResult{Tree: resp.Tree, Files: resp.Files, Dirs: resp.Dirs}, nil
}

// HTTPFetch fetches a URL and extracts readable text content.
func (s *IXSandbox) HTTPFetch(ctx context.Context, req sandbox.HTTPFetchRequest) (sandbox.HTTPFetchResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.HTTPFetchResult{}, err
	}
	body := map[string]any{
		"url": req.URL,
	}
	if req.Raw {
		body["raw"] = true
	}
	if req.MaxChars > 0 {
		body["max_chars"] = req.MaxChars
	}
	var resp struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := s.client.post(ctx, "/v1/http/fetch", body, &resp); err != nil {
		return sandbox.HTTPFetchResult{}, fmt.Errorf("http fetch: %w", err)
	}
	return sandbox.HTTPFetchResult{URL: resp.URL, Title: resp.Title, Content: resp.Content}, nil
}

// WorkspaceInfo returns environment information about the sandbox.
func (s *IXSandbox) WorkspaceInfo(ctx context.Context) (sandbox.WorkspaceInfoResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.WorkspaceInfoResult{}, err
	}
	var resp sandbox.WorkspaceInfoResult
	if err := s.client.getJSON(ctx, "/v1/workspace/info", &resp); err != nil {
		return sandbox.WorkspaceInfoResult{}, fmt.Errorf("workspace info: %w", err)
	}
	return resp, nil
}

// BrowserSnapshot returns the accessibility tree of the current page.
func (s *IXSandbox) BrowserSnapshot(ctx context.Context, opts sandbox.SnapshotOpts) (sandbox.BrowserSnapshot, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.BrowserSnapshot{}, err
	}

	q := make(url.Values)
	if opts.Filter != "" {
		q.Set("filter", opts.Filter)
	}
	if opts.Selector != "" {
		q.Set("selector", opts.Selector)
	}
	if opts.Depth > 0 {
		q.Set("depth", fmt.Sprintf("%d", opts.Depth))
	}

	path := "/v1/browser/snapshot"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var resp struct {
		URL   string `json:"url"`
		Title string `json:"title"`
		Nodes []struct {
			Ref  string `json:"ref"`
			Role string `json:"role"`
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := s.client.getJSON(ctx, path, &resp); err != nil {
		return sandbox.BrowserSnapshot{}, fmt.Errorf("browser snapshot: %w", err)
	}

	nodes := make([]sandbox.SnapshotNode, len(resp.Nodes))
	for i, n := range resp.Nodes {
		nodes[i] = sandbox.SnapshotNode{
			Ref:  n.Ref,
			Role: n.Role,
			Name: n.Name,
		}
	}
	return sandbox.BrowserSnapshot{
		URL:   resp.URL,
		Title: resp.Title,
		Nodes: nodes,
	}, nil
}

// BrowserText extracts readable text content from the current page.
func (s *IXSandbox) BrowserText(ctx context.Context, opts sandbox.TextOpts) (sandbox.BrowserTextResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.BrowserTextResult{}, err
	}

	q := make(url.Values)
	if opts.Raw {
		q.Set("raw", "true")
	}
	if opts.MaxChars > 0 {
		q.Set("maxChars", fmt.Sprintf("%d", opts.MaxChars))
	}

	path := "/v1/browser/text"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	var resp struct {
		URL       string `json:"url"`
		Title     string `json:"title"`
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if err := s.client.getJSON(ctx, path, &resp); err != nil {
		return sandbox.BrowserTextResult{}, fmt.Errorf("browser text: %w", err)
	}
	return sandbox.BrowserTextResult{
		URL:       resp.URL,
		Title:     resp.Title,
		Text:      resp.Text,
		Truncated: resp.Truncated,
	}, nil
}

// BrowserPDF exports the current page as a PDF document.
func (s *IXSandbox) BrowserPDF(ctx context.Context) ([]byte, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	rc, err := s.client.getRaw(ctx, "/v1/browser/pdf")
	if err != nil {
		return nil, fmt.Errorf("browser pdf: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read pdf: %w", err)
	}
	return data, nil
}

// Close releases resources held by this sandbox instance.
func (s *IXSandbox) Close() error {
	s.closed.Store(1)
	return nil
}

// healthCheck checks if the ix daemon is responding.
// Returns true if the health endpoint returns HTTP 200 within 3 seconds.
func (s *IXSandbox) healthCheck(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := s.client.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Compile-time interface check.
var _ sandbox.Sandbox = (*IXSandbox)(nil)
