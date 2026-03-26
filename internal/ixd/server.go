package ixd

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"
)

// Server is the ix HTTP daemon. It serves shell execution, code execution,
// and file operations inside a sandbox container.
type Server struct {
	addr    string
	startAt time.Time
	srv     *http.Server
}

// NewServer creates a new ix server that will listen on addr.
func NewServer(addr string) *Server {
	s := &Server{
		addr:    addr,
		startAt: time.Now(),
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

	// File transfer
	mux.HandleFunc("POST /v1/file/upload", s.handleFileUpload)
	mux.HandleFunc("GET /v1/file/download", s.handleFileDownload)

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
