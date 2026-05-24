package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

func TestApplyRunOptions_AppliesEachOption(t *testing.T) {
	ch := make(chan core.StreamEvent, 1)
	cfg := core.ApplyRunOptions(
		core.WithStream(ch),
		core.WithDeadline(2*time.Second),
	)
	if cfg.Stream != ch {
		t.Fatalf("WithStream not applied: got %v want %v", cfg.Stream, ch)
	}
	if cfg.Deadline != 2*time.Second {
		t.Fatalf("WithDeadline not applied: got %v want %v", cfg.Deadline, 2*time.Second)
	}
}

func TestApplyRunOptions_LaterWins(t *testing.T) {
	cfg := core.ApplyRunOptions(
		core.WithDeadline(1*time.Second),
		core.WithDeadline(5*time.Second),
	)
	if cfg.Deadline != 5*time.Second {
		t.Fatalf("later option should win: got %v", cfg.Deadline)
	}
}

func TestApplyRunOptions_NoOptions(t *testing.T) {
	cfg := core.ApplyRunOptions()
	if cfg.Stream != nil || cfg.Deadline != 0 || cfg.Tracer != nil || cfg.Overrides != nil {
		t.Fatalf("zero-option config should be empty: %+v", cfg)
	}
}

func TestAgentInterfaceAcceptsRunOptions(t *testing.T) {
	// Compile-time check: the variadic ...RunOption signature must compile.
	var _ core.Agent = stubAgent{}
}

type stubAgent struct{}

func (stubAgent) Name() string        { return "stub" }
func (stubAgent) Description() string { return "stub" }
func (stubAgent) Execute(_ context.Context, _ core.AgentTask, _ ...core.RunOption) (core.AgentResult, error) {
	return core.AgentResult{}, nil
}
