package ix

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nevindra/oasis/sandbox"
)

// ixMux returns a *http.ServeMux that simulates ix daemon responses.
func ixMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()

	// Shell exec (SSE)
	mux.HandleFunc("POST /v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
			Timeout int    `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("shell exec: decode body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		fmt.Fprintf(w, "event: stdout\ndata: {\"text\": \"hello world\\n\"}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: complete\ndata: {\"exit_code\": 0, \"elapsed_ms\": 50}\n\n")
		flusher.Flush()
	})

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "uptime_sec": 3600})
	})

	// Code execute (SSE)
	mux.HandleFunc("POST /v1/code/execute", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Language string `json:"language"`
			Code     string `json:"code"`
			Timeout  int    `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("code execute: decode body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		fmt.Fprintf(w, "event: stdout\ndata: {\"text\": \"42\\n\"}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: complete\ndata: {\"exit_code\": 0, \"elapsed_ms\": 30}\n\n")
		flusher.Flush()
	})

	// File read
	mux.HandleFunc("POST /v1/file/read", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("file read: decode body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"content":     "file content here",
			"path":        req.Path,
			"total_lines": 10,
		})
	})

	// File write
	mux.HandleFunc("POST /v1/file/write", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("file write: decode body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"bytes_written": len(req.Content)})
	})

	// File upload
	mux.HandleFunc("POST /v1/file/upload", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("file upload: parse multipart: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"bytes_written": 0})
	})

	// File download
	mux.HandleFunc("GET /v1/file/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte("downloaded content"))
	})

	// File edit
	mux.HandleFunc("POST /v1/file/edit", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
			Old  string `json:"old"`
			New  string `json:"new"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("file edit: decode body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"applied": true, "path": req.Path})
	})

	// File glob
	mux.HandleFunc("POST /v1/file/glob", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"files": []string{"/app/main.py", "/app/lib/utils.py"},
		})
	})

	// File grep
	mux.HandleFunc("POST /v1/file/grep", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"matches": []map[string]any{
				{"path": "/app/main.py", "line": 42, "content": "def main():"},
			},
		})
	})

	// Browser navigate
	mux.HandleFunc("POST /v1/browser/navigate", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"tabId": "tab-123",
			"url":   req.URL,
			"title": "Example Page",
		})
	})

	// Browser action
	mux.HandleFunc("POST /v1/browser/action", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"result":  map[string]any{"success": true},
		})
	})

	// Browser screenshot
	mux.HandleFunc("GET /v1/browser/screenshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("fake-png-data"))
	})

	// Browser snapshot
	mux.HandleFunc("GET /v1/browser/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"url":   "https://example.com",
			"title": "Example",
			"nodes": []map[string]any{
				{"ref": "e0", "role": "link", "name": "Home"},
				{"ref": "e1", "role": "button", "name": "Submit"},
			},
			"count": 2,
		})
	})

	// Browser text
	mux.HandleFunc("GET /v1/browser/text", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"url":       "https://example.com",
			"title":     "Example",
			"text":      "Welcome to Example Domain.",
			"truncated": false,
		})
	})

	// Browser PDF
	mux.HandleFunc("GET /v1/browser/pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-fake-data"))
	})

	// MCP call (wildcard pattern for /v1/mcp/{server}/tools/{tool})
	mux.HandleFunc("POST /v1/mcp/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"content":  "mcp result",
			"is_error": false,
		})
	})

	return mux
}

// newTestSandbox creates an IXSandbox pointing at a test server using ixMux.
func newTestSandbox(t *testing.T) (*IXSandbox, *httptest.Server) {
	t.Helper()
	mux := ixMux(t)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	s := &IXSandbox{
		id:          "test-session",
		containerID: "test-container-123",
		baseURL:     srv.URL,
		client:      newClient(srv.URL, srv.Client()),
		networkID:   "test-network",
		createdAt:   time.Now(),
		expiresAt:   time.Now().Add(time.Hour),
	}
	return s, srv
}

func TestIXSandboxShell(t *testing.T) {
	s, _ := newTestSandbox(t)

	res, err := s.Shell(context.Background(), sandbox.ShellRequest{
		Command: "echo hello world",
		Cwd:     "/workspace",
		Timeout: 30,
	})
	if err != nil {
		t.Fatalf("Shell() returned error: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", res.ExitCode)
	}
	if res.Output != "hello world\n" {
		t.Errorf("expected output %q, got %q", "hello world\n", res.Output)
	}
}

func TestIXSandboxExecCode(t *testing.T) {
	s, _ := newTestSandbox(t)

	res, err := s.ExecCode(context.Background(), sandbox.CodeRequest{
		Language: "python",
		Code:     "print(42)",
		Timeout:  10,
	})
	if err != nil {
		t.Fatalf("ExecCode() returned error: %v", err)
	}
	if res.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", res.Status)
	}
	if res.Stdout != "42\n" {
		t.Errorf("expected stdout %q, got %q", "42\n", res.Stdout)
	}
	if res.Stderr != "" {
		t.Errorf("expected empty stderr, got %q", res.Stderr)
	}
}

func TestIXSandboxReadFile(t *testing.T) {
	s, _ := newTestSandbox(t)

	fc, err := s.ReadFile(context.Background(), "/workspace/test.txt")
	if err != nil {
		t.Fatalf("ReadFile() returned error: %v", err)
	}
	if fc.Content != "file content here" {
		t.Errorf("expected content %q, got %q", "file content here", fc.Content)
	}
	if fc.Path != "/workspace/test.txt" {
		t.Errorf("expected path %q, got %q", "/workspace/test.txt", fc.Path)
	}
}

func TestIXSandboxWriteFile(t *testing.T) {
	s, _ := newTestSandbox(t)

	err := s.WriteFile(context.Background(), sandbox.WriteFileRequest{
		Path:    "/workspace/out.txt",
		Content: "new content",
	})
	if err != nil {
		t.Fatalf("WriteFile() returned error: %v", err)
	}
}

func TestIXSandboxUploadFile(t *testing.T) {
	s, _ := newTestSandbox(t)

	err := s.UploadFile(context.Background(), "/workspace/upload.bin", strings.NewReader("binary data"))
	if err != nil {
		t.Fatalf("UploadFile() returned error: %v", err)
	}
}

func TestIXSandboxDownloadFile(t *testing.T) {
	s, _ := newTestSandbox(t)

	rc, err := s.DownloadFile(context.Background(), "/workspace/file.txt")
	if err != nil {
		t.Fatalf("DownloadFile() returned error: %v", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read download body: %v", err)
	}
	if string(data) != "downloaded content" {
		t.Errorf("expected %q, got %q", "downloaded content", string(data))
	}
}

func TestIXSandboxBrowserScreenshot(t *testing.T) {
	s, _ := newTestSandbox(t)

	data, err := s.BrowserScreenshot(context.Background())
	if err != nil {
		t.Fatalf("BrowserScreenshot() returned error: %v", err)
	}
	if string(data) != "fake-png-data" {
		t.Errorf("expected %q, got %q", "fake-png-data", string(data))
	}
}

func TestIXSandboxBrowserNavigate(t *testing.T) {
	s, _ := newTestSandbox(t)

	err := s.BrowserNavigate(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("BrowserNavigate() returned error: %v", err)
	}
}

func TestIXSandboxBrowserAction(t *testing.T) {
	s, _ := newTestSandbox(t)

	res, err := s.BrowserAction(context.Background(), sandbox.BrowserAction{
		Type: "click",
		Ref:  "e5",
	})
	if err != nil {
		t.Fatalf("BrowserAction() returned error: %v", err)
	}
	if !res.Success {
		t.Error("expected success=true")
	}
}

func TestIXSandboxBrowserSnapshot(t *testing.T) {
	s, _ := newTestSandbox(t)

	snap, err := s.BrowserSnapshot(context.Background(), sandbox.SnapshotOpts{
		Filter: "interactive",
	})
	if err != nil {
		t.Fatalf("BrowserSnapshot() returned error: %v", err)
	}
	if snap.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", snap.URL, "https://example.com")
	}
	if snap.Title != "Example" {
		t.Errorf("Title = %q, want %q", snap.Title, "Example")
	}
	if len(snap.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(snap.Nodes))
	}
	if snap.Nodes[0].Ref != "e0" {
		t.Errorf("Nodes[0].Ref = %q, want %q", snap.Nodes[0].Ref, "e0")
	}
	if snap.Nodes[1].Role != "button" {
		t.Errorf("Nodes[1].Role = %q, want %q", snap.Nodes[1].Role, "button")
	}
}

func TestIXSandboxBrowserText(t *testing.T) {
	s, _ := newTestSandbox(t)

	result, err := s.BrowserText(context.Background(), sandbox.TextOpts{Raw: false})
	if err != nil {
		t.Fatalf("BrowserText() returned error: %v", err)
	}
	if result.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", result.URL, "https://example.com")
	}
	if result.Text != "Welcome to Example Domain." {
		t.Errorf("Text = %q, want %q", result.Text, "Welcome to Example Domain.")
	}
	if result.Truncated {
		t.Error("expected truncated=false")
	}
}

func TestIXSandboxBrowserPDF(t *testing.T) {
	s, _ := newTestSandbox(t)

	data, err := s.BrowserPDF(context.Background())
	if err != nil {
		t.Fatalf("BrowserPDF() returned error: %v", err)
	}
	if string(data) != "%PDF-fake-data" {
		t.Errorf("got %q, want %q", string(data), "%PDF-fake-data")
	}
}

func TestIXSandboxMCPCall(t *testing.T) {
	s, _ := newTestSandbox(t)

	res, err := s.MCPCall(context.Background(), sandbox.MCPRequest{
		Server: "test-server",
		Tool:   "test-tool",
		Args:   map[string]any{"key": "value"},
	})
	if err != nil {
		t.Fatalf("MCPCall() returned error: %v", err)
	}
	if res.Content != "mcp result" {
		t.Errorf("expected content %q, got %q", "mcp result", res.Content)
	}
}

func TestIXSandboxEditFile(t *testing.T) {
	s, _ := newTestSandbox(t)

	err := s.EditFile(context.Background(), sandbox.EditFileRequest{
		Path: "/workspace/main.py",
		Old:  "print('hello')",
		New:  "print('hello world')",
	})
	if err != nil {
		t.Fatalf("EditFile() returned error: %v", err)
	}
}

func TestIXSandboxGlobFiles(t *testing.T) {
	s, _ := newTestSandbox(t)

	files, err := s.GlobFiles(context.Background(), sandbox.GlobRequest{
		Pattern: "**/*.py",
		Path:    "/app",
	})
	if err != nil {
		t.Fatalf("GlobFiles() returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0] != "/app/main.py" {
		t.Errorf("expected first file '/app/main.py', got %q", files[0])
	}
}

func TestIXSandboxGrepFiles(t *testing.T) {
	s, _ := newTestSandbox(t)

	matches, err := s.GrepFiles(context.Background(), sandbox.GrepRequest{
		Pattern: "def main",
		Path:    "/app",
		Glob:    "*.py",
	})
	if err != nil {
		t.Fatalf("GrepFiles() returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Path != "/app/main.py" {
		t.Errorf("expected path '/app/main.py', got %q", matches[0].Path)
	}
	if matches[0].Line != 42 {
		t.Errorf("expected line 42, got %d", matches[0].Line)
	}
}

func TestIXSandboxClosedError(t *testing.T) {
	s, _ := newTestSandbox(t)
	s.Close()

	_, err := s.Shell(context.Background(), sandbox.ShellRequest{Command: "echo hi"})
	if err == nil {
		t.Fatal("expected error after Close(), got nil")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Errorf("expected error to mention 'closed', got: %v", err)
	}
}

func TestIXSandboxHealthCheck(t *testing.T) {
	s, _ := newTestSandbox(t)

	ok := s.healthCheck(context.Background())
	if !ok {
		t.Error("expected healthCheck to return true")
	}
}

func TestIXSandboxShellWithError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/shell/exec", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		fmt.Fprintf(w, "event: stderr\ndata: {\"text\": \"command not found\\n\"}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: error\ndata: {\"text\": \"command not found: foo\", \"code\": 127}\n\n")
		flusher.Flush()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &IXSandbox{
		id:      "test-error",
		baseURL: srv.URL,
		client:  newClient(srv.URL, srv.Client()),
	}

	res, err := s.Shell(context.Background(), sandbox.ShellRequest{Command: "foo"})
	if err != nil {
		t.Fatalf("Shell() returned error: %v", err)
	}
	if res.ExitCode != 127 {
		t.Errorf("expected exit code 127, got %d", res.ExitCode)
	}
	if res.Output != "command not found: foo" {
		t.Errorf("expected output %q, got %q", "command not found: foo", res.Output)
	}
}

func TestIXSandboxExecCodeWithStderr(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/code/execute", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		fmt.Fprintf(w, "event: stdout\ndata: {\"text\": \"partial output\\n\"}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: stderr\ndata: {\"text\": \"NameError: name 'x' is not defined\\n\"}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "event: complete\ndata: {\"exit_code\": 1, \"elapsed_ms\": 20}\n\n")
		flusher.Flush()
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &IXSandbox{
		id:      "test-stderr",
		baseURL: srv.URL,
		client:  newClient(srv.URL, srv.Client()),
	}

	res, err := s.ExecCode(context.Background(), sandbox.CodeRequest{
		Language: "python",
		Code:     "print(x)",
	})
	if err != nil {
		t.Fatalf("ExecCode() returned error: %v", err)
	}
	if res.Status != "error" {
		t.Errorf("expected status 'error', got %q", res.Status)
	}
	if res.Stdout != "partial output\n" {
		t.Errorf("expected stdout %q, got %q", "partial output\n", res.Stdout)
	}
	if res.Stderr != "NameError: name 'x' is not defined\n" {
		t.Errorf("expected stderr %q, got %q", "NameError: name 'x' is not defined\n", res.Stderr)
	}
}
