package mcp

import (
	"context"
	"encoding/json"
	"testing"

	oasis "github.com/nevindra/oasis"
)

type stubMCPClient struct {
	callRet  *CallToolResult
	callErr  error
	seenName string
	seenArgs json.RawMessage
}

func (s *stubMCPClient) Initialize(ctx context.Context) (*InitializeResult, error) {
	return nil, nil
}
func (s *stubMCPClient) ListTools(ctx context.Context) (*ListToolsResult, error) {
	return nil, nil
}
func (s *stubMCPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	s.seenName = name
	s.seenArgs = args
	return s.callRet, s.callErr
}
func (s *stubMCPClient) Close(ctx context.Context) error { return nil }
func (s *stubMCPClient) OnDisconnect(fn func(error))     {}

func TestMcpToolWrapper_Execute_Healthy(t *testing.T) {
	client := &stubMCPClient{
		callRet: &CallToolResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}},
	}
	server := &serverEntry{client: client}
	server.state.Store(int32(StateHealthy))
	entry := &toolEntry{rawName: "echo", fullName: "mcp__test__echo"}
	w := &toolWrapper{entry: entry, server: server}

	result, err := w.ExecuteRaw(context.Background(), json.RawMessage(`{"x":1}`))
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
	server := &serverEntry{cfg: StdioConfig{Name: "x"}}
	server.state.Store(int32(StateReconnecting))
	w := &toolWrapper{entry: &toolEntry{fullName: "mcp__x__y"}, server: server}

	result, _ := w.ExecuteRaw(context.Background(), nil)
	if result.Error == "" {
		t.Error("expected ToolResult.Error to be set")
	}
}

func TestMcpToolWrapper_Execute_TransportError(t *testing.T) {
	client := &stubMCPClient{callErr: context.DeadlineExceeded}
	server := &serverEntry{client: client, cfg: StdioConfig{Name: "x"}}
	server.state.Store(int32(StateHealthy))
	w := &toolWrapper{
		entry:  &toolEntry{rawName: "y", fullName: "mcp__x__y"},
		server: server,
	}

	// Must not return a Go error per PHILOSOPHY §4 — transport errors become ToolResult.Error
	result, err := w.ExecuteRaw(context.Background(), nil)
	if err != nil {
		t.Errorf("Go err must be nil per PHILOSOPHY §4: %v", err)
	}
	if result.Error == "" {
		t.Error("expected ToolResult.Error")
	}
}

func TestMcpToolWrapper_Definitions(t *testing.T) {
	def := oasis.ToolDefinition{Name: "mcp__test__echo", Description: "echo tool"}
	entry := &toolEntry{fullName: "mcp__test__echo"}
	entry.def.Store(&def)
	w := &toolWrapper{entry: entry, server: &serverEntry{}}

	got := w.Definition()
	if got.Name != "mcp__test__echo" {
		t.Errorf("definition name = %q, want %q", got.Name, "mcp__test__echo")
	}
	if w.Name() != "mcp__test__echo" {
		t.Errorf("Name() = %q", w.Name())
	}
}

func TestMcpToolWrapper_Execute_ContentMapping(t *testing.T) {
	client := &stubMCPClient{
		callRet: &CallToolResult{
			Content: []ContentBlock{
				{Type: "text", Text: "line1"},
				{Type: "text", Text: "line2"},
			},
		},
	}
	server := &serverEntry{client: client}
	server.state.Store(int32(StateHealthy))
	w := &toolWrapper{
		entry:  &toolEntry{rawName: "multi", fullName: "mcp__test__multi"},
		server: server,
	}

	result, err := w.ExecuteRaw(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got string
	if err := json.Unmarshal(result.Content, &got); err != nil {
		t.Fatalf("Content not JSON-decodable: %v (raw=%s)", err, result.Content)
	}
	if got != "line1\nline2" {
		t.Errorf("content = %q, want %q", got, "line1\nline2")
	}
}

func TestMcpToolWrapper_Execute_IsError(t *testing.T) {
	client := &stubMCPClient{
		callRet: &CallToolResult{
			Content: []ContentBlock{{Type: "text", Text: "tool error message"}},
			IsError: true,
		},
	}
	server := &serverEntry{client: client}
	server.state.Store(int32(StateHealthy))
	w := &toolWrapper{
		entry:  &toolEntry{rawName: "failing", fullName: "mcp__test__failing"},
		server: server,
	}

	result, err := w.ExecuteRaw(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected ToolResult.Error when IsError=true")
	}
	if len(result.Content) != 0 {
		t.Errorf("expected empty Content when IsError=true, got %q", string(result.Content))
	}
}
