package ixd

import (
	"context"
	"net/http"
)

type shellExecRequest struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Timeout int    `json:"timeout"`
}

// handleShellExec runs a shell command and streams output via SSE.
func (s *Server) handleShellExec(w http.ResponseWriter, r *http.Request) {
	var req shellExecRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	sse := newSSEWriter(w)
	if sse == nil {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ctx := r.Context()

	// Keep-alive pings until the request completes or client disconnects.
	pingCtx, pingCancel := context.WithCancel(ctx)
	defer pingCancel()
	go sse.keepAlive(pingCtx)

	runProcess(ctx, req.Command, req.Cwd, req.Timeout, sse)
}
