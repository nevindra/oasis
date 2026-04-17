// Package mcptest provides an in-memory MCP server fixture for testing MCP
// clients. It implements the minimal MCP protocol (initialize, tools/list,
// tools/call, notifications/initialized) over in-process pipes and supports
// scripted behavior: configurable tool returns, error injection, hang
// simulation, and crash simulation.
//
// Usage:
//
//	s := mcptest.New()
//	s.Tools = []mcp.ToolDefinition{{Name: "echo"}}
//	s.OnToolCall = func(name string, args json.RawMessage) (mcp.CallToolResult, error) {
//	    return mcp.CallToolResult{Content: []mcp.ContentBlock{{Type: "text", Text: name}}}, nil
//	}
//	out, in := s.Pipes()
//	defer s.Stop()
//	c := mcp.NewStdioClientFromPipes(out, in)
package mcptest

import (
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/oasis/mcp"
)

// Server is a controllable in-memory MCP server for testing the MCP client.
// It implements the minimal MCP protocol over in-process io.Pipe pairs.
//
// Configure Tools and OnToolCall before calling Pipes(). Control methods
// (Crash, HangNext, Stop) are safe to call concurrently from any goroutine.
type Server struct {
	// Tools is the list of tools returned by tools/list.
	Tools []mcp.ToolDefinition

	// OnToolCall is called for every tools/call request. If nil, the server
	// returns a JSON-RPC "no handler set" error for every call.
	OnToolCall func(name string, args json.RawMessage) (mcp.CallToolResult, error)

	// InitDelay, if positive, adds an artificial delay before the initialize
	// response is sent. Useful for testing timeout handling.
	InitDelay time.Duration

	// ListChangedCh, if non-nil, may be sent on to trigger a
	// notifications/tools/list_changed notification to the client.
	ListChangedCh chan struct{}

	serverReads  *io.PipeReader // server reads incoming client requests here
	serverWrites *io.PipeWriter // server writes responses here
	clientReads  *io.PipeReader // client reads server output from this end
	clientWrites *io.PipeWriter // client writes to this end

	enc     *json.Encoder // guarded by encMu; shared between serve and watchListChanged
	encMu   sync.Mutex

	hangNext atomic.Bool
	stopped  atomic.Bool
	once     sync.Once
}

// New returns a new Server ready for configuration. Call Pipes() to start it.
func New() *Server {
	return &Server{}
}

// Pipes wires up the in-process pipe pairs and starts the server goroutine.
// It returns the (read, write) pair to pass to mcp.NewStdioClientFromPipes:
//
//	out, in := s.Pipes()
//	c := mcp.NewStdioClientFromPipes(out, in)
//
// Pipes must be called exactly once per Server.
func (s *Server) Pipes() (clientReadsServerOutput io.ReadCloser, clientWritesServerInput io.WriteCloser) {
	// Pipe 1: server writes → client reads
	cr, sw := io.Pipe()
	// Pipe 2: client writes → server reads
	sr, cw := io.Pipe()

	s.clientReads = cr
	s.serverWrites = sw
	s.serverReads = sr
	s.clientWrites = cw
	s.enc = json.NewEncoder(sw)

	go s.serve()

	if s.ListChangedCh != nil {
		go s.watchListChanged()
	}

	return cr, cw
}

// writeResp encodes resp as a JSON line to serverWrites. Thread-safe.
func (s *Server) writeResp(resp interface{}) {
	s.encMu.Lock()
	defer s.encMu.Unlock()
	_ = s.enc.Encode(resp)
}

// serve is the main server loop. It reads JSON-RPC requests from serverReads
// and dispatches responses to serverWrites.
func (s *Server) serve() {
	dec := json.NewDecoder(s.serverReads)

	for {
		if s.stopped.Load() {
			return
		}

		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      interface{}     `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := dec.Decode(&req); err != nil {
			// EOF or closed pipe — normal shutdown.
			return
		}

		if s.hangNext.Swap(false) {
			// Simulate a hung server: do not respond to this request. The
			// goroutine blocks here until the test process exits. Callers
			// recover via context timeout on the client side.
			select {} //nolint:staticcheck // intentional hang for test purposes
		}

		switch req.Method {
		case "initialize":
			if s.InitDelay > 0 {
				time.Sleep(s.InitDelay)
			}
			s.writeResp(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities": map[string]interface{}{
						"tools": map[string]interface{}{
							"listChanged": true,
						},
					},
					"serverInfo": map[string]interface{}{
						"name":    "mcptest",
						"version": "0.0.1",
					},
				},
			})

		case "notifications/initialized":
			// Notification — no response expected.

		case "tools/list":
			tools := s.Tools
			if tools == nil {
				tools = []mcp.ToolDefinition{}
			}
			s.writeResp(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]interface{}{"tools": tools},
			})

		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)

			if s.OnToolCall == nil {
				s.writeResp(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error": map[string]interface{}{
						"code":    -32601,
						"message": "no handler set",
					},
				})
				continue
			}

			res, err := s.OnToolCall(p.Name, p.Arguments)
			if err != nil {
				s.writeResp(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error": map[string]interface{}{
						"code":    -1,
						"message": err.Error(),
					},
				})
				continue
			}
			s.writeResp(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  res,
			})

		default:
			s.writeResp(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error": map[string]interface{}{
					"code":    -32601,
					"message": "method not found: " + req.Method,
				},
			})
		}
	}
}

// watchListChanged listens on ListChangedCh and sends
// notifications/tools/list_changed to the client whenever a value arrives.
func (s *Server) watchListChanged() {
	for range s.ListChangedCh {
		if s.stopped.Load() {
			return
		}
		s.writeResp(map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "notifications/tools/list_changed",
			"params":  map[string]interface{}{},
		})
	}
}

// Stop shuts down the server gracefully, closing both pipe ends. Idempotent.
func (s *Server) Stop() {
	s.once.Do(func() {
		s.stopped.Store(true)
		if s.serverWrites != nil {
			_ = s.serverWrites.Close()
		}
		if s.serverReads != nil {
			_ = s.serverReads.Close()
		}
	})
}

// Crash simulates an abrupt server termination by closing both pipe ends
// without a graceful protocol shutdown. Equivalent to Stop.
func (s *Server) Crash() {
	s.Stop()
}

// HangNext causes the server to not respond to the next request it receives,
// simulating a hung or stalled server. The caller's context should have a
// timeout to recover from the hang.
func (s *Server) HangNext() {
	s.hangNext.Store(true)
}
