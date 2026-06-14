package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nevindra/oasis/mcp"
	"github.com/nevindra/oasis/mcp/mcptest"
)

func TestMCPRegistry_Register_Stdio(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "echo", Description: "echo input", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)

	err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "test"},
		mcp.NewStdioClientFromPipes(out, in))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	statuses := reg.List()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 server, got %d", len(statuses))
	}
	if statuses[0].State != mcp.StateHealthy {
		t.Errorf("state: %s", statuses[0].State)
	}
	if statuses[0].ToolCount != 1 {
		t.Errorf("tool count: %d", statuses[0].ToolCount)
	}

	// Tool should be registered with namespaced name.
	defs := reg.ToolDefinitionsForTest()
	var found bool
	for _, d := range defs {
		if d.Name == "mcp__test__echo" {
			found = true
		}
	}
	if !found {
		t.Errorf("namespaced tool not registered: %+v", defs)
	}
}

func TestMCPRegistry_Register_DuplicateName(t *testing.T) {
	reg := mcp.NewTestRegistry(t)
	fake1 := mcptest.New()
	out1, in1 := fake1.Pipes()
	defer fake1.Stop()
	fake2 := mcptest.New()
	out2, in2 := fake2.Pipes()
	defer fake2.Stop()

	err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "x"},
		mcp.NewStdioClientFromPipes(out1, in1))
	if err != nil {
		t.Fatal(err)
	}

	err2 := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "x"},
		mcp.NewStdioClientFromPipes(out2, in2))
	if err2 == nil {
		t.Error("expected duplicate name error")
	}
}

func TestMCPRegistry_Register_Disabled(t *testing.T) {
	reg := mcp.NewTestRegistry(t)
	err := reg.Register(context.Background(), mcp.StdioConfig{
		Name: "x", Command: "true", Disabled: true,
	})
	if err != nil {
		t.Fatalf("disabled register: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Error("disabled server should not be tracked")
	}
}

func TestMCPRegistry_Filter_Include(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "create_issue"}, {Name: "list_issues"}, {Name: "delete_repo"},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	if err := reg.RegisterTestClient(context.Background(),
		mcp.StdioConfig{Name: "gh", Filter: &mcp.ToolFilter{Include: []string{"create_*", "list_*"}}},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	defs := reg.ToolDefinitionsForTest()
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
	}
	if reg.List()[0].ToolCount != 2 {
		t.Errorf("filter: expected 2 tools, got %d (names: %v)", reg.List()[0].ToolCount, names)
	}
}

func TestMCPRegistry_Aliases(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "create_issue"}}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	if err := reg.RegisterTestClient(context.Background(),
		mcp.StdioConfig{Name: "gh", Aliases: map[string]string{"create_issue": "gh_new_issue"}},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	defs := reg.ToolDefinitionsForTest()
	var found bool
	for _, d := range defs {
		if d.Name == "mcp__gh__gh_new_issue" {
			found = true
		}
	}
	if !found {
		t.Errorf("alias not applied: %+v", defs)
	}
}

func TestMCPRegistry_Unregister(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Unregister(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if len(reg.List()) != 0 {
		t.Errorf("not unregistered")
	}

	defs := reg.ToolDefinitionsForTest()
	for _, d := range defs {
		if d.Name == "mcp__s__x" {
			t.Errorf("tool not removed from registry")
		}
	}
}

func TestMCPRegistry_OnDisconnect_Reconnects(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := fake.Pipes()

	reg := mcp.NewTestRegistry(t)
	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	fake.Crash()

	// Within a few seconds the state should transition to Reconnecting (then
	// eventually Dead since we don't restart fake).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		statuses := reg.List()
		if len(statuses) > 0 && statuses[0].State == mcp.StateReconnecting {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("never transitioned to Reconnecting; last status: %+v", reg.List())
}

func TestMCPRegistry_GetTool(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "echo"}}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.GetTool("s", "echo")
	if !ok {
		t.Fatal("tool not found")
	}
	def := tool.Definition()
	if def.Name != "mcp__s__echo" {
		t.Errorf("name: %v", def)
	}

	_, ok = reg.GetTool("s", "nonexistent")
	if ok {
		t.Error("expected false for nonexistent tool")
	}
}

func TestMCPRegistry_Unregister_NotFound(t *testing.T) {
	reg := mcp.NewTestRegistry(t)
	err := reg.Unregister(context.Background(), "nonexistent")
	if !errors.Is(err, mcp.ErrServerNotFound) {
		t.Errorf("expected ErrServerNotFound, got: %v", err)
	}
}

func TestMCPRegistry_Subscribe(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	ch := reg.Subscribe()

	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Type != mcp.EventConnected {
			t.Errorf("expected connected event, got %v", ev.Type)
		}
		if ev.Server != "s" {
			t.Errorf("expected server 's', got %q", ev.Server)
		}
	case <-time.After(time.Second):
		t.Error("no event received within 1s")
	}
}

func TestRegistry_Resources_StdioRoundTrip(t *testing.T) {
	srv := mcptest.New()
	srv.Capabilities = map[string]interface{}{"resources": map[string]interface{}{}}
	srv.OnResourcesList = func() []map[string]interface{} {
		return []map[string]interface{}{{"uri": "file:///a.txt", "name": "a", "mimeType": "text/plain"}}
	}
	srv.OnResourceRead = func(uri string) []map[string]interface{} {
		return []map[string]interface{}{{"uri": uri, "mimeType": "text/plain", "text": "hello"}}
	}
	out, in := srv.Pipes()
	defer srv.Stop()

	reg := mcp.NewTestRegistry(t)
	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "fs"}, mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatalf("register: %v", err)
	}

	list, err := reg.ListResources(context.Background(), "fs")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].URI != "file:///a.txt" || list[0].Name != "a" {
		t.Fatalf("list = %+v", list)
	}

	contents, err := reg.ReadResource(context.Background(), "fs", "file:///a.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(contents) != 1 || contents[0].Text != "hello" {
		t.Fatalf("contents = %+v", contents)
	}
}

func TestRegistry_Resources_Unsupported(t *testing.T) {
	srv := mcptest.New() // default caps: tools only, no resources
	out, in := srv.Pipes()
	defer srv.Stop()

	reg := mcp.NewTestRegistry(t)
	_ = reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "x"}, mcp.NewStdioClientFromPipes(out, in))

	if _, err := reg.ListResources(context.Background(), "x"); !errors.Is(err, mcp.ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}

func TestRegistry_Resources_ServerNotFound(t *testing.T) {
	reg := mcp.NewTestRegistry(t)
	if _, err := reg.ListResources(context.Background(), "nope"); !errors.Is(err, mcp.ErrServerNotFound) {
		t.Errorf("want ErrServerNotFound, got %v", err)
	}
}

func TestRegistry_Subscribe_StdioAndUpdate(t *testing.T) {
	srv := mcptest.New()
	srv.Capabilities = map[string]interface{}{"resources": map[string]interface{}{"subscribe": true}}
	out, in := srv.Pipes()
	defer srv.Stop()

	reg := mcp.NewTestRegistry(t)
	events := reg.Subscribe()
	_ = reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "fs"}, mcp.NewStdioClientFromPipes(out, in))

	if err := reg.SubscribeResource(context.Background(), "fs", "file:///a"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	srv.SendNotification("notifications/resources/updated", map[string]interface{}{"uri": "file:///a"})

	// Drain until the resource-updated event (connect/other events may precede).
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == mcp.EventResourceUpdated {
				if e.URI != "file:///a" || e.Server != "fs" {
					t.Fatalf("event = %+v", e)
				}
				return
			}
		case <-deadline:
			t.Fatal("no resource-updated event")
		}
	}
}

func TestRegistry_Subscribe_HTTPUnsupported(t *testing.T) {
	// httptest server that advertises resources during initialize, so the
	// capability short-circuit passes and we reach the transport-capability
	// assertion (HTTPClient does not implement resourceSubscriber).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"x","capabilities":{"resources":{}},"serverInfo":{"name":"h","version":"1"}}}`, req.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, req.ID)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID)
		}
	}))
	defer ts.Close()

	reg := mcp.NewTestRegistry(t)
	if err := reg.RegisterTestClient(context.Background(),
		mcp.HTTPConfig{Name: "h"}, mcp.NewHTTPClient(ts.URL, nil, nil, 0)); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := reg.SubscribeResource(context.Background(), "h", "x"); !errors.Is(err, mcp.ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}

func TestRegistry_Prompts_StdioRoundTrip(t *testing.T) {
	srv := mcptest.New()
	srv.Capabilities = map[string]interface{}{"prompts": map[string]interface{}{}}
	srv.OnPromptsList = func() []map[string]interface{} {
		return []map[string]interface{}{{"name": "summarize", "description": "d",
			"arguments": []map[string]interface{}{{"name": "tone", "required": true}}}}
	}
	srv.OnPromptGet = func(name string, args map[string]string) map[string]interface{} {
		return map[string]interface{}{
			"description": "got " + name + " " + args["tone"],
			"messages": []map[string]interface{}{
				{"role": "user", "content": map[string]interface{}{"type": "text", "text": "hi"}},
			},
		}
	}
	out, in := srv.Pipes()
	defer srv.Stop()

	reg := mcp.NewTestRegistry(t)
	_ = reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "p"}, mcp.NewStdioClientFromPipes(out, in))

	prompts, err := reg.ListPrompts(context.Background(), "p")
	if err != nil || len(prompts) != 1 || prompts[0].Name != "summarize" ||
		len(prompts[0].Arguments) != 1 || !prompts[0].Arguments[0].Required {
		t.Fatalf("prompts=%+v err=%v", prompts, err)
	}

	res, err := reg.GetPrompt(context.Background(), "p", "summarize", map[string]string{"tone": "dry"})
	if err != nil || res.Description != "got summarize dry" ||
		len(res.Messages) != 1 || res.Messages[0].Content.Text != "hi" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// TestMCPRegistry_NewRegistry verifies NewRegistry produces a valid, usable registry.
func TestMCPRegistry_NewRegistry(t *testing.T) {
	reg := mcp.NewRegistry()
	if reg == nil {
		t.Fatal("nil registry")
	}
	// Verify it works — register disabled server (no subprocess needed).
	err := reg.Register(context.Background(), mcp.StdioConfig{
		Name: "x", Command: "true", Disabled: true,
	})
	if err != nil {
		t.Fatalf("disabled register on new registry: %v", err)
	}
}

// TestMCPRegistry_ManualReconnect verifies that Reconnect doesn't return
// ErrServerNotFound for a known server even when forced to StateDead.
// Note: uses RegisterTestClient + forces state via List() check; the server
// entry's state field is not directly accessible from mcp_test package, but
// we can force dead by crashing the fake then letting reconnects exhaust.
// Here we just verify Reconnect doesn't error on a known server.
func TestMCPRegistry_ManualReconnect(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	// Reconnect should not return ErrServerNotFound since server exists.
	err := reg.Reconnect(context.Background(), "s")
	if errors.Is(err, mcp.ErrServerNotFound) {
		t.Errorf("got ErrServerNotFound, expected attempt to be made")
	}
}

func TestRegistry_SetLogLevel_AndEvent(t *testing.T) {
	srv := mcptest.New()
	srv.Capabilities = map[string]interface{}{"logging": map[string]interface{}{}}
	out, in := srv.Pipes()
	defer srv.Stop()

	reg := mcp.NewTestRegistry(t)
	events := reg.Subscribe()
	_ = reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "l"}, mcp.NewStdioClientFromPipes(out, in))

	if err := reg.SetLogLevel(context.Background(), "l", mcp.LogLevelWarning); err != nil {
		t.Fatalf("setLevel: %v", err)
	}

	srv.SendNotification("notifications/message", map[string]interface{}{"level": "error", "data": "boom"})

	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == mcp.EventLog {
				if e.Level != mcp.LogLevelError || e.Message != "boom" || e.Server != "l" {
					t.Fatalf("event = %+v", e)
				}
				return
			}
		case <-deadline:
			t.Fatal("no log event")
		}
	}
}
