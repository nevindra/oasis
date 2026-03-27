package ixd

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type fileTreeRequest struct {
	Path    string   `json:"path"`
	Depth   int      `json:"depth"`
	Exclude []string `json:"exclude"`
}

type fileTreeResponse struct {
	Tree  string `json:"tree"`
	Files int    `json:"files"`
	Dirs  int    `json:"dirs"`
}

func (s *Server) handleFileTree(w http.ResponseWriter, r *http.Request) {
	var req fileTreeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Path == "" {
		req.Path = "."
	}
	if req.Depth <= 0 {
		req.Depth = 3
	}
	if len(req.Exclude) == 0 {
		req.Exclude = []string{".git", "node_modules", "__pycache__", ".venv", "vendor"}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tree, files, dirs, err := buildTree(ctx, req.Path, req.Depth, req.Exclude)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tree failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, fileTreeResponse{Tree: tree, Files: files, Dirs: dirs})
}

// buildTree tries the tree command first, falling back to native Go.
func buildTree(ctx context.Context, path string, depth int, exclude []string) (string, int, int, error) {
	if treeBin, err := exec.LookPath("tree"); err == nil {
		tree, files, dirs, err := treeCommand(ctx, treeBin, path, depth, exclude)
		if err == nil {
			return tree, files, dirs, nil
		}
	}
	return treeNative(path, depth, exclude)
}

func treeCommand(ctx context.Context, bin, path string, depth int, exclude []string) (string, int, int, error) {
	args := []string{"-L", fmt.Sprintf("%d", depth), "--noreport", "--charset", "ascii"}
	if len(exclude) > 0 {
		args = append(args, "-I", strings.Join(exclude, "|"))
	}
	args = append(args, path)

	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", 0, 0, err
	}

	tree := strings.TrimRight(string(out), "\n")

	// Count files and dirs from the output.
	files, dirs := 0, 0
	for _, line := range strings.Split(tree, "\n") {
		trimmed := strings.TrimLeft(line, " |`-\\")
		if trimmed == "" {
			continue
		}
		if strings.HasSuffix(trimmed, "/") {
			dirs++
		} else {
			files++
		}
	}

	return tree, files, dirs, nil
}

func treeNative(root string, maxDepth int, exclude []string) (string, int, int, error) {
	excludeSet := make(map[string]struct{}, len(exclude))
	for _, ex := range exclude {
		excludeSet[ex] = struct{}{}
	}

	var b strings.Builder
	files, dirs := 0, 0
	entries := 0
	const maxEntries = 500

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}

		// Skip root itself.
		if rel == "." {
			b.WriteString(filepath.Base(root) + "/\n")
			return nil
		}

		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if depth > maxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			if _, skip := excludeSet[d.Name()]; skip {
				return fs.SkipDir
			}
		}

		if entries >= maxEntries {
			return fs.SkipAll
		}
		entries++

		indent := strings.Repeat("  ", depth)
		if d.IsDir() {
			dirs++
			fmt.Fprintf(&b, "%s%s/\n", indent, d.Name())
		} else {
			files++
			fmt.Fprintf(&b, "%s%s\n", indent, d.Name())
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}

	return strings.TrimRight(b.String(), "\n"), files, dirs, nil
}
