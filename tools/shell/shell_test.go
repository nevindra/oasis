package shell

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestShellExecEcho(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir, 5)
	args, _ := json.Marshal(map[string]any{"command": "echo hello"})
	result, err := tool.Execute(context.Background(), "shell_exec", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Content != "hello\n" {
		t.Errorf("expected 'hello\\n', got %q", result.Content)
	}
}

func TestShellExecWorkingDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/test.txt", []byte("content"), 0644)
	tool := New(dir, 5)
	args, _ := json.Marshal(map[string]any{"command": "ls test.txt"})
	result, _ := tool.Execute(context.Background(), "shell_exec", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Content != "test.txt\n" {
		t.Errorf("expected test.txt, got %q", result.Content)
	}
}

func TestShellExecBlocked(t *testing.T) {
	tool := New(t.TempDir(), 5)
	args, _ := json.Marshal(map[string]any{"command": "sudo reboot"})
	result, _ := tool.Execute(context.Background(), "shell_exec", args)
	if result.Error == "" {
		t.Error("expected blocked error")
	}
}

func TestShellExecTimeout(t *testing.T) {
	tool := New(t.TempDir(), 5)
	args, _ := json.Marshal(map[string]any{"command": "sleep 10", "timeout": 1})
	result, _ := tool.Execute(context.Background(), "shell_exec", args)
	if result.Error == "" {
		t.Error("expected timeout error")
	}
}

func TestShellExecStderr(t *testing.T) {
	tool := New(t.TempDir(), 5)
	args, _ := json.Marshal(map[string]any{"command": "echo out && echo err >&2"})
	result, err := tool.Execute(context.Background(), "shell_exec", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "out") {
		t.Error("missing stdout content")
	}
	if !strings.Contains(result.Content, "err") {
		t.Error("missing stderr content")
	}
	if !strings.Contains(result.Content, "stderr") {
		t.Error("missing stderr separator")
	}
}

func TestShellExecExitCode(t *testing.T) {
	tool := New(t.TempDir(), 5)
	args, _ := json.Marshal(map[string]any{"command": "exit 1"})
	result, _ := tool.Execute(context.Background(), "shell_exec", args)
	if result.Error == "" {
		t.Error("expected exit error")
	}
	if !strings.Contains(result.Error, "exit") {
		t.Errorf("error should mention exit, got %q", result.Error)
	}
}

func TestShellExecEmptyCommand(t *testing.T) {
	tool := New(t.TempDir(), 5)
	args, _ := json.Marshal(map[string]any{"command": ""})
	result, _ := tool.Execute(context.Background(), "shell_exec", args)
	if result.Error == "" {
		t.Error("expected error for empty command")
	}
	if !strings.Contains(result.Error, "required") {
		t.Errorf("error should mention required, got %q", result.Error)
	}
}

func TestShellExecMaxTimeoutCapped(t *testing.T) {
	tool := New(t.TempDir(), 5)
	// timeout=999 should be capped to 300, but command finishes fast anyway
	args, _ := json.Marshal(map[string]any{"command": "echo hi", "timeout": 999})
	result, err := tool.Execute(context.Background(), "shell_exec", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "hi") {
		t.Errorf("expected 'hi', got %q", result.Content)
	}
}

func TestShellExecDefinitions(t *testing.T) {
	tool := New(t.TempDir(), 5)
	defs := tool.Definitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
	if defs[0].Name != "shell_exec" {
		t.Errorf("expected 'shell_exec', got %q", defs[0].Name)
	}
}

func TestShellExecNoOutput(t *testing.T) {
	tool := New(t.TempDir(), 5)
	args, _ := json.Marshal(map[string]any{"command": "true"})
	result, err := tool.Execute(context.Background(), "shell_exec", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Content != "(no output)" {
		t.Errorf("expected '(no output)', got %q", result.Content)
	}
}

func TestShellExecBlockedVariants(t *testing.T) {
	tool := New(t.TempDir(), 5)
	blocked := []string{
		"rm -rf /",
		"SUDO reboot",
		"mkfs.ext4 /dev/sda",
		"echo test > /dev/null && dd if=/dev/zero of=/tmp/x",
	}
	for _, cmd := range blocked {
		args, _ := json.Marshal(map[string]any{"command": cmd})
		result, _ := tool.Execute(context.Background(), "shell_exec", args)
		if result.Error == "" {
			t.Errorf("expected %q to be blocked", cmd)
		}
	}
}
