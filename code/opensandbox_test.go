package code

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	oasis "github.com/nevindra/oasis"
)

func TestParseSSEStream(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"init","timestamp":1700000000}`,
		"",
		`{"type":"stdout","text":"hello\n","timestamp":1700000001}`,
		"",
		`{"type":"stderr","text":"warning\n","timestamp":1700000002}`,
		"",
		`{"type":"ping","timestamp":1700000003}`,
		"",
		`{"type":"execution_complete","execution_time":150,"timestamp":1700000004}`,
		"",
	}, "\n")

	var events []osSSEEvent
	err := parseSSEStream(context.Background(), strings.NewReader(input), func(e osSSEEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Ping should be skipped, leaving 4 events.
	if len(events) != 4 {
		t.Fatalf("expected 4 events (ping skipped), got %d", len(events))
	}

	if events[0].Type != "init" {
		t.Errorf("event[0] type = %q, want init", events[0].Type)
	}
	if events[1].Type != "stdout" {
		t.Errorf("event[1] type = %q, want stdout", events[1].Type)
	}
	if events[1].Text != "hello\n" {
		t.Errorf("event[1] text = %q, want %q", events[1].Text, "hello\n")
	}
	if events[2].Type != "stderr" {
		t.Errorf("event[2] type = %q, want stderr", events[2].Type)
	}
	if events[3].Type != "execution_complete" {
		t.Errorf("event[3] type = %q, want execution_complete", events[3].Type)
	}
	if events[3].ExecutionTime != 150 {
		t.Errorf("event[3] execution_time = %d, want 150", events[3].ExecutionTime)
	}
}

func TestParseSSEStream_Error(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"error","error":{"ename":"ValueError","evalue":"invalid input","traceback":["line 1","line 2"]},"timestamp":1700000000}`,
		"",
	}, "\n")

	var events []osSSEEvent
	err := parseSSEStream(context.Background(), strings.NewReader(input), func(e osSSEEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]
	if ev.Type != "error" {
		t.Errorf("type = %q, want error", ev.Type)
	}
	if ev.Error == nil {
		t.Fatal("expected non-nil Error field")
	}
	if ev.Error.Ename != "ValueError" {
		t.Errorf("ename = %q, want ValueError", ev.Error.Ename)
	}
	if ev.Error.Evalue != "invalid input" {
		t.Errorf("evalue = %q, want %q", ev.Error.Evalue, "invalid input")
	}
	if len(ev.Error.Traceback) != 2 {
		t.Errorf("traceback length = %d, want 2", len(ev.Error.Traceback))
	}
}

func TestOpenSandboxRunner_Compiles(t *testing.T) {
	runner := NewOpenSandboxRunner("http://server:8080", "test-key",
		WithImage("python:3.11-slim"),
		WithResources("1", "1Gi"),
		WithExecTimeout(60*time.Second),
		WithSandboxTTL(300),
		WithCallbackListenAddr("127.0.0.1:0"),
		WithRetryCount(3),
		WithRetryBackoff(time.Second),
	)
	_ = oasis.WithCodeExecution(runner)
}

// ---------------------------------------------------------------------------
// Mock OpenSandbox server infrastructure
// ---------------------------------------------------------------------------

// mockOSServer simulates both the OpenSandbox lifecycle API and the execd API
// within a single httptest.Server. The lifecycle endpoints (POST /v1/sandboxes,
// GET /v1/sandboxes/{id}/endpoints/{port}, DELETE /v1/sandboxes/{id}) return
// canned responses that point the runner at the same server's execd endpoints
// (/ping, /command, /files/upload, /files/download).
type mockOSServer struct {
	srv           *httptest.Server
	sandboxID     string
	onCommand     func(req osCommandRequest) string // returns raw SSE body
	uploadedFiles map[string][]byte
	downloadFiles map[string][]byte

	mu          sync.Mutex
	createCount int // number of POST /v1/sandboxes calls
}

// newMockOSServer creates and starts a mock server. The onCommand callback is
// called for every POST /command request; it receives the parsed command
// request and must return the raw SSE response body (use sseLines helper).
func newMockOSServer(t *testing.T, onCommand func(req osCommandRequest) string) *mockOSServer {
	t.Helper()
	m := &mockOSServer{
		sandboxID:     "sb-mock-001",
		onCommand:     onCommand,
		uploadedFiles: make(map[string][]byte),
		downloadFiles: make(map[string][]byte),
	}

	mux := http.NewServeMux()

	// --- Lifecycle API ---

	// POST /v1/sandboxes → create sandbox
	mux.HandleFunc("/v1/sandboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			m.mu.Lock()
			m.createCount++
			m.mu.Unlock()

			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(osSandbox{
				ID:     m.sandboxID,
				Status: osStatus{State: "Running"},
			})
			return
		}
		http.NotFound(w, r)
	})

	// GET /v1/sandboxes/{id}/endpoints/{port} → return self as execd
	mux.HandleFunc("/v1/sandboxes/"+m.sandboxID+"/endpoints/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		// Return the mock server's own URL as the execd endpoint.
		// The server URL is set after the server starts (see below).
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(osEndpoint{
			Endpoint: m.srv.URL,
			Headers:  map[string]string{},
		})
	})

	// DELETE /v1/sandboxes/{id} → 204 No Content
	mux.HandleFunc("/v1/sandboxes/"+m.sandboxID, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})

	// --- Execd API ---

	// GET /ping → 200 OK
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// POST /command → call onCommand handler, return SSE response
	mux.HandleFunc("/command", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		var req osCommandRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "parse error", http.StatusBadRequest)
			return
		}
		sseBody := m.onCommand(req)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sseBody))
	})

	// POST /files/upload → parse multipart, store in uploadedFiles
	mux.HandleFunc("/files/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(50 << 20); err != nil {
			http.Error(w, "parse multipart: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Read metadata to get path.
		metaStr := r.FormValue("metadata")
		var meta osFileMetadata
		if metaStr != "" {
			json.Unmarshal([]byte(metaStr), &meta)
		}

		// Read file data.
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "read file: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)

		m.mu.Lock()
		m.uploadedFiles[meta.Path] = data
		m.mu.Unlock()

		w.WriteHeader(http.StatusOK)
	})

	// GET /files/download?path=... → return from downloadFiles
	mux.HandleFunc("/files/download", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		path := r.URL.Query().Get("path")
		m.mu.Lock()
		data, ok := m.downloadFiles[path]
		m.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	})

	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockOSServer) Close() {
	m.srv.Close()
}

// sseLines joins SSE events with "\n\n" separator (the OpenSandbox SSE format).
func sseLines(events ...string) string {
	return strings.Join(events, "\n\n") + "\n\n"
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestOpenSandboxRunner_SimpleExecution(t *testing.T) {
	mock := newMockOSServer(t, func(req osCommandRequest) string {
		return sseLines(
			`{"type":"stdout","text":"{\"type\":\"result\",\"data\":{\"answer\":42}}\n"}`,
			`{"type":"execution_complete","execution_time":50}`,
		)
	})
	defer mock.Close()

	runner := NewOpenSandboxRunner(mock.srv.URL, "test-key",
		WithExecTimeout(10*time.Second),
	)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{Content: "unused"}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    `set_result({"answer": 42})`,
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result.Output), &out); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, result.Output)
	}
	if out["answer"] != float64(42) {
		t.Errorf("expected answer=42, got %v", out["answer"])
	}
}

func TestOpenSandboxRunner_ErrorExecution(t *testing.T) {
	mock := newMockOSServer(t, func(req osCommandRequest) string {
		return sseLines(
			`{"type":"stderr","text":"Traceback (most recent call last):\n  File \"test.py\", line 1\n"}`,
			`{"type":"error","error":{"ename":"NameError","evalue":"name 'foo' is not defined","traceback":["Traceback (most recent call last):","  File \"test.py\", line 1","NameError: name 'foo' is not defined"]}}`,
			`{"type":"execution_complete","execution_time":10}`,
		)
	})
	defer mock.Close()

	runner := NewOpenSandboxRunner(mock.srv.URL, "test-key",
		WithExecTimeout(10*time.Second),
	)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "print(foo)",
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
	if !strings.Contains(result.Error, "NameError") {
		t.Errorf("expected error to contain 'NameError', got %q", result.Error)
	}
	if !strings.Contains(result.Logs, "Traceback") {
		t.Errorf("expected logs to contain 'Traceback', got %q", result.Logs)
	}
}

func TestOpenSandboxRunner_ToolCallback(t *testing.T) {
	mock := newMockOSServer(t, func(req osCommandRequest) string {
		// Read callback URL and execution ID from the command envs.
		cbURL := req.Envs["_SANDBOX_CALLBACK_URL"]
		execID := req.Envs["_SANDBOX_EXECUTION_ID"]

		if cbURL == "" || execID == "" {
			return sseLines(
				`{"type":"stderr","text":"missing callback env vars\n"}`,
				`{"type":"error","error":{"ename":"Error","evalue":"missing env vars","traceback":[]}}`,
			)
		}

		// Simulate the sandbox code calling a tool via the callback URL.
		callbackPayload := fmt.Sprintf(
			`{"execution_id":%q,"name":"greet","args":{"name":"world"}}`,
			execID,
		)
		resp, err := http.Post(cbURL, "application/json", strings.NewReader(callbackPayload))
		if err != nil {
			return sseLines(
				fmt.Sprintf(`{"type":"stderr","text":"callback failed: %s\n"}`, err),
				`{"type":"error","error":{"ename":"Error","evalue":"callback failed","traceback":[]}}`,
			)
		}
		defer resp.Body.Close()
		var cbResp sandboxDispatchResponse
		json.NewDecoder(resp.Body).Decode(&cbResp)

		// Return the tool result as the code's stdout protocol message.
		resultLine := fmt.Sprintf(`{"type":"result","data":%s}`, cbResp.Data)
		return sseLines(
			fmt.Sprintf(`{"type":"stdout","text":"%s\n"}`, strings.ReplaceAll(resultLine, `"`, `\"`)),
			`{"type":"execution_complete","execution_time":100}`,
		)
	})
	defer mock.Close()

	runner := NewOpenSandboxRunner(mock.srv.URL, "test-key",
		WithExecTimeout(10*time.Second),
	)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		if tc.Name == "greet" {
			var args struct{ Name string }
			json.Unmarshal(tc.Args, &args)
			return oasis.DispatchResult{Content: fmt.Sprintf(`{"greeting":"hello %s"}`, args.Name)}
		}
		return oasis.DispatchResult{Content: "error: unknown tool", IsError: true}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    `result = call_tool('greet', {'name': 'world'})`,
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result.Output), &out); err != nil {
		t.Fatalf("parse output: %v (raw: %s)", err, result.Output)
	}
	if out["greeting"] != "hello world" {
		t.Errorf("expected greeting='hello world', got %v", out["greeting"])
	}
}

func TestOpenSandboxRunner_FileOutput(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG magic bytes

	mock := newMockOSServer(t, func(req osCommandRequest) string {
		return sseLines(
			`{"type":"stdout","text":"{\"type\":\"result_files\",\"files\":[\"chart.png\"]}\n"}`,
			`{"type":"stdout","text":"{\"type\":\"result\",\"data\":{\"summary\":\"chart created\"}}\n"}`,
			`{"type":"execution_complete","execution_time":200}`,
		)
	})
	mock.downloadFiles["/workspace/chart.png"] = pngData
	defer mock.Close()

	runner := NewOpenSandboxRunner(mock.srv.URL, "test-key",
		WithExecTimeout(10*time.Second),
	)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "make_chart()",
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}
	f := result.Files[0]
	if f.Name != "chart.png" {
		t.Errorf("expected name 'chart.png', got %q", f.Name)
	}
	if f.MIME != "image/png" {
		t.Errorf("expected MIME 'image/png', got %q", f.MIME)
	}
	if len(f.Data) != len(pngData) {
		t.Errorf("expected %d bytes of data, got %d", len(pngData), len(f.Data))
	}
}

func TestOpenSandboxRunner_FileInput(t *testing.T) {
	csvData := []byte("a,b,c\n1,2,3\n4,5,6\n")

	mock := newMockOSServer(t, func(req osCommandRequest) string {
		return sseLines(
			`{"type":"stdout","text":"{\"type\":\"result\",\"data\":{\"rows\":3}}\n"}`,
			`{"type":"execution_complete","execution_time":50}`,
		)
	})
	defer mock.Close()

	runner := NewOpenSandboxRunner(mock.srv.URL, "test-key",
		WithExecTimeout(10*time.Second),
	)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	_, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "import pandas as pd; df = pd.read_csv('data.csv')",
		Runtime: "python",
		Files: []oasis.CodeFile{
			{Name: "data.csv", Data: csvData},
		},
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mock.mu.Lock()
	uploaded, ok := mock.uploadedFiles["/workspace/data.csv"]
	mock.mu.Unlock()
	if !ok {
		t.Fatal("expected file uploaded at /workspace/data.csv")
	}
	if string(uploaded) != string(csvData) {
		t.Errorf("uploaded file data mismatch: got %q, want %q", string(uploaded), string(csvData))
	}
}

func TestOpenSandboxRunner_SessionReuse(t *testing.T) {
	var commandCount atomic.Int32

	mock := newMockOSServer(t, func(req osCommandRequest) string {
		commandCount.Add(1)
		return sseLines(
			`{"type":"stdout","text":"{\"type\":\"result\",\"data\":\"ok\"}\n"}`,
			`{"type":"execution_complete","execution_time":10}`,
		)
	})
	defer mock.Close()

	runner := NewOpenSandboxRunner(mock.srv.URL, "test-key",
		WithExecTimeout(10*time.Second),
	)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	// First execution with session ID.
	_, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:      "x = 1",
		Runtime:   "python",
		SessionID: "session-reuse-test",
	}, dispatch)
	if err != nil {
		t.Fatalf("first run error: %v", err)
	}

	// Second execution with same session ID.
	_, err = runner.Run(context.Background(), oasis.CodeRequest{
		Code:      "x = 2",
		Runtime:   "python",
		SessionID: "session-reuse-test",
	}, dispatch)
	if err != nil {
		t.Fatalf("second run error: %v", err)
	}

	// Verify sandbox was only created once.
	mock.mu.Lock()
	creates := mock.createCount
	mock.mu.Unlock()
	if creates != 1 {
		t.Errorf("expected 1 sandbox creation, got %d", creates)
	}

	// Verify both commands were executed.
	if commandCount.Load() != 2 {
		t.Errorf("expected 2 command executions, got %d", commandCount.Load())
	}
}

func TestOpenSandboxRunner_NodeRuntime(t *testing.T) {
	var capturedCommand string

	mock := newMockOSServer(t, func(req osCommandRequest) string {
		capturedCommand = req.Command
		return sseLines(
			`{"type":"stdout","text":"{\"type\":\"result\",\"data\":\"ok\"}\n"}`,
			`{"type":"execution_complete","execution_time":30}`,
		)
	})
	defer mock.Close()

	runner := NewOpenSandboxRunner(mock.srv.URL, "test-key",
		WithExecTimeout(10*time.Second),
	)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	_, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "console.log('hello')",
		Runtime: "node",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the command starts with "node" and ends with ".js".
	if !strings.HasPrefix(capturedCommand, "node ") {
		t.Errorf("expected command to start with 'node ', got %q", capturedCommand)
	}
	if !strings.HasSuffix(capturedCommand, ".js") {
		t.Errorf("expected command to end with '.js', got %q", capturedCommand)
	}

	// Verify the uploaded script contains "callTool" (from JS prelude).
	// The script is uploaded at the path referenced in the command.
	scriptPath := strings.TrimPrefix(capturedCommand, "node ")
	mock.mu.Lock()
	scriptData, ok := mock.uploadedFiles[scriptPath]
	mock.mu.Unlock()
	if !ok {
		t.Fatalf("expected script uploaded at %q, uploadedFiles keys: %v", scriptPath, func() []string {
			mock.mu.Lock()
			defer mock.mu.Unlock()
			keys := make([]string, 0, len(mock.uploadedFiles))
			for k := range mock.uploadedFiles {
				keys = append(keys, k)
			}
			return keys
		}())
	}
	if !strings.Contains(string(scriptData), "callTool") {
		t.Errorf("expected script to contain 'callTool' (JS prelude), got:\n%s", string(scriptData)[:min(200, len(scriptData))])
	}
}

func TestOpenSandboxRunner_Timeout(t *testing.T) {
	done := make(chan struct{})
	mock := newMockOSServer(t, func(req osCommandRequest) string {
		// Block until the test completes or timeout fires.
		select {
		case <-time.After(10 * time.Second):
		case <-done:
		}
		return sseLines(`{"type":"execution_complete","execution_time":0}`)
	})
	defer func() {
		close(done)
		mock.Close()
	}()

	runner := NewOpenSandboxRunner(mock.srv.URL, "test-key",
		WithExecTimeout(500*time.Millisecond),
	)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	_, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "import time; time.sleep(100)",
		Runtime: "python",
	}, dispatch)
	if err == nil {
		t.Fatal("expected error due to timeout, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") && !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout-related error, got: %v", err)
	}
}
