package shell

import (
	"context"
	"encoding/json"
	"os"
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
