package code

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	oasis "github.com/nevindra/oasis"
)

func TestSubprocessRunner_SimpleCode(t *testing.T) {
	runner := NewSubprocessRunner("python3")

	dispatch := func(ctx context.Context, tc oasis.ToolCall) (string, oasis.Usage) {
		return `{"content": "hello world"}`, oasis.Usage{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code: `set_result({"answer": 42})`,
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d (logs: %s, error: %s)", result.ExitCode, result.Logs, result.Error)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(result.Output), &out); err != nil {
		t.Fatalf("failed to parse output: %v (raw: %s)", err, result.Output)
	}
	if out["answer"] != float64(42) {
		t.Errorf("expected answer=42, got %v", out["answer"])
	}
}

func TestSubprocessRunner_CallTool(t *testing.T) {
	runner := NewSubprocessRunner("python3")

	dispatch := func(ctx context.Context, tc oasis.ToolCall) (string, oasis.Usage) {
		if tc.Name == "greet" {
			var args struct {
				Name string `json:"name"`
			}
			json.Unmarshal(tc.Args, &args)
			return fmt.Sprintf(`{"greeting": "hello %s"}`, args.Name), oasis.Usage{}
		}
		return "error: unknown tool", oasis.Usage{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code: `
result = call_tool('greet', {'name': 'world'})
set_result(result)
`,
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

func TestSubprocessRunner_CallToolsParallel(t *testing.T) {
	runner := NewSubprocessRunner("python3")

	dispatch := func(ctx context.Context, tc oasis.ToolCall) (string, oasis.Usage) {
		var args struct {
			Path string `json:"path"`
		}
		json.Unmarshal(tc.Args, &args)
		return fmt.Sprintf(`"content of %s"`, args.Path), oasis.Usage{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code: `
results = call_tools_parallel([
    ('file_read', {'path': 'a.py'}),
    ('file_read', {'path': 'b.py'}),
])
set_result({"count": len(results), "files": results})
`,
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	json.Unmarshal([]byte(result.Output), &out)
	if out["count"] != float64(2) {
		t.Errorf("expected count=2, got %v", out["count"])
	}
}

func TestSubprocessRunner_Timeout(t *testing.T) {
	runner := NewSubprocessRunner("python3", WithTimeout(2*time.Second))

	dispatch := func(ctx context.Context, tc oasis.ToolCall) (string, oasis.Usage) {
		return "", oasis.Usage{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code: `import time; time.sleep(10)`,
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected timeout error")
	}
	if !strings.Contains(result.Error, "timed out") {
		t.Errorf("expected timeout message, got: %s", result.Error)
	}
}

func TestSubprocessRunner_Blocklist(t *testing.T) {
	runner := NewSubprocessRunner("python3")
	dispatch := func(ctx context.Context, tc oasis.ToolCall) (string, oasis.Usage) {
		return "", oasis.Usage{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code: `os.system("rm -rf /")`,
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" || !strings.Contains(result.Error, "blocked") {
		t.Errorf("expected blocked error, got: %s", result.Error)
	}
}

func TestSubprocessRunner_PrintGoesToLogs(t *testing.T) {
	runner := NewSubprocessRunner("python3")
	dispatch := func(ctx context.Context, tc oasis.ToolCall) (string, oasis.Usage) {
		return "", oasis.Usage{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code: `
print("debug info here")
set_result({"status": "ok"})
`,
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Logs, "debug info here") {
		t.Errorf("expected logs to contain print output, got: %s", result.Logs)
	}

	var out map[string]any
	json.Unmarshal([]byte(result.Output), &out)
	if out["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", out["status"])
	}
}

func TestSubprocessRunner_ToolError(t *testing.T) {
	runner := NewSubprocessRunner("python3")
	dispatch := func(ctx context.Context, tc oasis.ToolCall) (string, oasis.Usage) {
		return "error: file not found", oasis.Usage{}
	}

	result, err := runner.Run(context.Background(), oasis.CodeRequest{
		Code: `
try:
    call_tool('file_read', {'path': 'nonexistent.txt'})
    set_result({"found": True})
except RuntimeError as e:
    set_result({"found": False, "error": str(e)})
`,
	}, dispatch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out map[string]any
	json.Unmarshal([]byte(result.Output), &out)
	if out["found"] != false {
		t.Errorf("expected found=false, got %v", out["found"])
	}
}

func TestAPICompiles(t *testing.T) {
	runner := NewSubprocessRunner("python3",
		WithTimeout(30*time.Second),
		WithWorkspace("/tmp"),
	)
	_ = oasis.WithCodeExecution(runner)
}
