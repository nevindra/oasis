package oasis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevindra/oasis/mcp"
	"github.com/nevindra/oasis/mcp/mcptest"
)

func TestWithDeferredSchemas_ActivatesDeferredMode(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "x", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	// New agent WITH deferred schemas + shared registry. Must register the
	// server through the registry AFTER the agent is constructed so the
	// deferred mode is in effect when registerTools runs.
	shared := NewSharedMCPRegistry()
	_ = NewLLMAgent("a", "test", nullProvider{},
		WithSharedMCPRegistry(shared),
		WithDeferredSchemas(),
	)
	if err := shared.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	for _, d := range shared.toolReg.AllDefinitions() {
		if d.Name == "mcp__s__x" && len(d.Parameters) != 0 {
			t.Errorf("deferred: schema should be empty, got %s", d.Parameters)
		}
	}
}

func TestWithoutDeferredSchemas_KeepsEagerSchema(t *testing.T) {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "x", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	shared := NewSharedMCPRegistry()
	_ = NewLLMAgent("a", "test", nullProvider{}, WithSharedMCPRegistry(shared))
	if err := shared.registerWithClient(context.Background(), StdioMCPConfig{Name: "s"},
		mcp.NewStdioClientFromPipes(out, in)); err != nil {
		t.Fatal(err)
	}

	var sawSchema bool
	for _, d := range shared.toolReg.AllDefinitions() {
		if d.Name == "mcp__s__x" && len(d.Parameters) > 0 {
			sawSchema = true
		}
	}
	if !sawSchema {
		t.Error("non-deferred agent should have eager schema")
	}
}

func TestWithDeferredSchemas_AutoRegistersToolSearch(t *testing.T) {
	a := NewLLMAgent("a", "test", nullProvider{}, WithDeferredSchemas())
	var found bool
	for _, d := range a.tools.AllDefinitions() {
		if d.Name == toolSearchName {
			found = true
		}
	}
	if !found {
		t.Error("ToolSearch not auto-registered when deferred enabled")
	}
}

func TestWithoutDeferredSchemas_NoToolSearch(t *testing.T) {
	a := NewLLMAgent("a", "test", nullProvider{})
	for _, d := range a.tools.AllDefinitions() {
		if d.Name == toolSearchName {
			t.Error("ToolSearch should NOT be registered without WithDeferredSchemas")
		}
	}
}

func TestWithDeferredSchemas_PrependsSystemPrompt(t *testing.T) {
	a := NewLLMAgent("a", "test", nullProvider{},
		WithPrompt("you are helpful"),
		WithDeferredSchemas(),
	)
	prompt := a.systemPrompt
	if !strings.Contains(prompt, "<deferred-tools>") {
		t.Errorf("system prompt missing deferred-tools section: %s", prompt)
	}
	if !strings.Contains(prompt, "you are helpful") {
		t.Errorf("user prompt should be preserved: %s", prompt)
	}
	deferredIdx := strings.Index(prompt, "<deferred-tools>")
	userIdx := strings.Index(prompt, "you are helpful")
	if deferredIdx < 0 || userIdx < 0 || deferredIdx > userIdx {
		t.Errorf("deferred-tools should precede user prompt; idx: %d, %d", deferredIdx, userIdx)
	}
}
