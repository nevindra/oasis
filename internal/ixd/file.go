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
	"regexp"
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

	f, err := os.Open(req.Path)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found: "+req.Path)
		} else {
			writeError(w, http.StatusInternalServerError, "failed to read file: "+err.Error())
		}
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var b strings.Builder
	lineNum := 0
	written := 0

	for scanner.Scan() {
		lineNum++
		if lineNum <= req.Offset {
			continue
		}
		if written >= req.Limit {
			// Keep counting total lines without storing content.
			continue
		}
		if written > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%6d\t%s", lineNum, scanner.Text())
		written++
	}

	if err := scanner.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read file: "+err.Error())
		return
	}

	// lineNum is now the total number of lines in the file.
	writeJSON(w, http.StatusOK, fileReadResponse{
		Content:    b.String(),
		Path:       req.Path,
		TotalLines: lineNum,
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
	Pattern string   `json:"pattern"`
	Path    string   `json:"path"`
	Exclude []string `json:"exclude"`
	Limit   int      `json:"limit"`
}

type fileGlobResponse struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
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
	if req.Limit <= 0 {
		req.Limit = 1000
	}
	if len(req.Exclude) == 0 {
		req.Exclude = []string{".git"}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	files, err := globFiles(ctx, req.Pattern, req.Path, req.Exclude, req.Limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "glob failed: "+err.Error())
		return
	}

	truncated := len(files) > req.Limit
	if truncated {
		files = files[:req.Limit]
	}

	writeJSON(w, http.StatusOK, fileGlobResponse{Files: files, Truncated: truncated})
}

// globFiles uses fd if available, falling back to Go native filepath.WalkDir.
func globFiles(ctx context.Context, pattern, path string, exclude []string, limit int) ([]string, error) {
	// Try fd (fd-find on Ubuntu installs as fdfind).
	fdBin := "fd"
	if _, err := exec.LookPath(fdBin); err != nil {
		fdBin = "fdfind"
		if _, err := exec.LookPath(fdBin); err != nil {
			return globFilesNative(pattern, path, exclude, limit)
		}
	}

	args := []string{"--glob", pattern}
	for _, ex := range exclude {
		args = append(args, "--exclude", ex)
	}
	args = append(args, path)

	cmd := exec.CommandContext(ctx, fdBin, args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok && exitErr.ExitCode() == 1 {
			return []string{}, nil
		}
		return globFilesNative(pattern, path, exclude, limit)
	}

	var files []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			files = append(files, line)
			if len(files) >= limit {
				break
			}
		}
	}
	if files == nil {
		files = []string{}
	}
	return files, nil
}

// globFilesNative walks the directory tree and matches files against a glob pattern.
// Supports ** for recursive matching by splitting the pattern into segments.
func globFilesNative(pattern, root string, exclude []string, limit int) ([]string, error) {
	excludeSet := make(map[string]struct{}, len(exclude))
	for _, ex := range exclude {
		excludeSet[ex] = struct{}{}
	}

	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, skip := excludeSet[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}
		if len(files) >= limit {
			return fs.SkipAll
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}

		if globMatch(pattern, rel) {
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

// globMatch matches a path against a glob pattern supporting **.
// ** matches zero or more path segments.
func globMatch(pattern, path string) bool {
	patParts := splitPath(pattern)
	pathParts := splitPath(path)
	return globMatchParts(patParts, pathParts)
}

func globMatchParts(pat, path []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			pat = pat[1:]
			if len(pat) == 0 {
				return true // ** at end matches everything
			}
			// Try matching the rest of pattern against every suffix of path.
			for i := 0; i <= len(path); i++ {
				if globMatchParts(pat, path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		matched, _ := filepath.Match(pat[0], path[0])
		if !matched {
			return false
		}
		pat = pat[1:]
		path = path[1:]
	}
	return len(path) == 0
}

func splitPath(p string) []string {
	p = filepath.ToSlash(p)
	parts := strings.Split(p, "/")
	// Filter empty parts from leading/trailing slashes.
	out := parts[:0]
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// --- POST /v1/file/grep ---

type fileGrepRequest struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Glob    string `json:"glob"`
	Context int    `json:"context"`
	Limit   int    `json:"limit"`
}

type grepMatch struct {
	Path          string   `json:"path"`
	Line          int      `json:"line"`
	Content       string   `json:"content"`
	ContextBefore []string `json:"context_before,omitempty"`
	ContextAfter  []string `json:"context_after,omitempty"`
}

type fileGrepResponse struct {
	Matches   []grepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
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
	if req.Limit <= 0 {
		req.Limit = 100
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	matches, err := grepFiles(ctx, req.Pattern, req.Path, req.Glob, req.Context, req.Limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grep failed: "+err.Error())
		return
	}

	truncated := len(matches) > req.Limit
	if truncated {
		matches = matches[:req.Limit]
	}

	writeJSON(w, http.StatusOK, fileGrepResponse{Matches: matches, Truncated: truncated})
}

// grepFiles uses rg (ripgrep) if available, falling back to Go native.
func grepFiles(ctx context.Context, pattern, path, glob string, ctxLines, limit int) ([]grepMatch, error) {
	if _, err := exec.LookPath("rg"); err != nil {
		return grepFilesNative(pattern, path, glob, ctxLines, limit)
	}

	args := []string{"--json", "--line-number"}
	if ctxLines > 0 {
		args = append(args, "-C", fmt.Sprintf("%d", ctxLines))
	}
	if glob != "" {
		args = append(args, "--glob", glob)
	}
	args = append(args, pattern, path)

	cmd := exec.CommandContext(ctx, "rg", args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok && exitErr.ExitCode() == 1 {
			return []grepMatch{}, nil
		}
		return grepFilesNative(pattern, path, glob, ctxLines, limit)
	}

	return parseRgJSON(out, limit), nil
}

// rgMessage is the minimal structure of ripgrep JSON output.
type rgMessage struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text string `json:"text"`
		} `json:"lines"`
	} `json:"data"`
}

// parseRgJSON extracts match entries from ripgrep --json output.
// With context lines (-C), rg emits "context" messages before/after "match" messages.
// We collect context and attach it to each match.
func parseRgJSON(data []byte, limit int) []grepMatch {
	var matches []grepMatch
	var beforeBuf []string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		var msg rgMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "context":
			line := strings.TrimRight(msg.Data.Lines.Text, "\n")
			if len(matches) > 0 {
				// Context after the previous match.
				last := &matches[len(matches)-1]
				last.ContextAfter = append(last.ContextAfter, line)
			} else {
				beforeBuf = append(beforeBuf, line)
			}
		case "match":
			if len(matches) > 0 {
				// Transfer trailing context lines that actually belong to this match as before-context.
				// rg separates context between two matches; we split at the boundary.
			}
			m := grepMatch{
				Path:          msg.Data.Path.Text,
				Line:          msg.Data.LineNumber,
				Content:       strings.TrimRight(msg.Data.Lines.Text, "\n"),
				ContextBefore: beforeBuf,
			}
			matches = append(matches, m)
			beforeBuf = nil

			if len(matches) >= limit {
				return matches
			}
		case "begin":
			beforeBuf = nil
		}
	}
	if matches == nil {
		matches = []grepMatch{}
	}
	return matches
}

// grepFilesNative searches files using Go's regexp package.
// Supports context lines and result limit.
func grepFilesNative(pattern, root, glob string, ctxLines, limit int) ([]grepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}

	var globRe *regexp.Regexp
	if glob != "" {
		// Convert simple file glob (e.g., "*.go") to a regex for basename matching.
		globPattern := "^" + strings.ReplaceAll(strings.ReplaceAll(glob, ".", `\.`), "*", ".*") + "$"
		globRe, _ = regexp.Compile(globPattern)
	}

	var matches []grepMatch
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			if d != nil && d.IsDir() && d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if globRe != nil && !globRe.MatchString(d.Name()) {
			return nil
		}

		f, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		// Ring buffer for context-before lines.
		var ring []string
		if ctxLines > 0 {
			ring = make([]string, 0, ctxLines)
		}

		lineNum := 0
		afterRemaining := 0

		for scanner.Scan() {
			lineNum++
			line := scanner.Text()

			if afterRemaining > 0 && len(matches) > 0 {
				last := &matches[len(matches)-1]
				last.ContextAfter = append(last.ContextAfter, line)
				afterRemaining--
			}

			if re.MatchString(line) {
				var before []string
				if len(ring) > 0 {
					before = make([]string, len(ring))
					copy(before, ring)
				}
				matches = append(matches, grepMatch{
					Path:          path,
					Line:          lineNum,
					Content:       line,
					ContextBefore: before,
				})
				afterRemaining = ctxLines

				if len(matches) >= limit {
					return fs.SkipAll
				}
			}

			// Maintain ring buffer for context-before.
			if ctxLines > 0 {
				if len(ring) >= ctxLines {
					ring = ring[1:]
				}
				ring = append(ring, line)
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
