package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

// ToolHandler is a tool that the MCP server exposes to clients.
type ToolHandler struct {
	// Definition describes the tool (name, description, input schema).
	Definition ToolDefinition
	// Execute is called when the client invokes tools/call for this tool.
	Execute func(ctx context.Context, args json.RawMessage) ToolCallResult
}

// Resource is a readable data source exposed via MCP resources/list and resources/read.
type Resource struct {
	URI         string
	Name        string
	Description string
	MimeType    string
	// Read returns the resource content. Called on each resources/read request.
	Read func() string
}

// Server is an MCP server that communicates over stdio using JSON-RPC 2.0.
// Register tools and resources before calling Serve.
type Server struct {
	name    string
	version string

	tools     []ToolHandler
	resources []Resource

	// reader/writer can be overridden for testing (defaults to stdin/stdout).
	reader io.Reader
	writer io.Writer
	mu     sync.Mutex // protects writes
}

// New creates an MCP server with the given name and version.
func New(name, version string) *Server {
	return &Server{
		name:    name,
		version: version,
		reader:  os.Stdin,
		writer:  os.Stdout,
	}
}

// AddTool registers a tool handler. Must be called before Serve.
func (s *Server) AddTool(h ToolHandler) {
	s.tools = append(s.tools, h)
}

// AddResource registers a resource. Must be called before Serve.
func (s *Server) AddResource(r Resource) {
	s.resources = append(s.resources, r)
}

// Serve runs the MCP server, reading JSON-RPC messages from stdin and writing
// responses to stdout. Blocks until stdin is closed or ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	scanner := bufio.NewScanner(s.reader)
	scanner.Buffer(make([]byte, 0, 10<<20), 10<<20) // 10MB max message

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		s.handleMessage(ctx, line)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("mcp: read stdin: %w", err)
	}
	return nil
}

// handleMessage parses a single JSON-RPC message (or batch) and dispatches it.
func (s *Server) handleMessage(ctx context.Context, data []byte) {
	// Check for batch (JSON array).
	if len(data) > 0 && data[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(data, &batch); err != nil {
			s.writeResponse(response{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: errCodeParse, Message: "parse error"},
			})
			return
		}
		for _, raw := range batch {
			s.handleSingleMessage(ctx, raw)
		}
		return
	}

	s.handleSingleMessage(ctx, data)
}

// handleSingleMessage parses and dispatches a single JSON-RPC request.
func (s *Server) handleSingleMessage(ctx context.Context, data []byte) {
	var req request
	if err := json.Unmarshal(data, &req); err != nil {
		s.writeResponse(response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{Code: errCodeParse, Message: "parse error"},
		})
		return
	}

	resp := s.dispatch(ctx, &req)
	if resp != nil {
		s.writeResponse(*resp)
	}
}

// dispatch routes a request to the appropriate handler. Returns nil for notifications.
func (s *Server) dispatch(ctx context.Context, req *request) *response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		return nil // notification, no response
	case "notifications/cancelled":
		return nil
	case "ping":
		return s.respond(req.ID, struct{}{})
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "resources/list":
		return s.handleResourcesList(req)
	case "resources/read":
		return s.handleResourcesRead(req)
	default:
		if req.isNotification() {
			return nil
		}
		return s.respondError(req.ID, errCodeMethodNotFound, "method not found: "+req.Method)
	}
}

// --- handlers ---

func (s *Server) handleInitialize(req *request) *response {
	caps := serverCapabilities{}
	if len(s.tools) > 0 {
		caps.Tools = &capability{}
	}
	if len(s.resources) > 0 {
		caps.Resources = &capability{}
	}

	return s.respond(req.ID, initializeResult{
		ProtocolVersion: protocolVersion,
		Capabilities:    caps,
		ServerInfo:      serverInfo{Name: s.name, Version: s.version},
	})
}

func (s *Server) handleToolsList(req *request) *response {
	defs := make([]ToolDefinition, len(s.tools))
	for i, t := range s.tools {
		defs[i] = t.Definition
	}
	return s.respond(req.ID, toolsListResult{Tools: defs})
}

func (s *Server) handleToolsCall(ctx context.Context, req *request) *response {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
	}

	for _, t := range s.tools {
		if t.Definition.Name == params.Name {
			result := t.Execute(ctx, params.Arguments)
			return s.respond(req.ID, result)
		}
	}

	return s.respond(req.ID, ErrorResult("unknown tool: "+params.Name))
}

func (s *Server) handleResourcesList(req *request) *response {
	defs := make([]resourceDef, len(s.resources))
	for i, r := range s.resources {
		defs[i] = resourceDef{
			URI:         r.URI,
			Name:        r.Name,
			Description: r.Description,
			MimeType:    r.MimeType,
		}
	}
	return s.respond(req.ID, resourcesListResult{Resources: defs})
}

func (s *Server) handleResourcesRead(req *request) *response {
	var params resourceReadParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.respondError(req.ID, errCodeInvalidParams, "invalid params: "+err.Error())
	}

	for _, r := range s.resources {
		if r.URI == params.URI {
			return s.respond(req.ID, resourceReadResult{
				Contents: []resourceContent{{
					URI:      r.URI,
					MimeType: r.MimeType,
					Text:     r.Read(),
				}},
			})
		}
	}

	return s.respondError(req.ID, errCodeInvalidParams, "resource not found: "+params.URI)
}

// --- response helpers ---

func (s *Server) respond(id json.RawMessage, result any) *response {
	return &response{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) respondError(id json.RawMessage, code int, message string) *response {
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func (s *Server) writeResponse(resp response) {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf(" [mcp] marshal response: %v", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	data = append(data, '\n')
	if _, err := s.writer.Write(data); err != nil {
		log.Printf(" [mcp] write response: %v", err)
	}
}
