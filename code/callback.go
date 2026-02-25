package code

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	oasis "github.com/nevindra/oasis"
)

const (
	callbackPath       = "/_oasis/dispatch"
	dispatchChanBuffer = 10 // matches maxParallelDispatch in loop.go
)

// dispatchEnvelope pairs a tool call from the sandbox with a channel
// to receive the dispatch result back from the main goroutine.
type dispatchEnvelope struct {
	call    oasis.ToolCall
	replyCh chan dispatchReply // buffered(1), created by handleDispatch
}

// dispatchReply carries the resolved tool result.
type dispatchReply struct {
	content string
	isError bool
}

// callbackServer manages the per-execution dispatch channel map and
// optionally runs its own net/http server for sandbox tool callbacks.
type callbackServer struct {
	mu      sync.RWMutex
	pending map[string]chan dispatchEnvelope // executionID → channel

	srv  *http.Server // nil when externally mounted
	addr string       // resolved listen address after Start
}

func newCallbackServer() *callbackServer {
	return &callbackServer{
		pending: make(map[string]chan dispatchEnvelope),
	}
}

// Start listens on addr and serves the dispatch handler.
// Returns once the listener is established. The server runs in a background
// goroutine that exits when Close is called.
func (cs *callbackServer) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("callback server: listen %s: %w", addr, err)
	}
	cs.addr = ln.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, cs.handleDispatch)
	cs.srv = &http.Server{Handler: mux}

	go cs.srv.Serve(ln)

	return nil
}

// Addr returns the resolved listen address (e.g. "127.0.0.1:54321").
// Valid only after Start returns nil.
func (cs *callbackServer) Addr() string {
	return cs.addr
}

// Handler returns the http.Handler for external mux mounting.
// Mount at /_oasis/dispatch on your HTTP server.
func (cs *callbackServer) Handler() http.Handler {
	return http.HandlerFunc(cs.handleDispatch)
}

// register adds an execution → channel mapping before an HTTPRunner.Run() call.
func (cs *callbackServer) register(executionID string) chan dispatchEnvelope {
	ch := make(chan dispatchEnvelope, dispatchChanBuffer)
	cs.mu.Lock()
	cs.pending[executionID] = ch
	cs.mu.Unlock()
	return ch
}

// deregister removes the mapping after Run() completes.
func (cs *callbackServer) deregister(executionID string) {
	cs.mu.Lock()
	delete(cs.pending, executionID)
	cs.mu.Unlock()
}

// Close shuts down the embedded server with a bounded drain timeout.
// No-op when externally mounted.
func (cs *callbackServer) Close() error {
	if cs.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return cs.srv.Shutdown(ctx)
	}
	return nil
}

// sandboxDispatchRequest is the JSON body POSTed by the sandbox for tool calls.
type sandboxDispatchRequest struct {
	ExecutionID string          `json:"execution_id"`
	Name        string          `json:"name"`
	Args        json.RawMessage `json:"args"`
}

// sandboxDispatchResponse is returned to the sandbox after tool dispatch.
type sandboxDispatchResponse struct {
	Data  string `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// handleDispatch is the HTTP handler for /_oasis/dispatch.
// The sandbox POSTs tool call requests here; this handler routes them
// to the correct execution's dispatch goroutine via the pending map.
func (cs *callbackServer) handleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req sandboxDispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, sandboxDispatchResponse{
			Error: "invalid request: " + err.Error(),
		})
		return
	}

	// Look up the dispatch channel for this execution.
	cs.mu.RLock()
	ch, ok := cs.pending[req.ExecutionID]
	cs.mu.RUnlock()
	if !ok {
		writeJSONResponse(w, http.StatusNotFound, sandboxDispatchResponse{
			Error: "unknown execution_id: " + req.ExecutionID,
		})
		return
	}

	// Build envelope and send to the dispatch goroutine.
	replyCh := make(chan dispatchReply, 1)
	env := dispatchEnvelope{
		call: oasis.ToolCall{
			ID:   req.ExecutionID + "_" + req.Name,
			Name: req.Name,
			Args: req.Args,
		},
		replyCh: replyCh,
	}

	// Non-blocking send: if the channel is full, the execution may have
	// already completed or is overloaded.
	select {
	case ch <- env:
	case <-r.Context().Done():
		writeJSONResponse(w, http.StatusGatewayTimeout, sandboxDispatchResponse{
			Error: "request cancelled",
		})
		return
	}

	// Wait for the dispatch result.
	select {
	case reply := <-replyCh:
		resp := sandboxDispatchResponse{Data: reply.content}
		if reply.isError {
			resp.Data = ""
			resp.Error = reply.content
		}
		writeJSONResponse(w, http.StatusOK, resp)
	case <-r.Context().Done():
		writeJSONResponse(w, http.StatusGatewayTimeout, sandboxDispatchResponse{
			Error: "dispatch timeout",
		})
	}
}

func writeJSONResponse(w http.ResponseWriter, code int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "marshal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(data)
}
