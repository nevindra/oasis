package oasis

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/mcp"
)

type stubMCPClient struct {
	callRet  *mcp.CallToolResult
	callErr  error
	seenName string
	seenArgs json.RawMessage
}

func (s *stubMCPClient) Initialize(ctx context.Context) (*mcp.InitializeResult, error) {
	return nil, nil
}
func (s *stubMCPClient) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	return nil, nil
}
func (s *stubMCPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	s.seenName = name
	s.seenArgs = args
	return s.callRet, s.callErr
}
func (s *stubMCPClient) Close(ctx context.Context) error { return nil }
func (s *stubMCPClient) OnDisconnect(fn func(error))     {}

func TestMcpToolWrapper_Execute_Healthy(t *testing.T) {
	client := &stubMCPClient{
		callRet: &mcp.CallToolResult{Content: []mcp.ContentBlock{{Type: "text", Text: "ok"}}},
	}
	server := &mcpServerEntry{client: client}
	server.state.Store(int32(MCPStateHealthy))
	entry := &mcpToolEntry{rawName: "echo", fullName: "mcp__test__echo"}
	w := &mcpToolWrapper{entry: entry, server: server}

	// Real oasis.Tool interface: Execute(ctx, name string, args json.RawMessage) (ToolResult, error)
	result, err := w.Execute(context.Background(), "mcp__test__echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if result.Error != "" {
		t.Errorf("result.Error: %s", result.Error)
	}
	if client.seenName != "echo" {
		t.Errorf("called raw name %q, want %q", client.seenName, "echo")
	}
}

func TestMcpToolWrapper_Execute_NotHealthy(t *testing.T) {
	server := &mcpServerEntry{cfg: StdioMCPConfig{Name: "x"}}
	server.state.Store(int32(MCPStateReconnecting))
	w := &mcpToolWrapper{entry: &mcpToolEntry{fullName: "mcp__x__y"}, server: server}

	// Real interface: returns (ToolResult, error) — value, not pointer
	result, _ := w.Execute(context.Background(), "mcp__x__y", nil)
	if result.Error == "" {
		t.Error("expected ToolResult.Error to be set")
	}
}

func TestMcpToolWrapper_Execute_TransportError(t *testing.T) {
	client := &stubMCPClient{callErr: context.DeadlineExceeded}
	server := &mcpServerEntry{client: client, cfg: StdioMCPConfig{Name: "x"}}
	server.state.Store(int32(MCPStateHealthy))
	w := &mcpToolWrapper{
		entry:  &mcpToolEntry{rawName: "y", fullName: "mcp__x__y"},
		server: server,
	}

	// Must not return a Go error per PHILOSOPHY §4 — transport errors become ToolResult.Error
	result, err := w.Execute(context.Background(), "mcp__x__y", nil)
	if err != nil {
		t.Errorf("Go err must be nil per PHILOSOPHY §4: %v", err)
	}
	if result.Error == "" {
		t.Error("expected ToolResult.Error")
	}
}

func TestMcpToolWrapper_Definitions(t *testing.T) {
	def := ToolDefinition{Name: "mcp__test__echo", Description: "echo tool"}
	entry := &mcpToolEntry{fullName: "mcp__test__echo", def: def}
	w := &mcpToolWrapper{entry: entry, server: &mcpServerEntry{}}

	defs := w.Definitions()
	if len(defs) != 1 {
		t.Fatalf("Definitions() returned %d items, want 1", len(defs))
	}
	if defs[0].Name != "mcp__test__echo" {
		t.Errorf("definition name = %q, want %q", defs[0].Name, "mcp__test__echo")
	}
}

func TestMcpToolWrapper_Execute_ContentMapping(t *testing.T) {
	client := &stubMCPClient{
		callRet: &mcp.CallToolResult{
			Content: []mcp.ContentBlock{
				{Type: "text", Text: "line1"},
				{Type: "text", Text: "line2"},
			},
		},
	}
	server := &mcpServerEntry{client: client}
	server.state.Store(int32(MCPStateHealthy))
	w := &mcpToolWrapper{
		entry:  &mcpToolEntry{rawName: "multi", fullName: "mcp__test__multi"},
		server: server,
	}

	result, err := w.Execute(context.Background(), "mcp__test__multi", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "line1\nline2" {
		t.Errorf("content = %q, want %q", result.Content, "line1\nline2")
	}
}

func TestMcpToolWrapper_Execute_IsError(t *testing.T) {
	client := &stubMCPClient{
		callRet: &mcp.CallToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "tool error message"}},
			IsError: true,
		},
	}
	server := &mcpServerEntry{client: client}
	server.state.Store(int32(MCPStateHealthy))
	w := &mcpToolWrapper{
		entry:  &mcpToolEntry{rawName: "failing", fullName: "mcp__test__failing"},
		server: server,
	}

	result, err := w.Execute(context.Background(), "mcp__test__failing", nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected ToolResult.Error when IsError=true")
	}
	if result.Content != "" {
		t.Errorf("expected empty Content when IsError=true, got %q", result.Content)
	}
}
