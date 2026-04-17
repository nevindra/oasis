package oasis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis/mcp"
	"github.com/nevindra/oasis/mcp/mcptest"
)

func TestDeferOptions_Apply(t *testing.T) {
	cfg := &deferConfig{thresholdPercent: -1} // sentinel: "not set"
	DeferThreshold(15)(cfg)
	if cfg.thresholdPercent != 15 {
		t.Errorf("threshold: %d", cfg.thresholdPercent)
	}

	DeferAlwaysOn()(cfg)
	if !cfg.alwaysOn {
		t.Error("alwaysOn not set")
	}

	DeferExclude("github", "fs")(cfg)
	if len(cfg.exclude) != 2 || !cfg.exclude["github"] {
		t.Errorf("exclude: %+v", cfg.exclude)
	}
}

func TestDeferThreshold_Clamps(t *testing.T) {
	cfg := &deferConfig{}
	DeferThreshold(-5)(cfg)
	if cfg.thresholdPercent != 0 {
		t.Errorf("got %d", cfg.thresholdPercent)
	}
	DeferThreshold(150)(cfg)
	if cfg.thresholdPercent != 100 {
		t.Errorf("got %d", cfg.thresholdPercent)
	}
}

func TestMCPRegistry_DeferredMode_SkipsSchema(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := newTestRegistry(t)
	reg.SetDeferredMode(&deferConfig{enabled: true})

	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	defs := reg.toolReg.AllDefinitions()
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

	reg := newTestRegistry(t)
	reg.SetDeferredMode(&deferConfig{enabled: true, exclude: map[string]bool{"s": true}})

	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	for _, d := range reg.toolReg.AllDefinitions() {
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

	reg := newTestRegistry(t)
	reg.SetDeferredMode(&deferConfig{enabled: true})
	if err := reg.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	if err := reg.toolReg.EnsureSchema(context.Background(), "mcp__s__echo"); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	for _, d := range reg.toolReg.AllDefinitions() {
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
