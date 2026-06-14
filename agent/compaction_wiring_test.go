package agent

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
)

type stubCompactor struct{}

func (stubCompactor) Compact(_ context.Context, _ core.CompactRequest) (core.CompactResult, error) {
	return core.CompactResult{}, nil
}

func TestWithCompaction_StoresOnConfig(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithMemory(memory.WithCompaction(stubCompactor{}, 0.75)),
	})
	if cfg.Compactor == nil {
		t.Fatal("compactor not stored on config")
	}
	if cfg.CompactThreshold != 0.75 {
		t.Errorf("threshold = %f, want 0.75", cfg.CompactThreshold)
	}
}

func TestWithCompaction_OmittedLeavesNilCompactor(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithMemory(memory.WithHistory(memory.HistoryConfig{MaxMessages: 10})),
	})
	if cfg.Compactor != nil {
		t.Error("compactor should be nil when WithCompaction not used")
	}
}
