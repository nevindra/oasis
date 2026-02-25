package code

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	oasis "github.com/nevindra/oasis"
)

// mockSandbox creates a test server that simulates a sandbox /execute endpoint.
// The handler function receives the parsed request and returns the response.
func mockSandbox(t *testing.T, handler func(req sandboxExecRequest) sandboxExecResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("mock sandbox: read body: %v", err)
			http.Error(w, "read error", 500)
			return
		}
		var req sandboxExecRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("mock sandbox: unmarshal: %v", err)
			http.Error(w, "parse error", 400)
			return
		}
		resp := handler(req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestHTTPRunner_SimpleExecution(t *testing.T) {
	sandbox := mockSandbox(t, func(req sandboxExecRequest) sandboxExecResponse {
		return sandboxExecResponse{
			Output:   `{"answer":42}`,
			ExitCode: 0,
		}
	})
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL)
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

func TestHTTPRunner_RuntimePassed(t *testing.T) {
	var gotRuntime string
	sandbox := mockSandbox(t, func(req sandboxExecRequest) sandboxExecResponse {
		gotRuntime = req.Runtime
		return sandboxExecResponse{Output: `"ok"`, ExitCode: 0}
	})
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	_, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "print('hi')",
		Runtime: "node",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotRuntime != "node" {
		t.Errorf("expected runtime 'node', got %q", gotRuntime)
	}
}

func TestHTTPRunner_SessionIDPassed(t *testing.T) {
	var gotSession string
	sandbox := mockSandbox(t, func(req sandboxExecRequest) sandboxExecResponse {
		gotSession = req.SessionID
		return sandboxExecResponse{Output: `"ok"`, ExitCode: 0}
	})
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	_, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:      "x = 1",
		Runtime:   "python",
		SessionID: "conv_abc123",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSession != "conv_abc123" {
		t.Errorf("expected session_id 'conv_abc123', got %q", gotSession)
	}
}

func TestHTTPRunner_ToolCallback(t *testing.T) {
	// Mock sandbox that calls back with a tool call before returning.
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req sandboxExecRequest
		json.Unmarshal(body, &req)

		// Simulate a tool callback to the app.
		callbackPayload := fmt.Sprintf(`{"execution_id":%q,"name":"greet","args":{"name":"world"}}`, req.ExecutionID)
		resp, err := http.Post(req.CallbackURL, "application/json", strings.NewReader(callbackPayload))
		if err != nil {
			t.Errorf("callback failed: %v", err)
			json.NewEncoder(w).Encode(sandboxExecResponse{Error: "callback failed", ExitCode: 1})
			return
		}
		defer resp.Body.Close()
		var cbResp sandboxDispatchResponse
		json.NewDecoder(resp.Body).Decode(&cbResp)

		// Return the tool result as output.
		json.NewEncoder(w).Encode(sandboxExecResponse{
			Output:   cbResp.Data,
			ExitCode: 0,
		})
	}))
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL)
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
		Code:    "result = call_tool('greet', {'name': 'world'})",
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	json.Unmarshal([]byte(result.Output), &out)
	if out["greeting"] != "hello world" {
		t.Errorf("expected 'hello world', got %v", out["greeting"])
	}
}

func TestHTTPRunner_ParallelToolCallbacks(t *testing.T) {
	// Mock sandbox that makes 3 concurrent tool callbacks.
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req sandboxExecRequest
		json.Unmarshal(body, &req)

		// Fire 3 parallel callbacks.
		type cbResult struct {
			idx  int
			data string
		}
		ch := make(chan cbResult, 3)
		for i := 0; i < 3; i++ {
			go func(idx int) {
				payload := fmt.Sprintf(`{"execution_id":%q,"name":"echo","args":{"n":%d}}`, req.ExecutionID, idx)
				resp, err := http.Post(req.CallbackURL, "application/json", strings.NewReader(payload))
				if err != nil {
					ch <- cbResult{idx: idx, data: "error"}
					return
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				ch <- cbResult{idx: idx, data: string(b)}
			}(i)
		}

		// Collect all 3.
		results := make([]string, 3)
		for i := 0; i < 3; i++ {
			r := <-ch
			results[r.idx] = r.data
		}

		json.NewEncoder(w).Encode(sandboxExecResponse{
			Output:   fmt.Sprintf(`{"count":%d}`, len(results)),
			ExitCode: 0,
		})
	}))
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL)
	defer runner.Close()

	var callCount atomic.Int32
	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		callCount.Add(1)
		var args struct{ N int }
		json.Unmarshal(tc.Args, &args)
		return oasis.DispatchResult{Content: fmt.Sprintf(`"echo_%d"`, args.N)}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "parallel calls",
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 tool calls, got %d", callCount.Load())
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
}

func TestHTTPRunner_Timeout(t *testing.T) {
	// Sandbox that delays longer than the runner timeout.
	done := make(chan struct{})
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-done:
		}
	}))
	defer func() {
		close(done)
		sandbox.Close()
	}()

	runner := NewHTTPRunner(sandbox.URL, WithTimeout(500*time.Millisecond), WithMaxRetries(1))
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	_, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "time.sleep(100)",
		Runtime: "python",
	}, dispatch)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") && !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("expected timeout-related error, got: %v", err)
	}
}

func TestHTTPRunner_RetryOnTransient(t *testing.T) {
	var attempts atomic.Int32
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			http.NotFound(w, r)
			return
		}
		n := attempts.Add(1)
		if n == 1 {
			// First attempt: 503.
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"busy"}`))
			return
		}
		// Second attempt: success.
		json.NewEncoder(w).Encode(sandboxExecResponse{
			Output:   `"retried"`,
			ExitCode: 0,
		})
	}))
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL, WithMaxRetries(2), WithRetryDelay(10*time.Millisecond))
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "x = 1",
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if result.Output != `"retried"` {
		t.Errorf("expected output 'retried', got %q", result.Output)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestHTTPRunner_FileOutput(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4E, 0x47} // PNG magic bytes
	b64Data := base64.StdEncoding.EncodeToString(pngData)

	sandbox := mockSandbox(t, func(req sandboxExecRequest) sandboxExecResponse {
		return sandboxExecResponse{
			Output:   `{"summary":"chart created"}`,
			ExitCode: 0,
			Files: []wireFile{
				{Name: "chart.png", MIME: "image/png", Data: b64Data},
			},
		}
	})
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "make chart",
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
		t.Errorf("expected %d bytes, got %d", len(pngData), len(f.Data))
	}
}

func TestHTTPRunner_FileInputSent(t *testing.T) {
	csvData := []byte("a,b,c\n1,2,3\n")
	var gotFiles []wireFile

	sandbox := mockSandbox(t, func(req sandboxExecRequest) sandboxExecResponse {
		gotFiles = req.Files
		return sandboxExecResponse{Output: `"ok"`, ExitCode: 0}
	})
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	_, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "pd.read_csv('data.csv')",
		Runtime: "python",
		Files: []oasis.CodeFile{
			{Name: "data.csv", Data: csvData},
		},
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotFiles) != 1 {
		t.Fatalf("expected 1 input file, got %d", len(gotFiles))
	}
	if gotFiles[0].Name != "data.csv" {
		t.Errorf("expected file name 'data.csv', got %q", gotFiles[0].Name)
	}
	decoded, err := base64.StdEncoding.DecodeString(gotFiles[0].Data)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != string(csvData) {
		t.Errorf("file data mismatch")
	}
}

func TestHTTPRunner_MaxFileSizeEnforced(t *testing.T) {
	// Return a file larger than max.
	bigData := strings.Repeat("x", 100)
	b64Big := base64.StdEncoding.EncodeToString([]byte(bigData))

	sandbox := mockSandbox(t, func(req sandboxExecRequest) sandboxExecResponse {
		return sandboxExecResponse{
			Output:   `"ok"`,
			ExitCode: 0,
			Files: []wireFile{
				{Name: "big.bin", MIME: "application/octet-stream", Data: b64Big},
			},
		}
	})
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL, WithMaxFileSize(50)) // 50 bytes max
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "big file",
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}
	// File should have metadata but no data (degraded).
	f := result.Files[0]
	if f.Name != "big.bin" {
		t.Errorf("expected name 'big.bin', got %q", f.Name)
	}
	if len(f.Data) != 0 {
		t.Errorf("expected empty data (degraded), got %d bytes", len(f.Data))
	}
}

func TestHTTPRunner_ErrorResponse(t *testing.T) {
	sandbox := mockSandbox(t, func(req sandboxExecRequest) sandboxExecResponse {
		return sandboxExecResponse{
			ExitCode: 1,
			Error:    "SyntaxError: invalid syntax",
			Logs:     "Traceback...\nSyntaxError: invalid syntax",
		}
	})
	defer sandbox.Close()

	runner := NewHTTPRunner(sandbox.URL)
	defer runner.Close()

	dispatch := func(ctx context.Context, tc oasis.ToolCall) oasis.DispatchResult {
		return oasis.DispatchResult{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code:    "def foo(:",
		Runtime: "python",
	}, dispatch)
	if err != nil {
		t.Fatalf("expected no Go error, got: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", result.ExitCode)
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
	if !strings.Contains(result.Logs, "Traceback") {
		t.Errorf("expected traceback in logs, got: %s", result.Logs)
	}
}

func TestHTTPRunner_ExternalMount(t *testing.T) {
	sandbox := mockSandbox(t, func(req sandboxExecRequest) sandboxExecResponse {
		return sandboxExecResponse{Output: `"mounted"`, ExitCode: 0}
	})
	defer sandbox.Close()

	// Create runner with external callback.
	runner := NewHTTPRunner(sandbox.URL, WithCallbackExternal("http://myapp:8080"))
	defer runner.Close()

	// Verify the callback URL is correctly constructed.
	url := runner.callbackURL()
	if url != "http://myapp:8080/_oasis/dispatch" {
		t.Errorf("expected external callback URL, got %q", url)
	}
}

func TestAPICompiles(t *testing.T) {
	runner := NewHTTPRunner("http://sandbox:9000",
		WithTimeout(30*time.Second),
		WithMaxFileSize(10<<20),
		WithCallbackAddr("127.0.0.1:0"),
		WithMaxRetries(3),
		WithRetryDelay(time.Second),
	)
	_ = oasis.WithCodeExecution(runner)
}
