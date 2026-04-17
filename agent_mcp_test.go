package oasis

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/mcp"
	"github.com/nevindra/oasis/mcp/mcptest"
)

// nullProvider is a minimal Provider for agent construction tests that do not
// exercise LLM interactions.
type nullProvider struct{}

func (nullProvider) Name() string { return "null" }
func (nullProvider) Chat(_ context.Context, _ ChatRequest) (ChatResponse, error) {
	return ChatResponse{Content: "null"}, nil
}
func (nullProvider) ChatStream(_ context.Context, _ ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	close(ch)
	return ChatResponse{Content: "null"}, nil
}

// Task 11.1 tests

func TestAgent_WithMCPServers_RegistersAtConstruction(t *testing.T) {
	// Empty variadic must not panic and should not register any servers.
	a := NewLLMAgent("a", "test", nullProvider{},
		WithMCPServers(), // empty variadic — must be accepted
	)
	if a.MCP() == nil {
		t.Fatal("MCP() returned nil after WithMCPServers()")
	}
	if n := len(a.MCP().List()); n != 0 {
		t.Errorf("expected 0 servers, got %d", n)
	}
}

func TestAgent_MCP_Accessor_ReturnsController(t *testing.T) {
	a := NewLLMAgent("a", "test", nullProvider{})
	ctrl := a.MCP()
	if ctrl == nil {
		t.Fatal("MCP() returned nil")
	}
	statuses := ctrl.List()
	if len(statuses) != 0 {
		t.Errorf("fresh agent should have 0 servers, got %d", len(statuses))
	}
}

func TestAgent_WithSharedMCPRegistry_SharesAcrossAgents(t *testing.T) {
	shared := NewSharedMCPRegistry()

	a1 := NewLLMAgent("a1", "test", nullProvider{}, WithSharedMCPRegistry(shared))
	a2 := NewLLMAgent("a2", "test", nullProvider{}, WithSharedMCPRegistry(shared))

	// Register a server directly into the shared registry via the test seam.
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "x"}}
	out, in := fake.Pipes()
	defer fake.Stop()
	if err := shared.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatalf("registerWithClient: %v", err)
	}

	if n := len(a1.MCP().List()); n != 1 {
		t.Errorf("a1 should see 1 server, got %d", n)
	}
	if n := len(a2.MCP().List()); n != 1 {
		t.Errorf("a2 should see 1 server (shared), got %d", n)
	}
}

func TestAgent_WithSharedMCPRegistry_IndependentRegistriesByDefault(t *testing.T) {
	a1 := NewLLMAgent("a1", "test", nullProvider{})
	a2 := NewLLMAgent("a2", "test", nullProvider{})

	// Each agent gets its own registry; they must not share.
	reg1 := a1.MCP()
	reg2 := a2.MCP()
	if reg1 == nil || reg2 == nil {
		t.Fatal("MCP() returned nil")
	}

	// Register a server into a1; a2 must not see it.
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "y"}}
	out, in := fake.Pipes()
	defer fake.Stop()
	if err := a1.mcpRegistry.registerWithClient(context.Background(), StdioMCPConfig{Name: "t"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatalf("registerWithClient: %v", err)
	}

	if n := len(reg1.List()); n != 1 {
		t.Errorf("a1 should see 1 server, got %d", n)
	}
	if n := len(reg2.List()); n != 0 {
		t.Errorf("a2 should see 0 servers (independent), got %d", n)
	}
}

func TestAgent_WithMCPLifecycleHandler_SetOnRegistry(t *testing.T) {
	calls := 0
	h := &countingMCPLifecycle{onConnect: func() { calls++ }}

	a := NewLLMAgent("a", "test", nullProvider{}, WithMCPLifecycleHandler(h))

	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{{Name: "z"}}
	out, in := fake.Pipes()
	defer fake.Stop()
	if err := a.mcpRegistry.registerWithClient(context.Background(), StdioMCPConfig{Name: "u"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatalf("registerWithClient: %v", err)
	}

	if calls != 1 {
		t.Errorf("OnConnect called %d times, want 1", calls)
	}
}

// Task 11.2 test

func TestLLMAgent_ImplementsMCPAccessor(t *testing.T) {
	a := NewLLMAgent("a", "test", nullProvider{})
	var ma MCPAccessor = a // compile-time check
	_ = ma.MCP()
}

// --- helpers ---

type countingMCPLifecycle struct {
	NoopMCPLifecycle
	onConnect func()
}

func (c *countingMCPLifecycle) OnConnect(name string, info MCPServerInfo) {
	if c.onConnect != nil {
		c.onConnect()
	}
}
