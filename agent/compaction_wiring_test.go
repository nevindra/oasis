package agent

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/history"
)

type stubCompactor struct{}

func (stubCompactor) Compact(_ context.Context, _ CompactRequest) (CompactResult, error) {
	return CompactResult{}, nil
}

func TestWithCompaction_StoresOnConfig(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithHistory(history.Compaction(stubCompactor{}, 0.75)),
	})
	if cfg.compactor == nil {
		t.Fatal("compactor not stored on config")
	}
	if cfg.compactThreshold != 0.75 {
		t.Errorf("threshold = %f, want 0.75", cfg.compactThreshold)
	}
}

func TestWithCompaction_OmittedLeavesNilCompactor(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithHistory(history.MaxHistory(10)),
	})
	if cfg.compactor != nil {
		t.Error("compactor should be nil when WithCompaction not used")
	}
}
