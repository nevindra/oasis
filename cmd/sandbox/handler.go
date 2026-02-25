package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// executeRequest is the parsed body of POST /execute.
type executeRequest struct {
	ExecutionID string      `json:"execution_id"`
	Code        string      `json:"code"`
	Runtime     string      `json:"runtime"`
	Timeout     int         `json:"timeout"` // seconds
	SessionID   string      `json:"session_id"`
	CallbackURL string      `json:"callback_url"`
	Files       []inputFile `json:"files,omitempty"`
}

// inputFile is a base64-encoded file to write into the workspace.
type inputFile struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64
}

// executeResponse is the JSON body returned by POST /execute.
type executeResponse struct {
	Output   string       `json:"output"`
	Logs     string       `json:"logs"`
	ExitCode int          `json:"exit_code"`
	Error    string       `json:"error,omitempty"`
	Files    []outputFile `json:"files,omitempty"`
}

// outputFile is a file produced by execution, returned base64-encoded.
type outputFile struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data string `json:"data"` // base64
}

const (
	maxRequestBodyBytes = 32 << 20 // 32MB
	defaultTimeoutSecs  = 30
	maxTimeoutSecs      = 300
)

func handleExecute(sem chan struct{}, sessions *sessionManager, run *runner, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req executeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}
	if req.Runtime != "" && req.Runtime != "python" && req.Runtime != "node" {
		writeError(w, http.StatusBadRequest, "unsupported runtime: "+req.Runtime+"; supported: python, node")
		return
	}
	if req.SessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	timeout := defaultTimeoutSecs
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	if timeout > maxTimeoutSecs {
		timeout = maxTimeoutSecs
	}

	// Acquire execution slot — fail fast under load.
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		writeError(w, http.StatusServiceUnavailable, "server busy: execution capacity reached")
		return
	}

	workspaceDir, err := sessions.get(req.SessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "workspace error: "+err.Error())
		return
	}

	if err := writeInputFiles(workspaceDir, req.Files); err != nil {
		writeError(w, http.StatusBadRequest, "file write error: "+err.Error())
		return
	}

	result := run.run(r.Context(), runRequest{
		code:         req.Code,
		runtime:      req.Runtime,
		workspaceDir: workspaceDir,
		callbackURL:  req.CallbackURL,
		executionID:  req.ExecutionID,
		timeout:      time.Duration(timeout) * time.Second,
	})

	outFiles := collectOutputFiles(workspaceDir, result.files)

	writeJSON(w, http.StatusOK, executeResponse{
		Output:   result.stdout,
		Logs:     result.stderr,
		ExitCode: result.exitCode,
		Error:    result.err,
		Files:    outFiles,
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func handleDeleteWorkspace(sessions *sessionManager, w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/workspace/")
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if err := sessions.delete(sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "marshal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(data)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
