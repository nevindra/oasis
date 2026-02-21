package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestFileList(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "."})
	result, _ := tool.Execute(context.Background(), "file_list", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "file\ta.txt") {
		t.Errorf("expected a.txt in listing, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "dir\tsubdir") {
		t.Errorf("expected subdir in listing, got: %s", result.Content)
	}
}

func TestFileListEmpty(t *testing.T) {
	tool := New(t.TempDir())
	args, _ := json.Marshal(map[string]string{"path": "."})
	result, _ := tool.Execute(context.Background(), "file_list", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Content != "" {
		t.Errorf("expected empty listing, got: %q", result.Content)
	}
}

func TestFileListNonexistent(t *testing.T) {
	tool := New(t.TempDir())
	args, _ := json.Marshal(map[string]string{"path": "nope"})
	result, _ := tool.Execute(context.Background(), "file_list", args)
	if result.Error == "" {
		t.Error("expected error for nonexistent directory")
	}
}

func TestFileListDefaultPath(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "root.txt"), []byte("r"), 0644)
	tool := New(dir)
	// Empty path should list workspace root.
	args, _ := json.Marshal(map[string]string{})
	result, _ := tool.Execute(context.Background(), "file_list", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "root.txt") {
		t.Errorf("expected root.txt in listing, got: %s", result.Content)
	}
}

func TestFileDelete(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "del.txt"), []byte("bye"), 0644)
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "del.txt"})
	result, _ := tool.Execute(context.Background(), "file_delete", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "del.txt")); !os.IsNotExist(err) {
		t.Error("file should have been deleted")
	}
}

func TestFileDeleteEmptyDir(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "empty"), 0755)
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "empty"})
	result, _ := tool.Execute(context.Background(), "file_delete", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
}

func TestFileDeleteNonexistent(t *testing.T) {
	tool := New(t.TempDir())
	args, _ := json.Marshal(map[string]string{"path": "ghost.txt"})
	result, _ := tool.Execute(context.Background(), "file_delete", args)
	if result.Error == "" {
		t.Error("expected error for nonexistent file")
	}
}

func TestFileDeleteNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "notempty"), 0755)
	os.WriteFile(filepath.Join(dir, "notempty", "child.txt"), []byte("x"), 0644)
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "notempty"})
	result, _ := tool.Execute(context.Background(), "file_delete", args)
	if result.Error == "" {
		t.Error("expected error for non-empty directory")
	}
}

func TestFileStat(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "info.txt"), []byte("hello"), 0644)
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "info.txt"})
	result, _ := tool.Execute(context.Background(), "file_stat", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	var stat map[string]any
	if err := json.Unmarshal([]byte(result.Content), &stat); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if stat["name"] != "info.txt" {
		t.Errorf("expected name info.txt, got %v", stat["name"])
	}
	if stat["type"] != "file" {
		t.Errorf("expected type file, got %v", stat["type"])
	}
	if stat["size"] != float64(5) {
		t.Errorf("expected size 5, got %v", stat["size"])
	}
}

func TestFileStatDir(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "mydir"), 0755)
	tool := New(dir)
	args, _ := json.Marshal(map[string]string{"path": "mydir"})
	result, _ := tool.Execute(context.Background(), "file_stat", args)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	var stat map[string]any
	if err := json.Unmarshal([]byte(result.Content), &stat); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if stat["type"] != "directory" {
		t.Errorf("expected type directory, got %v", stat["type"])
	}
}

func TestFileStatNonexistent(t *testing.T) {
	tool := New(t.TempDir())
	args, _ := json.Marshal(map[string]string{"path": "nope.txt"})
	result, _ := tool.Execute(context.Background(), "file_stat", args)
	if result.Error == "" {
		t.Error("expected error for nonexistent path")
	}
}

func TestFileDefinitions(t *testing.T) {
	tool := New(t.TempDir())
	defs := tool.Definitions()
	if len(defs) != 5 {
		t.Fatalf("expected 5 definitions, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"file_read", "file_write", "file_list", "file_delete", "file_stat"} {
		if !names[want] {
			t.Errorf("missing %s definition", want)
		}
	}
}
