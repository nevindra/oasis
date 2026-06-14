package oasis_test

import (
	"reflect"
	"testing"

	"github.com/nevindra/oasis"
	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/network"
	"github.com/nevindra/oasis/ratelimit"
	"github.com/nevindra/oasis/workflow"
)

// TestReexportsNonNil guards against dangling umbrella re-exports: every
// curated oasis.X var must be wired to a real symbol (non-nil func value).
// A nil here means the source symbol was renamed/removed without updating the
// umbrella.
func TestReexportsNonNil(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"NewAgent", oasis.NewAgent},
		{"NewNetwork", oasis.NewNetwork},
		{"NewWorkflow", oasis.NewWorkflow},
		{"Spawn", oasis.Spawn},
		{"Subscribe", oasis.Subscribe},
		{"NewID", oasis.NewID},
		{"NewInMemoryToolResultStore", oasis.NewInMemoryToolResultStore},
		{"Chat", oasis.Chat},
		{"WithTools", oasis.WithTools},
		{"WithPrompt", oasis.WithPrompt},
		{"WithMemory", oasis.WithMemory},
		{"WithGeneration", oasis.WithGeneration},
		{"WithStream", oasis.WithStream},
		{"RateLimitMiddleware", oasis.RateLimitMiddleware},
		{"RPM", oasis.RPM},
		{"TPM", oasis.TPM},
		{"TextResult", oasis.TextResult},
		{"ErrorResult", oasis.ErrorResult},
		{"RawTool", oasis.RawTool},
		{"SystemMessage", oasis.SystemMessage},
		{"UserMessage", oasis.UserMessage},
		{"AssistantMessage", oasis.AssistantMessage},
		{"AllStreamEventTypes", oasis.AllStreamEventTypes},
	}
	for _, c := range cases {
		v := reflect.ValueOf(c.v)
		if !v.IsValid() || (v.Kind() == reflect.Func && v.IsNil()) {
			t.Errorf("oasis.%s is nil — dangling re-export", c.name)
		}
	}
}

// TestReexportsIdentity asserts each var re-export points at its source symbol,
// not merely a non-nil unrelated func. Compared by reflect pointer value.
func TestReexportsIdentity(t *testing.T) {
	cases := []struct {
		name     string
		reexport any
		source   any
	}{
		{"NewAgent", oasis.NewAgent, agent.New},
		{"NewNetwork", oasis.NewNetwork, network.New},
		{"NewWorkflow", oasis.NewWorkflow, workflow.New},
		{"Spawn", oasis.Spawn, agent.Spawn},
		{"Subscribe", oasis.Subscribe, agent.Subscribe},
		{"Chat", oasis.Chat, core.Chat},
		{"RateLimitMiddleware", oasis.RateLimitMiddleware, ratelimit.RateLimitMiddleware},
		{"RPM", oasis.RPM, ratelimit.RPM},
		{"TPM", oasis.TPM, ratelimit.TPM},
		{"TextResult", oasis.TextResult, core.TextResult},
		{"ErrorResult", oasis.ErrorResult, core.ErrorResult},
		{"RawTool", oasis.RawTool, core.RawTool},
		{"NewID", oasis.NewID, core.NewID},
	}
	for _, c := range cases {
		got := reflect.ValueOf(c.reexport).Pointer()
		want := reflect.ValueOf(c.source).Pointer()
		if got != want {
			t.Errorf("oasis.%s does not point at its source symbol", c.name)
		}
	}
}

// TestGenericWrapperReexports exercises the generic-func wrappers (which can't
// be aliased as vars) and the Ptr helper to ensure they delegate to core.
func TestGenericWrapperReexports(t *testing.T) {
	if p := oasis.Ptr(0.2); p == nil || *p != 0.2 {
		t.Errorf("oasis.Ptr returned %v, want pointer to 0.2", p)
	}

	if r := oasis.JSONResult(map[string]int{"a": 1}); r.Content == "" {
		t.Error("oasis.JSONResult returned empty Content")
	}

	if r := oasis.TextResult("hi"); r.Content != "hi" {
		t.Errorf("oasis.TextResult Content = %q, want %q", r.Content, "hi")
	}

	// ToolMeta type alias must equal core.ToolMeta.
	var _ oasis.ToolMeta = core.ToolMeta{Name: "x", Description: "d"}

	reg := oasis.NewToolRegistry()
	if reg == nil {
		t.Error("oasis.NewToolRegistry returned nil")
	}
	chain := oasis.NewProcessorChain()
	if chain == nil {
		t.Error("oasis.NewProcessorChain returned nil")
	}
}
