# IX Browser Browsing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Pinchtab-powered browser browsing to IX sandbox — accessibility-tree snapshots, text extraction, PDF export, and element-ref-based interactions.

**Architecture:** ix daemon spawns Pinchtab bridge as a subprocess, proxies browser requests to it. IXSandbox client calls ix daemon endpoints. Separate Docker image (`oasis-ix-browser:latest`) adds Chromium + Pinchtab to the base image.

**Tech Stack:** Go 1.25, Docker, Pinchtab (Go binary), Chromium headless

**Spec:** `docs/superpowers/specs/2026-03-26-ix-browser-browsing-design.md`

---

### Task 1: Sandbox Interface — New Types and Enhanced BrowserAction

**Files:**
- Modify: `sandbox/sandbox.go`

- [ ] **Step 1: Add new types after the existing BrowserResult type (line 114)**

Add after the `BrowserResult` struct:

```go
// SnapshotOpts configures a browser snapshot request.
type SnapshotOpts struct {
	Filter   string // "interactive" filters to actionable elements only
	Selector string // CSS selector to scope snapshot to a subtree
	Depth    int    // traversal depth; 0 = unlimited
}

// BrowserSnapshot is the accessibility tree of the current page.
type BrowserSnapshot struct {
	URL   string         // current page URL
	Title string         // page title
	Nodes []SnapshotNode // accessibility tree nodes
}

// SnapshotNode is a single element in the accessibility tree.
type SnapshotNode struct {
	Ref  string // element reference (e.g., "e0") — use in BrowserAction.Ref
	Role string // semantic role (link, button, textbox, heading, etc.)
	Name string // accessible name / visible text
}

// TextOpts configures a browser text extraction request.
type TextOpts struct {
	Raw      bool // true = innerText, false = readability extraction
	MaxChars int  // 0 = unlimited
}

// BrowserTextResult is the output of BrowserText.
type BrowserTextResult struct {
	URL       string
	Title     string
	Text      string
	Truncated bool
}
```

- [ ] **Step 2: Add `Ref` field to BrowserAction**

Replace the existing `BrowserAction` struct:

```go
// BrowserAction describes a browser interaction.
type BrowserAction struct {
	Type string // "click", "type", "scroll", "navigate", "key", "hover", "fill", "press", "select"
	Ref  string // element ref from snapshot (preferred over coordinates)
	X    int    // pixel coordinates (fallback for canvas/maps)
	Y    int
	Text string // text for type/fill, URL for navigate
	Key  string // key name for key/press
}
```

- [ ] **Step 3: Add 3 new methods to the Sandbox interface**

Add after `BrowserAction` method in the interface:

```go
	// BrowserSnapshot returns the accessibility tree of the current page.
	// Each interactive element has a Ref that can be passed to BrowserAction
	// for precise interaction without pixel coordinates.
	BrowserSnapshot(ctx context.Context, opts SnapshotOpts) (BrowserSnapshot, error)

	// BrowserText extracts readable text content from the current page.
	// Uses readability-style extraction by default; set Raw to true for innerText.
	BrowserText(ctx context.Context, opts TextOpts) (BrowserTextResult, error)

	// BrowserPDF exports the current page as a PDF document.
	// Returns raw PDF bytes.
	BrowserPDF(ctx context.Context) ([]byte, error)
```

- [ ] **Step 4: Verify it compiles**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./sandbox/...`
Expected: Compilation errors in `sandbox/ix/sandbox.go` and `sandbox/tools_test.go` because `IXSandbox` and `mockSandbox` don't implement the new methods yet. This is expected — we'll fix them in subsequent tasks.

- [ ] **Step 5: Commit**

```bash
git add sandbox/sandbox.go
git commit -m "feat(sandbox): add browser snapshot, text, PDF types and interface methods"
```

---

### Task 2: Update Mock Sandbox and IXSandbox Stubs

This task makes the code compile again by adding stub implementations for the 3 new interface methods.

**Files:**
- Modify: `sandbox/tools_test.go` (mockSandbox)
- Modify: `sandbox/ix/sandbox.go` (IXSandbox stubs)

- [ ] **Step 1: Add mock functions and stub methods to mockSandbox in tools_test.go**

Add fields to the `mockSandbox` struct (after `mcpCallFn`):

```go
	snapshotFn     func(ctx context.Context, opts SnapshotOpts) (BrowserSnapshot, error)
	browserTextFn  func(ctx context.Context, opts TextOpts) (BrowserTextResult, error)
	browserPDFFn   func(ctx context.Context) ([]byte, error)
```

Add method implementations (after `MCPCall` method):

```go
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
```

- [ ] **Step 2: Add stub methods to IXSandbox in sandbox/ix/sandbox.go**

Add before the `Close()` method:

```go
// BrowserSnapshot returns the accessibility tree of the current page.
func (s *IXSandbox) BrowserSnapshot(ctx context.Context, opts sandbox.SnapshotOpts) (sandbox.BrowserSnapshot, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.BrowserSnapshot{}, err
	}
	return sandbox.BrowserSnapshot{}, fmt.Errorf("browser snapshot: not implemented")
}

// BrowserText extracts readable text content from the current page.
func (s *IXSandbox) BrowserText(ctx context.Context, opts sandbox.TextOpts) (sandbox.BrowserTextResult, error) {
	if err := s.checkClosed(); err != nil {
		return sandbox.BrowserTextResult{}, err
	}
	return sandbox.BrowserTextResult{}, fmt.Errorf("browser text: not implemented")
}

// BrowserPDF exports the current page as a PDF document.
func (s *IXSandbox) BrowserPDF(ctx context.Context) ([]byte, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("browser pdf: not implemented")
}
```

- [ ] **Step 3: Verify everything compiles and tests pass**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./sandbox/... ./sandbox/ix/...`
Expected: All existing tests PASS, no compilation errors.

- [ ] **Step 4: Commit**

```bash
git add sandbox/tools_test.go sandbox/ix/sandbox.go
git commit -m "feat(sandbox): add stub implementations for new browser methods"
```

---

### Task 3: ix Daemon — Pinchtab Subprocess Lifecycle

**Files:**
- Create: `internal/ixd/pinchtab.go`

- [ ] **Step 1: Create pinchtab.go with subprocess management**

```go
package ixd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// pinchtab manages the Pinchtab bridge subprocess lifecycle.
// It spawns "pinchtab bridge" on startup if the binary is found in PATH,
// polls its health endpoint, and restarts on crash (up to maxRestarts).
type pinchtab struct {
	port        string
	cmd         *exec.Cmd
	available   bool
	mu          sync.Mutex
	restarts    int
	maxRestarts int
	cancel      context.CancelFunc
}

// newPinchtab attempts to start a Pinchtab bridge subprocess.
// Returns a pinchtab instance. If the pinchtab binary is not in PATH,
// available is false and all browser proxy calls should return 501.
func newPinchtab(ctx context.Context) *pinchtab {
	p := &pinchtab{
		port:        "9867",
		maxRestarts: 3,
	}

	if _, err := exec.LookPath("pinchtab"); err != nil {
		log.Printf("pinchtab not in PATH, browser disabled")
		return p
	}

	if err := p.start(ctx); err != nil {
		log.Printf("pinchtab start failed: %v", err)
		return p
	}

	return p
}

// start spawns the pinchtab bridge subprocess and waits for it to become healthy.
func (p *pinchtab) start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	childCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	p.cmd = exec.CommandContext(childCtx, "pinchtab", "bridge",
		"--port", p.port,
		"--headless",
	)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := p.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("spawn pinchtab: %w", err)
	}

	// Monitor process exit in background.
	go func() {
		_ = p.cmd.Wait()
	}()

	if err := p.waitHealthy(ctx); err != nil {
		p.kill()
		return fmt.Errorf("pinchtab not healthy: %w", err)
	}

	p.available = true
	log.Printf("pinchtab bridge ready on :%s", p.port)
	return nil
}

// waitHealthy polls the Pinchtab health endpoint with exponential backoff.
func (p *pinchtab) waitHealthy(ctx context.Context) error {
	client := &http.Client{Timeout: 2 * time.Second}
	endpoint := fmt.Sprintf("http://127.0.0.1:%s/health", p.port)

	deadline := time.After(15 * time.Second)
	delay := 200 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("pinchtab not ready after 15s")
		default:
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			time.Sleep(delay)
			delay = min(delay*2, 2*time.Second)
			continue
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(delay)
		delay = min(delay*2, 2*time.Second)
	}
}

// restart attempts to restart the Pinchtab subprocess after a crash.
// Returns an error if max restarts exceeded.
func (p *pinchtab) restart(ctx context.Context) error {
	p.mu.Lock()
	p.restarts++
	count := p.restarts
	p.available = false
	p.mu.Unlock()

	if count > p.maxRestarts {
		return fmt.Errorf("pinchtab max restarts (%d) exceeded", p.maxRestarts)
	}

	log.Printf("pinchtab restart attempt %d/%d", count, p.maxRestarts)
	p.kill()
	return p.start(ctx)
}

// kill terminates the Pinchtab subprocess and its children.
func (p *pinchtab) kill() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// baseURL returns the Pinchtab bridge HTTP base URL.
func (p *pinchtab) baseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%s", p.port)
}

// isAvailable returns true if Pinchtab is running and healthy.
func (p *pinchtab) isAvailable() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.available
}

// shutdown gracefully stops the Pinchtab subprocess.
func (p *pinchtab) shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.available = false
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = p.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			p.kill()
		}
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./internal/ixd/...`
Expected: PASS (no errors)

- [ ] **Step 3: Commit**

```bash
git add internal/ixd/pinchtab.go
git commit -m "feat(ixd): add Pinchtab subprocess lifecycle management"
```

---

### Task 4: ix Daemon — Browser Proxy Handlers

**Files:**
- Create: `internal/ixd/browser.go`

- [ ] **Step 1: Create browser.go with all 6 proxy handlers**

```go
package ixd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// browserProxy proxies browser requests to the Pinchtab bridge subprocess.
type browserProxy struct {
	pt     *pinchtab
	client *http.Client
}

func newBrowserProxy(pt *pinchtab) *browserProxy {
	return &browserProxy{pt: pt, client: &http.Client{}}
}

// checkAvailable returns HTTP 501 if Pinchtab is not available.
func (b *browserProxy) checkAvailable(w http.ResponseWriter) bool {
	if !b.pt.isAvailable() {
		writeError(w, http.StatusNotImplemented,
			"browser not available: use oasis-ix-browser image")
		return false
	}
	return true
}

// handleNavigate proxies POST /v1/browser/navigate → POST /navigate on Pinchtab.
func (b *browserProxy) handleNavigate(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	body, _ := json.Marshal(map[string]string{"url": req.URL})
	resp, err := b.forward(r.Context(), http.MethodPost, "/navigate", body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handleAction proxies POST /v1/browser/action → POST /action on Pinchtab.
func (b *browserProxy) handleAction(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	var req struct {
		Kind string `json:"kind"`
		Ref  string `json:"ref"`
		X    int    `json:"x"`
		Y    int    `json:"y"`
		Text string `json:"text"`
		Key  string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	payload := map[string]any{"kind": req.Kind}
	if req.Ref != "" {
		payload["ref"] = req.Ref
	}
	if req.X != 0 || req.Y != 0 {
		payload["x"] = req.X
		payload["y"] = req.Y
	}
	if req.Text != "" {
		payload["text"] = req.Text
	}
	if req.Key != "" {
		payload["key"] = req.Key
	}

	body, _ := json.Marshal(payload)
	resp, err := b.forward(r.Context(), http.MethodPost, "/action", body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handleScreenshot proxies GET /v1/browser/screenshot → GET /screenshot on Pinchtab.
func (b *browserProxy) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	resp, err := b.forward(r.Context(), http.MethodGet, "/screenshot?raw=true", nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// handleSnapshot proxies GET /v1/browser/snapshot → GET /snapshot on Pinchtab.
func (b *browserProxy) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	q := url.Values{}
	if v := r.URL.Query().Get("filter"); v != "" {
		q.Set("filter", v)
	}
	if v := r.URL.Query().Get("selector"); v != "" {
		q.Set("selector", v)
	}
	if v := r.URL.Query().Get("depth"); v != "" {
		q.Set("depth", v)
	}

	path := "/snapshot"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := b.forward(r.Context(), http.MethodGet, path, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handleText proxies GET /v1/browser/text → GET /text on Pinchtab.
func (b *browserProxy) handleText(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	q := url.Values{}
	if r.URL.Query().Get("raw") == "true" {
		q.Set("mode", "raw")
	}
	if v := r.URL.Query().Get("maxChars"); v != "" {
		q.Set("maxChars", v)
	}

	path := "/text"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	resp, err := b.forward(r.Context(), http.MethodGet, path, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

// handlePDF proxies GET /v1/browser/pdf → GET /pdf on Pinchtab.
func (b *browserProxy) handlePDF(w http.ResponseWriter, r *http.Request) {
	if !b.checkAvailable(w) {
		return
	}
	resp, err := b.forward(r.Context(), http.MethodGet, "/pdf", nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/pdf")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// forward sends an HTTP request to the Pinchtab bridge and returns the response.
func (b *browserProxy) forward(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, b.pt.baseURL()+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pinchtab %s %s: %w", method, path, err)
	}
	return resp, nil
}

// copyResponse copies a Pinchtab JSON response to the ix daemon response writer.
func copyResponse(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./internal/ixd/...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/ixd/browser.go
git commit -m "feat(ixd): add browser proxy handlers for Pinchtab bridge"
```

---

### Task 5: ix Daemon — Server Registration and Health Enhancement

**Files:**
- Modify: `internal/ixd/server.go`
- Modify: `cmd/ix/main.go`

- [ ] **Step 1: Add pinchtab and browser proxy to Server struct and wire routes**

Replace the `Server` struct and `NewServer` function in `server.go`:

```go
// Server is the ix HTTP daemon. It serves shell execution, code execution,
// file operations, and browser automation inside a sandbox container.
type Server struct {
	addr    string
	startAt time.Time
	srv     *http.Server
	pt      *pinchtab
}

// NewServer creates a new ix server that will listen on addr.
// It starts a Pinchtab bridge subprocess if the binary is available.
func NewServer(ctx context.Context, addr string) *Server {
	s := &Server{
		addr:    addr,
		startAt: time.Now(),
		pt:      newPinchtab(ctx),
	}

	bp := newBrowserProxy(s.pt)
	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /health", s.handleHealth)

	// Shell execution (SSE)
	mux.HandleFunc("POST /v1/shell/exec", s.handleShellExec)

	// Code execution (SSE)
	mux.HandleFunc("POST /v1/code/execute", s.handleCodeExecute)

	// File operations (JSON)
	mux.HandleFunc("POST /v1/file/read", s.handleFileRead)
	mux.HandleFunc("POST /v1/file/write", s.handleFileWrite)
	mux.HandleFunc("POST /v1/file/edit", s.handleFileEdit)
	mux.HandleFunc("POST /v1/file/glob", s.handleFileGlob)
	mux.HandleFunc("POST /v1/file/grep", s.handleFileGrep)
	mux.HandleFunc("GET /v1/file/stat", s.handleFileStat)
	mux.HandleFunc("POST /v1/file/ls", s.handleFileLs)

	// File transfer
	mux.HandleFunc("POST /v1/file/upload", s.handleFileUpload)
	mux.HandleFunc("GET /v1/file/download", s.handleFileDownload)

	// Browser (proxied to Pinchtab)
	mux.HandleFunc("POST /v1/browser/navigate", bp.handleNavigate)
	mux.HandleFunc("POST /v1/browser/action", bp.handleAction)
	mux.HandleFunc("GET /v1/browser/screenshot", bp.handleScreenshot)
	mux.HandleFunc("GET /v1/browser/snapshot", bp.handleSnapshot)
	mux.HandleFunc("GET /v1/browser/text", bp.handleText)
	mux.HandleFunc("GET /v1/browser/pdf", bp.handlePDF)

	s.srv = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return s
}
```

- [ ] **Step 2: Update handleHealth to include browser field**

```go
// handleHealth returns daemon status, uptime, and browser availability.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"uptime_sec": int(time.Since(s.startAt).Seconds()),
		"browser":    s.pt.isAvailable(),
	})
}
```

- [ ] **Step 3: Add Shutdown method to Server for Pinchtab cleanup**

Add after `ListenAndServe`:

```go
// Shutdown gracefully stops the server and the Pinchtab subprocess.
func (s *Server) Shutdown() {
	s.pt.shutdown()
}
```

- [ ] **Step 4: Update cmd/ix/main.go to pass context and call shutdown**

```go
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/nevindra/oasis/internal/ixd"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := ixd.NewServer(ctx, *addr)
	defer srv.Shutdown()

	if err := srv.ListenAndServe(ctx); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 5: Verify it compiles**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./internal/ixd/... && go build ./cmd/ix/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/ixd/server.go cmd/ix/main.go
git commit -m "feat(ixd): register browser routes, add browser to health, wire Pinchtab lifecycle"
```

---

### Task 6: IXSandbox Client — Implement Browser Methods

**Files:**
- Modify: `sandbox/ix/sandbox_test.go` (add mock handlers + tests)
- Modify: `sandbox/ix/sandbox.go` (replace stubs with real implementations)

- [ ] **Step 1: Add mock Pinchtab handlers to ixMux in sandbox_test.go**

Replace the existing `POST /v1/browser/actions` and `GET /v1/browser/screenshot` handlers, and add new handlers. Replace lines 157-177 with:

```go
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
```

- [ ] **Step 2: Add test cases for new client methods**

Add after `TestIXSandboxBrowserScreenshot`:

```go
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

func TestIXSandboxBrowserActionWithRef(t *testing.T) {
	s, _ := newTestSandbox(t)

	res, err := s.BrowserAction(context.Background(), sandbox.BrowserAction{
		Type: "fill",
		Ref:  "e3",
		Text: "hello@example.com",
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./sandbox/ix/ -run "TestIXSandboxBrowser(Snapshot|Text|PDF|Navigate|Action)" -v`
Expected: FAIL — stubs return "not implemented" errors, and navigate/action tests fail because endpoints changed.

- [ ] **Step 4: Replace stub implementations in sandbox/ix/sandbox.go**

Replace the `BrowserNavigate` method (lines 229-246):

```go
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
```

Replace the `BrowserAction` method (lines 265-292):

```go
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
```

Replace the 3 stub methods with real implementations:

```go
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
```

Note: Add `"net/url"` to the import block in `sandbox/ix/sandbox.go`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./sandbox/ix/ -v`
Expected: ALL tests PASS

- [ ] **Step 6: Commit**

```bash
git add sandbox/ix/sandbox.go sandbox/ix/sandbox_test.go
git commit -m "feat(sandbox/ix): implement browser snapshot, text, PDF client methods"
```

---

### Task 7: Tools — New Snapshot, PageText, ExportPDF Tools and Enhanced Browser Tool

**Files:**
- Modify: `sandbox/tools_test.go` (add tests)
- Modify: `sandbox/tools.go` (add tools)

- [ ] **Step 1: Add tests for new tools in tools_test.go**

Add before the `mockFileDelivery` type:

```go
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
				if !strings.Contains(result.Content, "12 bytes") {
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
```

- [ ] **Step 2: Update TestToolDefinitionsComplete to include new tools**

Replace the `expected` map and count assertion:

```go
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
		"snapshot":     false,
		"page_text":    false,
		"export_pdf":   false,
	}
```

And:

```go
	if len(tools) != 13 {
		t.Errorf("got %d tools, want 13", len(tools))
	}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./sandbox/ -run "Test(Snapshot|PageText|ExportPDF|BrowserToolWithRef|ToolDefinitionsComplete)" -v`
Expected: FAIL — tools don't exist yet, count is wrong.

- [ ] **Step 4: Add new tools and enhance browser tool in tools.go**

Add the 3 new tool functions:

```go
func snapshotTool(sb Sandbox) toolImpl {
	return newTool("snapshot", "Get the accessibility tree of the current browser page. Returns element references (e0, e1, ...) that can be used with the browser tool for precise interactions.", `{
		"type": "object",
		"properties": {
			"filter":   {"type": "string", "description": "Set to 'interactive' to show only actionable elements"},
			"selector": {"type": "string", "description": "CSS selector to scope snapshot to a subtree"},
			"depth":    {"type": "integer", "description": "Tree traversal depth limit (0 = unlimited)"}
		}
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Filter   string `json:"filter"`
			Selector string `json:"selector"`
			Depth    int    `json:"depth"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		snap, err := sb.BrowserSnapshot(ctx, SnapshotOpts{
			Filter:   p.Filter,
			Selector: p.Selector,
			Depth:    p.Depth,
		})
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		var out strings.Builder
		fmt.Fprintf(&out, "url: %s\ntitle: %s\n", snap.URL, snap.Title)
		for _, n := range snap.Nodes {
			fmt.Fprintf(&out, "[%s] %s %q\n", n.Ref, n.Role, n.Name)
		}
		return oasis.ToolResult{Content: out.String()}, nil
	})
}

func pageTextTool(sb Sandbox) toolImpl {
	return newTool("page_text", "Extract readable text content from the current browser page. Ideal for RAG and information gathering — much cheaper than screenshots.", `{
		"type": "object",
		"properties": {
			"raw":       {"type": "boolean", "description": "true = raw innerText, false = readability extraction (default)"},
			"max_chars": {"type": "integer", "description": "Truncation limit in characters (0 = unlimited)"}
		}
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Raw      bool `json:"raw"`
			MaxChars int  `json:"max_chars"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
		result, err := sb.BrowserText(ctx, TextOpts{Raw: p.Raw, MaxChars: p.MaxChars})
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		return oasis.ToolResult{Content: result.Text}, nil
	})
}

func exportPDFTool(sb Sandbox) toolImpl {
	return newTool("export_pdf", "Export the current browser page as a PDF document.", `{
		"type": "object",
		"properties": {}
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		data, err := sb.BrowserPDF(ctx)
		if err != nil {
			return oasis.ToolResult{Error: err.Error()}, nil
		}
		return oasis.ToolResult{Content: fmt.Sprintf("pdf exported (%d bytes)", len(data))}, nil
	})
}
```

Add `"strings"` to the import block in `tools.go`.

Update the `Tools` function to include the new tools:

```go
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
		snapshotTool(sb),
		pageTextTool(sb),
		exportPDFTool(sb),
	}
```

Update the `browserTool` function to support `ref` parameter:

```go
func browserTool(sb Sandbox) toolImpl {
	return newTool("browser", "Interact with the sandbox browser. Use element refs from the snapshot tool for precise interactions.", `{
		"type": "object",
		"properties": {
			"action": {"type": "string", "description": "Browser action: navigate, click, type, scroll, key, hover, fill, press, select"},
			"ref":    {"type": "string", "description": "Element reference from snapshot (e.g., 'e5'). Preferred over coordinates."},
			"url":    {"type": "string", "description": "URL for navigate action"},
			"x":      {"type": "integer", "description": "X coordinate (fallback when ref not available)"},
			"y":      {"type": "integer", "description": "Y coordinate (fallback when ref not available)"},
			"text":   {"type": "string", "description": "Text to type or fill"},
			"key":    {"type": "string", "description": "Key to press"}
		},
		"required": ["action"]
	}`, func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		var p struct {
			Action string `json:"action"`
			Ref    string `json:"ref"`
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
			Ref:  p.Ref,
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
```

- [ ] **Step 5: Run all tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./sandbox/ -v`
Expected: ALL tests PASS

- [ ] **Step 6: Commit**

```bash
git add sandbox/tools.go sandbox/tools_test.go
git commit -m "feat(sandbox): add snapshot, page_text, export_pdf tools and enhance browser tool with ref"
```

---

### Task 8: Run Full Test Suite

**Files:** None (verification only)

- [ ] **Step 1: Run all package tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./sandbox/... ./sandbox/ix/... ./internal/ixd/...`
Expected: ALL PASS

- [ ] **Step 2: Build all binaries**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./...`
Expected: No errors

---

### Task 9: Docker — Browser Image

**Files:**
- Create: `cmd/ix/Dockerfile.browser`

- [ ] **Step 1: Create the browser Dockerfile**

```dockerfile
FROM oasis-ix:latest

# Install Chromium for headless browser automation.
RUN apt-get update && apt-get install -y \
    chromium \
    --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*

# Install Pinchtab bridge binary.
COPY --from=pinchtab/pinchtab:latest /usr/local/bin/pinchtab /usr/local/bin/pinchtab

# Chrome needs larger shared memory.
# Run with: docker run --shm-size=2g
ENV CHROME_BIN=/usr/bin/chromium
```

- [ ] **Step 2: Commit**

```bash
git add cmd/ix/Dockerfile.browser
git commit -m "feat(ix): add Dockerfile.browser for oasis-ix-browser image"
```

---

### Task 10: IX Manager — Browser Image Security Adjustments

**Files:**
- Modify: `sandbox/ix/manager.go`

- [ ] **Step 1: Add browser image detection and security adjustments to Create method**

In the `Create` method, after `resolved := m.resolveOpts(opts)`, add browser image detection logic. When the image name contains "browser", adjust the container config:

```go
	// Detect browser image and adjust container security for Chrome.
	isBrowserImage := strings.Contains(resolved.Image, "browser")
	if isBrowserImage && resolved.Resources.Memory < 3<<30 {
		resolved.Resources.Memory = 3 << 30 // 3 GB minimum for Chrome
	}
```

In the `hostCfg` construction, adjust for browser images:

```go
	hostCfg := &container.HostConfig{
		Runtime: m.cfg.Runtime,
		Resources: container.Resources{
			Memory:    resolved.Resources.Memory,
			CPUQuota:  int64(resolved.Resources.CPU) * 100000,
			CPUPeriod: 100000,
			PidsLimit: &pidsLimit,
		},
		NetworkMode:  container.NetworkMode(networkName),
		PortBindings: portBindings,
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyDisabled,
		},
		SecurityOpt: []string{"no-new-privileges:true"},
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{"CHOWN", "SETUID", "SETGID", "KILL", "NET_BIND_SERVICE"},
	}

	// Chrome requires larger shared memory and relaxed seccomp for its sandbox.
	if isBrowserImage {
		hostCfg.ShmSize = 2 << 30 // 2 GB shared memory
		hostCfg.SecurityOpt = []string{"no-new-privileges:true", "seccomp=unconfined"}
	}
```

Add `"strings"` to the import block if not already present.

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./sandbox/ix/...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add sandbox/ix/manager.go
git commit -m "feat(sandbox/ix): adjust container security for browser image (shm, seccomp, memory)"
```

---

### Task 11: Documentation Update

**Files:**
- Modify: `docs/concepts/code-execution.md`

- [ ] **Step 1: Update the tool table in code-execution.md**

Replace the tool table (lines 98-110):

```markdown
| Tool | Description |
|---|---|
| `shell` | Execute shell commands |
| `execute_code` | Execute code (Python, JS, Bash) |
| `file_read` | Read file content from the sandbox |
| `file_write` | Write content to a file in the sandbox |
| `file_edit` | Edit a file by replacing an exact string match. More efficient than read+rewrite. |
| `file_glob` | Find files matching a glob pattern with recursive support. |
| `file_grep` | Search file contents for a regex pattern with line numbers. |
| `browser` | Browser interactions (navigate, click, type, fill) with element ref support |
| `screenshot` | Capture browser/desktop screenshot |
| `snapshot` | Get accessibility tree with element refs for precise interaction |
| `page_text` | Extract readable text from the page (token-efficient alternative to screenshots) |
| `export_pdf` | Export current page as PDF |
| `mcp_call` | Invoke MCP server tools |
```

- [ ] **Step 2: Update the tool count references**

Change "10 tools" to "13 tools" where mentioned in the doc.

- [ ] **Step 3: Commit**

```bash
git add docs/concepts/code-execution.md
git commit -m "docs: update code-execution with browser browsing tools"
```
