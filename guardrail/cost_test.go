package guardrail

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

func pricing() map[string]core.ModelPricing {
	return map[string]core.ModelPricing{
		"gpt-4o": {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	}
}

func TestCostGuardHaltsWhenOverBudget(t *testing.T) {
	g := NewCostGuard(0.01, WithPricing(pricing())) // $0.01 ceiling
	ctx := core.WithRunUsage(context.Background())
	// 2000 in + 2000 out @ gpt-4o = 0.005 + 0.020 = $0.025 > $0.01
	core.AddRunUsage(ctx, "gpt-4o", core.Usage{InputTokens: 2000, OutputTokens: 2000})

	err := g.PostLLM(ctx, &core.ChatResponse{})
	if _, ok := err.(*core.ErrHalt); !ok {
		t.Errorf("expected *core.ErrHalt over budget, got %v", err)
	}
}

func TestCostGuardUnderBudgetOK(t *testing.T) {
	g := NewCostGuard(1.00, WithPricing(pricing()))
	ctx := core.WithRunUsage(context.Background())
	core.AddRunUsage(ctx, "gpt-4o", core.Usage{InputTokens: 100, OutputTokens: 50})
	if err := g.PostLLM(ctx, &core.ChatResponse{}); err != nil {
		t.Errorf("expected nil under budget, got %v", err)
	}
}

func TestCostGuardUnknownModelFailsOpen(t *testing.T) {
	g := NewCostGuard(0.0001, WithPricing(pricing()))
	ctx := core.WithRunUsage(context.Background())
	core.AddRunUsage(ctx, "mystery-model", core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if err := g.PostLLM(ctx, &core.ChatResponse{}); err != nil {
		t.Errorf("unknown model must fail open (cost 0), got %v", err)
	}
}

func TestCostGuardNoPricingInactive(t *testing.T) {
	g := NewCostGuard(0.0001) // no pricing supplied
	ctx := core.WithRunUsage(context.Background())
	core.AddRunUsage(ctx, "gpt-4o", core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if err := g.PostLLM(ctx, &core.ChatResponse{}); err != nil {
		t.Errorf("guard with no pricing must be inactive, got %v", err)
	}
}
