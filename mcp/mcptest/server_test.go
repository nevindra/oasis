package mcptest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nevindra/oasis/mcp"
)

func TestServer_BasicHandshakeAndCall(t *testing.T) {
	s := New()
	s.Tools = []mcp.ToolDefinition{
		{Name: "echo", Description: "echos input", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	s.OnToolCall = func(name string, args json.RawMessage) (mcp.CallToolResult, error) {
		return mcp.CallToolResult{Content: []mcp.ContentBlock{{Type: "text", Text: "got: " + name}}}, nil
	}

	out, in := s.Pipes()
	defer s.Stop()

	// Use the same StdioClient used in production, but with our pipes.
	c := mcp.NewStdioClientFromPipes(out, in)
	defer c.Close(context.Background())

	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if info.ServerInfo.Name == "" {
		t.Errorf("empty server info: %+v", info)
	}

	list, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "echo" {
		t.Errorf("unexpected tools: %+v", list)
	}

	res, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "got: echo" {
		t.Errorf("call result: %+v", res)
	}
}

func TestServer_Crash(t *testing.T) {
	s := New()
	out, in := s.Pipes()
	c := mcp.NewStdioClientFromPipes(out, in)

	disconnected := make(chan error, 1)
	c.OnDisconnect(func(err error) { disconnected <- err })

	s.Crash()

	select {
	case <-disconnected:
		// ok
	case <-time.After(time.Second):
		t.Fatal("crash didn't trigger disconnect")
	}
}

func TestServer_HangNext(t *testing.T) {
	s := New()
	s.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := s.Pipes()
	c := mcp.NewStdioClientFromPipes(out, in)
	defer c.Close(context.Background())

	c.Initialize(context.Background())
	s.HangNext()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.CallTool(ctx, "x", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
