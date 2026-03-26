package ixd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
)

type codeExecuteRequest struct {
	Language string `json:"language"`
	Code     string `json:"code"`
	Timeout  int    `json:"timeout"`
}

// handleCodeExecute runs code in the specified language and streams output via SSE.
func (s *Server) handleCodeExecute(w http.ResponseWriter, r *http.Request) {
	var req codeExecuteRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	command, cleanup, err := buildCodeCommand(req.Language, req.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cleanup != nil {
		defer cleanup()
	}

	sse := newSSEWriter(w)
	if sse == nil {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ctx := r.Context()

	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go sse.keepAlive(pingCtx)

	runProcess(ctx, command, "", req.Timeout, sse)
}

// buildCodeCommand returns the shell command to execute the given code,
// an optional cleanup function for temp files, and any error.
func buildCodeCommand(language, code string) (command string, cleanup func(), err error) {
	switch strings.ToLower(language) {
	case "python":
		if isSimpleCode(code) {
			return fmt.Sprintf("python3 -c %s", shellQuote(code)), nil, nil
		}
		return writeTempAndRun("python3", ".py", code)

	case "javascript", "js":
		if isSimpleCode(code) {
			return fmt.Sprintf("node -e %s", shellQuote(code)), nil, nil
		}
		return writeTempAndRun("node", ".js", code)

	case "bash", "sh", "shell":
		return fmt.Sprintf("bash -c %s", shellQuote(code)), nil, nil

	default:
		return "", nil, fmt.Errorf("unsupported language: %s", language)
	}
}

// isSimpleCode returns true if code is a single line without quotes or
// characters that would make -c/-e invocation unreliable.
func isSimpleCode(code string) bool {
	return !strings.Contains(code, "\n") && len(code) < 500
}

// writeTempAndRun writes code to a temp file, returns the command to run it,
// and a cleanup function that removes the temp file.
func writeTempAndRun(runtime, ext, code string) (string, func(), error) {
	id := randomID()
	path := fmt.Sprintf("/tmp/ix_%s%s", id, ext)
	if err := os.WriteFile(path, []byte(code), 0644); err != nil {
		return "", nil, fmt.Errorf("failed to write temp file: %w", err)
	}
	cleanup := func() { os.Remove(path) }
	return fmt.Sprintf("%s %s", runtime, path), cleanup, nil
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// randomID returns a random 16-byte hex string for use in temp file names.
func randomID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
