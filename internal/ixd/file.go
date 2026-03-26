package ixd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// --- POST /v1/file/read ---

type fileReadRequest struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type fileReadResponse struct {
	Content    string `json:"content"`
	Path       string `json:"path"`
	TotalLines int    `json:"total_lines"`
}

func (s *Server) handleFileRead(w http.ResponseWriter, r *http.Request) {
	var req fileReadRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if req.Limit <= 0 {
		req.Limit = 2000
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found: "+req.Path)
		} else {
			writeError(w, http.StatusInternalServerError, "failed to read file: "+err.Error())
		}
		return
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	// Apply offset and limit (line-based).
	start := req.Offset
	if start < 0 {
		start = 0
	}
	if start > totalLines {
		start = totalLines
	}
	end := start + req.Limit
	if end > totalLines {
		end = totalLines
	}

	content := strings.Join(lines[start:end], "\n")

	writeJSON(w, http.StatusOK, fileReadResponse{
		Content:    content,
		Path:       req.Path,
		TotalLines: totalLines,
	})
}

// --- POST /v1/file/write ---

type fileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type fileWriteResponse struct {
	BytesWritten int `json:"bytes_written"`
}

func (s *Server) handleFileWrite(w http.ResponseWriter, r *http.Request) {
	var req fileWriteRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	// Create parent directories if needed.
	dir := filepath.Dir(req.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create directory: "+err.Error())
		return
	}

	data := []byte(req.Content)
	if err := os.WriteFile(req.Path, data, 0644); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, fileWriteResponse{BytesWritten: len(data)})
}

// --- POST /v1/file/edit ---

type fileEditRequest struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type fileEditResponse struct {
	Applied bool   `json:"applied"`
	Path    string `json:"path"`
}

func (s *Server) handleFileEdit(w http.ResponseWriter, r *http.Request) {
	var req fileEditRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if req.Old == "" {
		writeError(w, http.StatusBadRequest, "old string is required")
		return
	}

	data, err := os.ReadFile(req.Path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found: "+req.Path)
		} else {
			writeError(w, http.StatusInternalServerError, "failed to read file: "+err.Error())
		}
		return
	}

	content := string(data)
	count := strings.Count(content, req.Old)
	if count == 0 {
		writeError(w, http.StatusBadRequest, "old string not found in file")
		return
	}
	if count > 1 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("old string is not unique: found %d occurrences", count))
		return
	}

	newContent := strings.Replace(content, req.Old, req.New, 1)

	// Preserve original file permissions.
	info, err := os.Stat(req.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to stat file: "+err.Error())
		return
	}

	if err := os.WriteFile(req.Path, []byte(newContent), info.Mode()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, fileEditResponse{Applied: true, Path: req.Path})
}

// --- POST /v1/file/glob ---

type fileGlobRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type fileGlobResponse struct {
	Files []string `json:"files"`
}

func (s *Server) handleFileGlob(w http.ResponseWriter, r *http.Request) {
	var req fileGlobRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Pattern == "" {
		writeError(w, http.StatusBadRequest, "pattern is required")
		return
	}
	if req.Path == "" {
		req.Path = "."
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	files, err := globFiles(ctx, req.Pattern, req.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "glob failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, fileGlobResponse{Files: files})
}

// globFiles uses fd if available, falling back to Go native filepath.WalkDir.
func globFiles(ctx context.Context, pattern, path string) ([]string, error) {
	// Try fd (fd-find on Ubuntu installs as fdfind).
	fdBin := "fd"
	if _, err := exec.LookPath(fdBin); err != nil {
		fdBin = "fdfind"
		if _, err := exec.LookPath(fdBin); err != nil {
			return globFilesNative(pattern, path)
		}
	}

	cmd := exec.CommandContext(ctx, fdBin, "--glob", pattern, path)
	out, err := cmd.Output()
	if err != nil {
		// fd returns exit code 1 if no matches. That's not an error.
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok && exitErr.ExitCode() == 1 {
			return []string{}, nil
		}
		// Fallback to native on any other error.
		return globFilesNative(pattern, path)
	}

	var files []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			files = append(files, line)
		}
	}
	if files == nil {
		files = []string{}
	}
	return files, nil
}

// globFilesNative walks the directory tree and matches files using filepath.Match.
// This is the fallback when fd is not installed.
func globFilesNative(pattern, root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if d.IsDir() {
			return nil
		}
		// Match against base name for simple patterns, full path for ** patterns.
		matched, matchErr := filepath.Match(pattern, filepath.Base(path))
		if matchErr != nil {
			return nil
		}
		if matched {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []string{}
	}
	return files, nil
}

// --- POST /v1/file/grep ---

type fileGrepRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Glob    string `json:"glob"`
}

type grepMatch struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type fileGrepResponse struct {
	Matches []grepMatch `json:"matches"`
}

func (s *Server) handleFileGrep(w http.ResponseWriter, r *http.Request) {
	var req fileGrepRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Pattern == "" {
		writeError(w, http.StatusBadRequest, "pattern is required")
		return
	}
	if req.Path == "" {
		req.Path = "."
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	matches, err := grepFiles(ctx, req.Pattern, req.Path, req.Glob)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grep failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, fileGrepResponse{Matches: matches})
}

// grepFiles uses rg (ripgrep) if available, falling back to Go native.
func grepFiles(ctx context.Context, pattern, path, glob string) ([]grepMatch, error) {
	if _, err := exec.LookPath("rg"); err != nil {
		return grepFilesNative(pattern, path)
	}

	args := []string{"--json", "--line-number", pattern, path}
	if glob != "" {
		args = append(args, "--glob", glob)
	}

	cmd := exec.CommandContext(ctx, "rg", args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok && exitErr.ExitCode() == 1 {
			// rg returns 1 when no matches found.
			return []grepMatch{}, nil
		}
		// Fallback to native on other errors.
		return grepFilesNative(pattern, path)
	}

	return parseRgJSON(out), nil
}

// rgMessage is the minimal structure of ripgrep JSON output for match lines.
type rgMessage struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber  int `json:"line_number"`
		Lines       struct {
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

// parseRgJSON extracts match entries from ripgrep --json output.
func parseRgJSON(data []byte) []grepMatch {
	var matches []grepMatch
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var msg rgMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Type != "match" {
			continue
		}
		matches = append(matches, grepMatch{
			Path:    msg.Data.Path.Text,
			Line:    msg.Data.LineNumber,
			Content: strings.TrimRight(msg.Data.Lines.Text, "\n"),
		})
	}
	if matches == nil {
		matches = []grepMatch{}
	}
	return matches
}

// grepFilesNative is a basic fallback grep using Go's standard library.
// It searches files line-by-line for the pattern as a substring.
func grepFilesNative(pattern, root string) ([]grepMatch, error) {
	var matches []grepMatch
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if strings.Contains(line, pattern) {
				matches = append(matches, grepMatch{
					Path:    path,
					Line:    lineNum,
					Content: line,
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if matches == nil {
		matches = []grepMatch{}
	}
	return matches, nil
}

// --- GET /v1/file/stat ---

type fileStatResponse struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

func (s *Server) handleFileStat(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found: "+path)
		} else {
			writeError(w, http.StatusInternalServerError, "failed to stat: "+err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, fileStatResponse{
		Path:    path,
		Size:    info.Size(),
		Mode:    fmt.Sprintf("%04o", info.Mode().Perm()),
		ModTime: info.ModTime().Format(time.RFC3339),
		IsDir:   info.IsDir(),
	})
}

// --- POST /v1/file/ls ---

type fileLsRequest struct {
	Path string `json:"path"`
}

type fileLsEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

type fileLsResponse struct {
	Entries []fileLsEntry `json:"entries"`
}

func (s *Server) handleFileLs(w http.ResponseWriter, r *http.Request) {
	var req fileLsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	dirEntries, err := os.ReadDir(req.Path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "directory not found: "+req.Path)
		} else {
			writeError(w, http.StatusInternalServerError, "failed to read directory: "+err.Error())
		}
		return
	}

	entries := make([]fileLsEntry, 0, len(dirEntries))
	for _, d := range dirEntries {
		entry := fileLsEntry{
			Name: d.Name(),
		}
		if d.IsDir() {
			entry.Type = "dir"
		} else {
			entry.Type = "file"
			if info, err := d.Info(); err == nil {
				entry.Size = info.Size()
			}
		}
		entries = append(entries, entry)
	}

	writeJSON(w, http.StatusOK, fileLsResponse{Entries: entries})
}

// asExitError checks if err wraps an *exec.ExitError.
func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
