package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis/mcp"
	"github.com/nevindra/oasis/mcp/mcptest"
)

func TestDeferOptions_Apply(t *testing.T) {
	// Exercise DeferThreshold option.
	reg := mcp.NewRegistry(mcp.WithDeferredSchemas(mcp.DeferThreshold(15)))
	_ = reg // verify it constructs without panic
}

func TestDeferThreshold_Clamps(t *testing.T) {
	// Threshold is stored in the registry config; verify no panic with extreme values.
	mcp.NewRegistry(mcp.WithDeferredSchemas(mcp.DeferThreshold(-5)))
	mcp.NewRegistry(mcp.WithDeferredSchemas(mcp.DeferThreshold(150)))
}

func TestMCPRegistry_DeferredMode_SkipsSchema(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	reg.SetDeferredModeForTest(true, false, nil)

	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	defs := reg.ToolDefinitionsForTest()
	var found bool
	for _, d := range defs {
		if d.Name == "mcp__s__echo" {
			found = true
			if len(d.Parameters) != 0 {
				t.Errorf("expected deferred (empty Parameters), got: %s", d.Parameters)
			}
		}
	}
	if !found {
		t.Fatal("mcp__s__echo not registered")
	}
}

func TestMCPRegistry_DeferredMode_ExcludeKeepsSchema(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	reg.SetDeferredModeForTest(true, false, map[string]bool{"s": true})

	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	for _, d := range reg.ToolDefinitionsForTest() {
		if d.Name == "mcp__s__echo" && len(d.Parameters) == 0 {
			t.Error("excluded server should keep eager schema")
		}
	}
}

func TestMcpToolWrapper_EnsureSchema_FetchesAndCaches(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "echo", InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{}}}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewTestRegistry(t)
	reg.SetDeferredModeForTest(true, false, nil)
	if err := reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	// Find the tool wrapper and call EnsureSchema directly.
	tool, ok := reg.GetTool("s", "echo")
	if !ok {
		t.Fatal("tool not found")
	}
	type schemaEnsurer interface {
		EnsureSchema(ctx context.Context) error
	}
	ensurer, ok := tool.(schemaEnsurer)
	if !ok {
		t.Fatal("tool does not implement SchemaEnsurer")
	}
	if err := ensurer.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	for _, d := range reg.ToolDefinitionsForTest() {
		if d.Name != "mcp__s__echo" {
			continue
		}
		if len(d.Parameters) == 0 {
			t.Error("schema not loaded after EnsureSchema")
		}
		if !strings.Contains(string(d.Parameters), "properties") {
			t.Errorf("schema contents wrong: %s", d.Parameters)
		}
	}
}
