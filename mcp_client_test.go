package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nevindra/oasis/mcp"
	"github.com/nevindra/oasis/mcp/mcptest"
)

func newTestRegistry(t *testing.T) *MCPRegistry {
	t.Helper()
	return &MCPRegistry{
		servers:  make(map[string]*mcpServerEntry),
		handler:  NoopMCPLifecycle{},
		eventsCh: make(chan MCPEvent, 64),
		logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
		toolReg:  NewToolRegistry(),
	}
}

func TestMCPRegistry_Register_Stdio(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "echo", Description: "echo input", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := newTestRegistry(t)

	err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "test"},
		mcp.NewStdioClientFromPipes(out, in))
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	statuses := reg.List()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 server, got %d", len(statuses))
	}
	if statuses[0].State != MCPStateHealthy {
		t.Errorf("state: %s", statuses[0].State)
	}
	if statuses[0].ToolCount != 1 {
		t.Errorf("tool count: %d", statuses[0].ToolCount)
	}

	// Tool should be registered with namespaced name.
	defs := reg.toolReg.AllDefinitions()
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
	reg := newTestRegistry(t)
	fake1 := mcptest.New()
	out1, in1 := fake1.Pipes()
	defer fake1.Stop()
	fake2 := mcptest.New()
	out2, in2 := fake2.Pipes()
	defer fake2.Stop()

	err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "x"},
		mcp.NewStdioClientFromPipes(out1, in1))
	if err != nil {
		t.Fatal(err)
	}

	err2 := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "x"},
		mcp.NewStdioClientFromPipes(out2, in2))
	if err2 == nil {
		t.Error("expected duplicate name error")
	}
}

func TestMCPRegistry_Register_Disabled(t *testing.T) {
	reg := newTestRegistry(t)
	err := reg.Register(context.Background(), StdioMCPConfig{
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

	reg := newTestRegistry(t)
	if err := reg.registerWithClient(context.Background(),
		StdioMCPConfig{Name: "gh", Filter: &MCPToolFilter{Include: []string{"create_*", "list_*"}}},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	defs := reg.toolReg.AllDefinitions()
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

	reg := newTestRegistry(t)
	if err := reg.registerWithClient(context.Background(),
		StdioMCPConfig{Name: "gh", Aliases: map[string]string{"create_issue": "gh_new_issue"}},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	defs := reg.toolReg.AllDefinitions()
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

	reg := newTestRegistry(t)
	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Unregister(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if len(reg.List()) != 0 {
		t.Errorf("not unregistered")
	}

	defs := reg.toolReg.AllDefinitions()
	for _, d := range defs {
		if d.Name == "mcp__s__x" {
			t.Errorf("tool not removed from ToolRegistry")
		}
	}
}

func TestMCPRegistry_OnDisconnect_Reconnects(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := fake.Pipes()

	reg := newTestRegistry(t)
	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	fake.Crash()

	// Within a few seconds the state should transition to Reconnecting (then
	// eventually Dead since we don't restart fake).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		statuses := reg.List()
		if len(statuses) > 0 && statuses[0].State == MCPStateReconnecting {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("never transitioned to Reconnecting; last status: %+v", reg.List())
}

// --- Task 10.3 tests ---

func TestMCPRegistry_ManualReconnect(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := newTestRegistry(t)
	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	// Force to dead state for test.
	reg.servers["s"].state.Store(int32(MCPStateDead))

	// Reconnect should not return ErrServerNotFound since server exists.
	err := reg.Reconnect(context.Background(), "s")
	if errors.Is(err, ErrServerNotFound) {
		t.Errorf("got ErrServerNotFound, expected attempt to be made")
	}
}

func TestMCPRegistry_GetTool(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "echo"}}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := newTestRegistry(t)
	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	tool, ok := reg.GetTool("s", "echo")
	if !ok {
		t.Fatal("tool not found")
	}
	defs := tool.Definitions()
	if len(defs) == 0 || defs[0].Name != "mcp__s__echo" {
		t.Errorf("name: %v", defs)
	}

	_, ok = reg.GetTool("s", "nonexistent")
	if ok {
		t.Error("expected false for nonexistent tool")
	}
}

func TestMCPRegistry_Unregister_NotFound(t *testing.T) {
	reg := newTestRegistry(t)
	err := reg.Unregister(context.Background(), "nonexistent")
	if !errors.Is(err, ErrServerNotFound) {
		t.Errorf("expected ErrServerNotFound, got: %v", err)
	}
}

func TestMCPRegistry_Subscribe(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := newTestRegistry(t)
	ch := reg.Subscribe()

	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-ch:
		if ev.Type != MCPEventConnected {
			t.Errorf("expected connected event, got %v", ev.Type)
		}
		if ev.Server != "s" {
			t.Errorf("expected server 's', got %q", ev.Server)
		}
	case <-time.After(time.Second):
		t.Error("no event received within 1s")
	}
}

func TestMCPRegistry_NewSharedMCPRegistry(t *testing.T) {
	reg := NewSharedMCPRegistry()
	if reg == nil {
		t.Fatal("nil registry")
	}
	if reg.servers == nil {
		t.Error("servers map not initialized")
	}
	if reg.toolReg == nil {
		t.Error("toolReg not initialized")
	}
}
