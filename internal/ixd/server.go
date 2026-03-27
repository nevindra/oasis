package ixd

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Server is the ix HTTP daemon. It serves shell execution, code execution,
// and file operations inside a sandbox container.
type Server struct {
	addr    string
	startAt time.Time
	srv     *http.Server
	pt      *pinchtab
}

// NewServer creates a new ix server that will listen on addr.
func NewServer(ctx context.Context, addr string) *Server {
	s := &Server{
		addr:    addr,
		startAt: time.Now(),
		pt:      newPinchtab(ctx),
	}

	mux := http.NewServeMux()

	// Health
	mux.HandleFunc("GET /health", s.handleHealth)

	// Shell execution (SSE)
	mux.HandleFunc("POST /v1/shell/exec", s.handleShellExec)

	// Code execution (SSE)
	mux.HandleFunc("POST /v1/code/execute", s.handleCodeExecute)

	// File operations (JSON)
	mux.HandleFunc("POST /v1/file/read", s.handleFileRead)
	mux.HandleFunc("POST /v1/file/write", s.handleFileWrite)
	mux.HandleFunc("POST /v1/file/edit", s.handleFileEdit)
	mux.HandleFunc("POST /v1/file/glob", s.handleFileGlob)
	mux.HandleFunc("POST /v1/file/grep", s.handleFileGrep)
	mux.HandleFunc("GET /v1/file/stat", s.handleFileStat)
	mux.HandleFunc("POST /v1/file/ls", s.handleFileLs)

	// File tree
	mux.HandleFunc("POST /v1/file/tree", s.handleFileTree)

	// File transfer
	mux.HandleFunc("POST /v1/file/upload", s.handleFileUpload)
	mux.HandleFunc("GET /v1/file/download", s.handleFileDownload)

	// HTTP fetch
	mux.HandleFunc("POST /v1/http/fetch", s.handleHTTPFetch)

	// Web search
	mux.HandleFunc("POST /v1/web/search", s.handleWebSearch)

	// Workspace info
	mux.HandleFunc("GET /v1/workspace/info", s.handleWorkspaceInfo)

	// Browser (proxied to Pinchtab)
	bp := newBrowserProxy(s.pt)
	mux.HandleFunc("POST /v1/browser/navigate", bp.handleNavigate)
	mux.HandleFunc("POST /v1/browser/action", bp.handleAction)
	mux.HandleFunc("POST /v1/browser/evaluate", bp.handleEvaluate)
	mux.HandleFunc("POST /v1/browser/find", bp.handleFind)
	mux.HandleFunc("GET /v1/browser/screenshot", bp.handleScreenshot)
	mux.HandleFunc("GET /v1/browser/snapshot", bp.handleSnapshot)
	mux.HandleFunc("GET /v1/browser/text", bp.handleText)
	mux.HandleFunc("GET /v1/browser/pdf", bp.handlePDF)

	s.srv = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return s
}

// ListenAndServe starts the HTTP server and blocks until ctx is cancelled.
// On cancellation, it initiates a graceful shutdown with a 10-second deadline.
func (s *Server) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		log.Printf("ix listening on %s", s.addr)
		errCh <- s.srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	}
}

// handleHealth returns daemon status and uptime.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"uptime_sec": int(time.Since(s.startAt).Seconds()),
		"browser":    s.pt.isAvailable(),
	})
}

// Shutdown gracefully stops the server and the Pinchtab subprocess.
func (s *Server) Shutdown() {
	s.pt.shutdown()
}

// handleWorkspaceInfo returns environment info for agent calibration.
func (s *Server) handleWorkspaceInfo(w http.ResponseWriter, r *http.Request) {
	cwd, _ := os.Getwd()

	tools := make(map[string]bool)
	for _, name := range []string{"rg", "fd", "fdfind", "git", "python3", "node", "tree", "curl", "wget"} {
		_, err := exec.LookPath(name)
		tools[name] = err == nil
	}
	// Normalize fd/fdfind into a single "fd" key.
	if tools["fdfind"] {
		tools["fd"] = true
	}
	delete(tools, "fdfind")

	writeJSON(w, http.StatusOK, map[string]any{
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"working_dir": cwd,
		"tools":       tools,
		"browser":     s.pt.isAvailable(),
	})
}

// writeJSON serializes data as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// readJSON decodes a JSON request body into dst.
func readJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("empty request body")
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
