package agent_test

import (
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

func TestWithOverrides_PacksRunOptionsIntoRunConfig(t *testing.T) {
	opts := &agent.RunOptions{Logger: nil} // any non-nil RunOptions value
	cfg := core.ApplyRunOptions(agent.WithOverrides(opts))
	got, ok := cfg.Overrides.(*agent.RunOptions)
	if !ok {
		t.Fatalf("RunConfig.Overrides should be *agent.RunOptions, got %T", cfg.Overrides)
	}
	if got != opts {
		t.Fatalf("RunOptions pointer should round-trip")
	}
}

func TestWithOverrides_NilOpts(t *testing.T) {
	cfg := core.ApplyRunOptions(agent.WithOverrides(nil))
	if cfg.Overrides != nil {
		t.Fatalf("nil RunOptions should leave Overrides nil, got %v", cfg.Overrides)
	}
}

func TestAgentNamespaceRunOptionReExports(t *testing.T) {
	// agent.WithStream, WithDeadline, WithTracer are aliases for the core
	// equivalents so users can write either form.
	ch := make(chan core.StreamEvent, 1)
	if cfg := core.ApplyRunOptions(agent.WithStream(ch)); cfg.Stream != ch {
		t.Fatal("agent.WithStream should set RunConfig.Stream")
	}
}
