package ixd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// --- POST /v1/file/upload ---

func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	// Limit upload size to 1GB.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
		return
	}

	targetPath := r.FormValue("path")
	if targetPath == "" {
		writeError(w, http.StatusBadRequest, "path field is required")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file field is required: "+err.Error())
		return
	}
	defer file.Close()

	// Create parent directories if needed.
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create directory: "+err.Error())
		return
	}

	dst, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create file: "+err.Error())
		return
	}
	defer dst.Close()

	n, err := io.Copy(dst, file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	if err := dst.Sync(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync file: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]int64{"bytes_written": n})
}

// --- GET /v1/file/download ---

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found: "+filePath)
		} else {
			writeError(w, http.StatusInternalServerError, "failed to open file: "+err.Error())
		}
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to stat file: "+err.Error())
		return
	}

	if info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is a directory, not a file")
		return
	}

	filename := filepath.Base(filePath)
	w.Header().Set("Content-Disposition", formatContentDisposition(filename))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))

	// http.ServeContent handles Range requests, Content-Type detection,
	// and conditional requests (If-Modified-Since, If-None-Match).
	http.ServeContent(w, r, filename, info.ModTime(), f)
}

// formatContentDisposition formats the Content-Disposition header value.
// Uses RFC 5987 encoding for non-ASCII filenames.
// Adapted from OpenSandbox execd filesystem_download.go.
func formatContentDisposition(filename string) string {
	needsEncoding := false
	for _, r := range filename {
		if r > 127 {
			needsEncoding = true
			break
		}
	}

	if !needsEncoding {
		return fmt.Sprintf("attachment; filename=%q", filename)
	}

	encoded := url.PathEscape(filename)
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", encoded, encoded)
}
