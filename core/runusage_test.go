package core

import (
	"context"
	"testing"
)

func TestRunUsageAccumulatesPerModel(t *testing.T) {
	ctx := WithRunUsage(context.Background())
	AddRunUsage(ctx, "gpt-4o", Usage{InputTokens: 100, OutputTokens: 50})
	AddRunUsage(ctx, "gpt-4o", Usage{InputTokens: 10, OutputTokens: 5})
	AddRunUsage(ctx, "claude-opus-4", Usage{InputTokens: 1, OutputTokens: 2})

	m, ok := RunUsageByModel(ctx)
	if !ok {
		t.Fatal("expected accumulator present")
	}
	if m["gpt-4o"].InputTokens != 110 || m["gpt-4o"].OutputTokens != 55 {
		t.Errorf("gpt-4o = %+v", m["gpt-4o"])
	}
	if m["claude-opus-4"].InputTokens != 1 {
		t.Errorf("claude = %+v", m["claude-opus-4"])
	}
}

func TestRunUsageAbsent(t *testing.T) {
	if _, ok := RunUsageByModel(context.Background()); ok {
		t.Error("expected no accumulator on a bare context")
	}
	// AddRunUsage on a bare context must be a safe no-op.
	AddRunUsage(context.Background(), "x", Usage{InputTokens: 1})
}
