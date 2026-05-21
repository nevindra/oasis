package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type stubRegistryTool struct{ name string }

func (s stubRegistryTool) Name() string               { return s.name }
func (s stubRegistryTool) Definition() ToolDefinition { return ToolDefinition{Name: s.name} }
func (s stubRegistryTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return TextResult("ok"), nil
}

// deferredStubTool implements AnyTool + SchemaEnsurer.
type deferredStubTool struct {
	name      string
	hasSchema bool
	loadErr   error
	loadCount int
}

func (t *deferredStubTool) Name() string { return t.name }

func (t *deferredStubTool) Definition() ToolDefinition {
	d := ToolDefinition{Name: t.name, Description: "test"}
	if t.hasSchema {
		d.Parameters = json.RawMessage(`{"type":"object"}`)
	}
	return d
}
func (t *deferredStubTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return TextResult("ok"), nil
}
func (t *deferredStubTool) EnsureSchema(_ context.Context) error {
	t.loadCount++
	if t.loadErr != nil {
		return t.loadErr
	}
	t.hasSchema = true
	return nil
}

func TestToolRegistry_Remove_Existing(t *testing.T) {
	r := NewToolRegistry()
	r.Add(stubRegistryTool{name: "foo"})
	if err := r.Remove("foo"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	defs := r.AllDefinitions()
	for _, d := range defs {
		if d.Name == "foo" {
			t.Errorf("tool not removed from AllDefinitions")
		}
	}
}

func TestToolRegistry_Remove_NotFound(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Remove("nonexistent"); err == nil {
		t.Errorf("expected error for missing tool")
	}
}

func TestToolRegistry_Remove_IndexCleared(t *testing.T) {
	r := NewToolRegistry()
	r.Add(stubRegistryTool{name: "bar"})
	if err := r.Remove("bar"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Execute should return unknown-tool error, not hit the removed tool.
	result, err := r.Execute(context.Background(), "bar", nil)
	if err != nil {
		t.Fatalf("unexpected error from Execute: %v", err)
	}
	if result.Error == "" {
		t.Errorf("expected unknown-tool error in result, got content=%q", result.Content)
	}
}

func TestToolRegistry_DeferredDefinitions_ReturnsOnlyMissing(t *testing.T) {
	r := NewToolRegistry()
	r.Add(&deferredStubTool{name: "a", hasSchema: true})
	r.Add(&deferredStubTool{name: "b", hasSchema: false})

	defs := r.DeferredDefinitions()
	if len(defs) != 1 || defs[0].Name != "b" {
		t.Errorf("got %+v, expected only 'b'", defs)
	}
}

func TestToolRegistry_EnsureSchema_OnDeferredTool(t *testing.T) {
	r := NewToolRegistry()
	tool := &deferredStubTool{name: "b", hasSchema: false}
	r.Add(tool)

	if err := r.EnsureSchema(context.Background(), "b"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if tool.loadCount != 1 {
		t.Errorf("loadCount=%d", tool.loadCount)
	}

	// Idempotent: schema is now present, so EnsureSchema should not call ensureSchema again.
	if err := r.EnsureSchema(context.Background(), "b"); err != nil {
		t.Fatalf("ensure (second): %v", err)
	}
	if tool.loadCount != 1 {
		t.Errorf("expected idempotent, got loadCount=%d", tool.loadCount)
	}
}

func TestToolRegistry_EnsureSchema_NoOpOnNonDeferredTool(t *testing.T) {
	r := NewToolRegistry()
	tool := &deferredStubTool{name: "a", hasSchema: true}
	r.Add(tool)

	if err := r.EnsureSchema(context.Background(), "a"); err != nil {
		t.Errorf("ensure should noop: %v", err)
	}
	if tool.loadCount != 0 {
		t.Errorf("should not have loaded, count=%d", tool.loadCount)
	}
}

func TestToolRegistry_EnsureSchema_ToolNotFound(t *testing.T) {
	r := NewToolRegistry()
	err := r.EnsureSchema(context.Background(), "missing")
	if err == nil {
		t.Error("expected error")
	}
}

func TestToolRegistry_EnsureSchema_LoadError(t *testing.T) {
	r := NewToolRegistry()
	tool := &deferredStubTool{name: "b", loadErr: errors.New("fetch failed")}
	r.Add(tool)

	err := r.EnsureSchema(context.Background(), "b")
	if err == nil {
		t.Errorf("expected fetch failed error, got nil")
	}
}

func TestToolRegistry_EnsureSchema_ToolNotSchemaEnsurer(t *testing.T) {
	r := NewToolRegistry()
	r.Add(stubRegistryTool{name: "plain"}) // regular tool without ensureSchema method

	err := r.EnsureSchema(context.Background(), "plain")
	if err != nil {
		t.Errorf("expected no-op for non-ensurer: %v", err)
	}
}

// streamingAnyToolStub is a minimal StreamingAnyTool used for registry tests.
type streamingAnyToolStub struct{ name string }

func (s *streamingAnyToolStub) Name() string               { return s.name }
func (s *streamingAnyToolStub) Definition() ToolDefinition { return ToolDefinition{Name: s.name} }
func (s *streamingAnyToolStub) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return TextResult("ok"), nil
}
func (s *streamingAnyToolStub) ExecuteStream(_ context.Context, _ json.RawMessage, _ chan<- StreamEvent) (ToolResult, error) {
	return TextResult("ok"), nil
}

func TestToolRegistry_IsStreamingTool(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(stubRegistryTool{name: "plain"})
	reg.Add(&streamingAnyToolStub{name: "stream"})

	if reg.IsStreamingTool("stream") != true {
		t.Errorf("IsStreamingTool(stream) = false, want true")
	}
	if reg.IsStreamingTool("plain") != false {
		t.Errorf("IsStreamingTool(plain) = true, want false")
	}
	if reg.IsStreamingTool("missing") != false {
		t.Errorf("IsStreamingTool(missing) = true, want false")
	}
}
