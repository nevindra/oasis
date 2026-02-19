package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// testServer creates a Server wired to in-memory reader/writer for testing.
func testServer() (*Server, *bytes.Buffer) {
	srv := New("test-server", "1.0.0")
	var out bytes.Buffer
	srv.writer = &out
	return srv, &out
}

// sendAndReceive writes a JSON-RPC message to the server and returns the response.
func sendAndReceive(t *testing.T, srv *Server, out *bytes.Buffer, msg string) response {
	t.Helper()
	out.Reset()
	srv.reader = strings.NewReader(msg + "\n")
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v (raw: %s)", err, out.String())
	}
	return resp
}

func TestInitializeHandshake(t *testing.T) {
	srv, out := testServer()
	srv.AddTool(ToolHandler{
		Definition: ToolDefinition{Name: "test_tool", Description: "a test tool"},
		Execute:    func(_ context.Context, _ json.RawMessage) ToolCallResult { return TextResult("ok") },
	})
	srv.AddResource(Resource{
		URI: "test://doc", Name: "doc", MimeType: "text/plain",
		Read: func() string { return "content" },
	})

	resp := sendAndReceive(t, srv, out,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	raw, _ := json.Marshal(resp.Result)
	var result initializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	if result.ProtocolVersion != protocolVersion {
		t.Errorf("protocolVersion = %q, want %q", result.ProtocolVersion, protocolVersion)
	}
	if result.ServerInfo.Name != "test-server" {
		t.Errorf("serverInfo.name = %q, want %q", result.ServerInfo.Name, "test-server")
	}
	if result.Capabilities.Tools == nil {
		t.Error("expected tools capability to be set")
	}
	if result.Capabilities.Resources == nil {
		t.Error("expected resources capability to be set")
	}
}

func TestInitializeNoToolsNoResources(t *testing.T) {
	srv, out := testServer()

	resp := sendAndReceive(t, srv, out,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`)

	raw, _ := json.Marshal(resp.Result)
	var result initializeResult
	json.Unmarshal(raw, &result)

	if result.Capabilities.Tools != nil {
		t.Error("expected tools capability to be nil when no tools registered")
	}
	if result.Capabilities.Resources != nil {
		t.Error("expected resources capability to be nil when no resources registered")
	}
}

func TestPing(t *testing.T) {
	srv, out := testServer()
	resp := sendAndReceive(t, srv, out, `{"jsonrpc":"2.0","id":42,"method":"ping"}`)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if string(resp.ID) != "42" {
		t.Errorf("id = %s, want 42", resp.ID)
	}
}

func TestToolsList(t *testing.T) {
	srv, out := testServer()
	srv.AddTool(ToolHandler{
		Definition: ToolDefinition{
			Name:        "search_docs",
			Description: "Search documentation",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []string{"query"},
			},
		},
		Execute: func(_ context.Context, _ json.RawMessage) ToolCallResult { return TextResult("ok") },
	})

	resp := sendAndReceive(t, srv, out, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	raw, _ := json.Marshal(resp.Result)
	var result toolsListResult
	json.Unmarshal(raw, &result)

	if len(result.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(result.Tools))
	}
	if result.Tools[0].Name != "search_docs" {
		t.Errorf("tool name = %q, want %q", result.Tools[0].Name, "search_docs")
	}
}

func TestToolsCall(t *testing.T) {
	srv, out := testServer()
	srv.AddTool(ToolHandler{
		Definition: ToolDefinition{Name: "echo", Description: "Echo input"},
		Execute: func(_ context.Context, args json.RawMessage) ToolCallResult {
			var params struct{ Text string `json:"text"` }
			json.Unmarshal(args, &params)
			return TextResult("echo: " + params.Text)
		},
	})

	resp := sendAndReceive(t, srv, out,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello"}}}`)

	raw, _ := json.Marshal(resp.Result)
	var result ToolCallResult
	json.Unmarshal(raw, &result)

	if result.IsError {
		t.Error("expected isError=false")
	}
	if len(result.Content) != 1 || result.Content[0].Text != "echo: hello" {
		t.Errorf("unexpected content: %+v", result.Content)
	}
}

func TestToolsCallUnknown(t *testing.T) {
	srv, out := testServer()

	resp := sendAndReceive(t, srv, out,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nonexistent","arguments":{}}}`)

	raw, _ := json.Marshal(resp.Result)
	var result ToolCallResult
	json.Unmarshal(raw, &result)

	if !result.IsError {
		t.Error("expected isError=true for unknown tool")
	}
}

func TestResourcesList(t *testing.T) {
	srv, out := testServer()
	srv.AddResource(Resource{
		URI: "oasis://docs/architecture", Name: "Architecture",
		Description: "Framework architecture", MimeType: "text/markdown",
		Read: func() string { return "# Architecture" },
	})

	resp := sendAndReceive(t, srv, out, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)

	raw, _ := json.Marshal(resp.Result)
	var result resourcesListResult
	json.Unmarshal(raw, &result)

	if len(result.Resources) != 1 {
		t.Fatalf("got %d resources, want 1", len(result.Resources))
	}
	if result.Resources[0].URI != "oasis://docs/architecture" {
		t.Errorf("uri = %q, want %q", result.Resources[0].URI, "oasis://docs/architecture")
	}
}

func TestResourcesRead(t *testing.T) {
	srv, out := testServer()
	srv.AddResource(Resource{
		URI: "oasis://docs/test", Name: "Test Doc", MimeType: "text/markdown",
		Read: func() string { return "# Test\nHello world" },
	})

	resp := sendAndReceive(t, srv, out,
		`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"oasis://docs/test"}}`)

	raw, _ := json.Marshal(resp.Result)
	var result resourceReadResult
	json.Unmarshal(raw, &result)

	if len(result.Contents) != 1 {
		t.Fatalf("got %d contents, want 1", len(result.Contents))
	}
	if result.Contents[0].Text != "# Test\nHello world" {
		t.Errorf("text = %q, want %q", result.Contents[0].Text, "# Test\nHello world")
	}
}

func TestResourcesReadNotFound(t *testing.T) {
	srv, out := testServer()

	resp := sendAndReceive(t, srv, out,
		`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"oasis://nonexistent"}}`)

	if resp.Error == nil {
		t.Fatal("expected error for nonexistent resource")
	}
	if resp.Error.Code != errCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, errCodeInvalidParams)
	}
}

func TestUnknownMethod(t *testing.T) {
	srv, out := testServer()

	resp := sendAndReceive(t, srv, out,
		`{"jsonrpc":"2.0","id":1,"method":"unknown/method"}`)

	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != errCodeMethodNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, errCodeMethodNotFound)
	}
}

func TestNotificationNoResponse(t *testing.T) {
	srv, out := testServer()
	out.Reset()
	srv.reader = strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no output for notification, got: %s", out.String())
	}
}

func TestBatchRequest(t *testing.T) {
	srv, out := testServer()
	out.Reset()
	srv.reader = strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"ping"}]` + "\n")
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	// Should get two responses (each on its own line).
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d response lines, want 2", len(lines))
	}

	for i, line := range lines {
		var resp response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("line %d: unmarshal: %v", i, err)
		}
		if resp.Error != nil {
			t.Errorf("line %d: unexpected error: %v", i, resp.Error)
		}
	}
}

func TestParseError(t *testing.T) {
	srv, out := testServer()
	out.Reset()
	srv.reader = strings.NewReader("not-json\n")
	srv.Serve(context.Background())

	var resp response
	json.Unmarshal(out.Bytes(), &resp)

	if resp.Error == nil {
		t.Fatal("expected parse error")
	}
	if resp.Error.Code != errCodeParse {
		t.Errorf("error code = %d, want %d", resp.Error.Code, errCodeParse)
	}
}
