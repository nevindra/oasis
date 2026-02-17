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

func TestFileReadNonexistent(t *testing.T) {
	tool := New(t.TempDir())
	args, _ := json.Marshal(map[string]string{"path": "does_not_exist.txt"})
	result, _ := tool.Execute(context.Background(), "file_read", args)
	if result.Error == "" {
		t.Error("expected error for nonexistent file")
	}
}

func TestFileWriteOverwrite(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir)

	// First write
	args, _ := json.Marshal(map[string]string{"path": "ow.txt", "content": "first"})
	tool.Execute(context.Background(), "file_write", args)

	// Overwrite
	args, _ = json.Marshal(map[string]string{"path": "ow.txt", "content": "second"})
	result, _ := tool.Execute(context.Background(), "file_write", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "ow.txt"))
	if string(data) != "second" {
		t.Errorf("expected 'second', got %q", string(data))
	}
}

func TestFileWriteEmptyContent(t *testing.T) {
	dir := t.TempDir()
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "empty.txt", "content": ""})
	result, _ := tool.Execute(context.Background(), "file_write", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}

	info, err := os.Stat(filepath.Join(dir, "empty.txt"))
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("expected 0 bytes, got %d", info.Size())
	}
}

func TestFileDefinitions(t *testing.T) {
	tool := New(t.TempDir())
	defs := tool.Definitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["file_read"] {
		t.Error("missing file_read definition")
	}
	if !names["file_write"] {
		t.Error("missing file_write definition")
	}
}
