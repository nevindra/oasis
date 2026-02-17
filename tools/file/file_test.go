package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFileWrite(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "test.txt", "content": "hello"})
	result, _ := tool.Execute(context.Background(), "file_write", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "test.txt"))
	if string(data) != "hello" {
		t.Errorf("wrong content: %s", data)
	}
}

func TestFileRead(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("content here"), 0644)
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "test.txt"})
	result, _ := tool.Execute(context.Background(), "file_read", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Content != "content here" {
		t.Errorf("wrong content: %q", result.Content)
	}
}

func TestFileWriteSubdir(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "sub/dir/file.txt", "content": "nested"})
	result, _ := tool.Execute(context.Background(), "file_write", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "sub/dir/file.txt"))
	if string(data) != "nested" {
		t.Errorf("wrong content: %s", data)
	}
}

func TestFilePathTraversal(t *testing.T) {
	tool := New(t.TempDir())
	args, _ := json.Marshal(map[string]string{"path": "../etc/passwd"})
	result, _ := tool.Execute(context.Background(), "file_read", args)
	if result.Error == "" {
		t.Error("expected path traversal error")
	}
}

func TestFileAbsolutePath(t *testing.T) {
	tool := New(t.TempDir())
	args, _ := json.Marshal(map[string]string{"path": "/etc/passwd"})
	result, _ := tool.Execute(context.Background(), "file_read", args)
	if result.Error == "" {
		t.Error("expected absolute path error")
	}
}

func TestFileReadTruncation(t *testing.T) {
	dir := t.TempDir()
	bigContent := make([]byte, 10000)
	for i := range bigContent {
		bigContent[i] = 'A'
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), bigContent, 0644)
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "big.txt"})
	result, _ := tool.Execute(context.Background(), "file_read", args)
	if len(result.Content) > 8100 { // 8000 + truncation message
		t.Errorf("content not truncated: %d chars", len(result.Content))
	}
}
