package guardrail

import (
	"context"
	"log/slog"

	"github.com/nevindra/oasis/core"
)

// CostGuard halts a run when its cumulative estimated spend crosses maxUSD.
// It prices the run's per-model token usage (read from the run context that
// the agent loop populates) against a pricing table. Deterministic and
// zero-cost; requires no observability storage.
//
// Pricing is injected explicitly (WithPricing) to keep the guardrail package
// free of the model-catalog import. Canonical usage:
//
//	guardrail.NewCostGuard(5.0, guardrail.WithPricing(catalog.PricingMap()))
//
// With no pricing the guard is inactive (logs once) — it never blocks blind.
type CostGuard struct {
	maxUSD   float64
	pricing  map[string]core.ModelPricing
	warnOnly bool
	response string
	logger   *slog.Logger
}

// CostOption configures a CostGuard.
type CostOption func(*CostGuard)

// NewCostGuard returns a guard that halts at maxUSD cumulative spend per run.
func NewCostGuard(maxUSD float64, opts ...CostOption) *CostGuard {
	g := &CostGuard{
		maxUSD:   maxUSD,
		response: "Spending limit reached.",
	}
	for _, opt := range opts {
		opt(g)
	}
	if g.logger == nil {
		g.logger = nopLogger
	}
	if len(g.pricing) == 0 && g.maxUSD > 0 {
		g.logger.Warn("cost guard inactive: no pricing configured (use WithPricing)")
	}
	return g
}

// WithPricing supplies the model→pricing table (e.g. catalog.PricingMap()).
func WithPricing(m map[string]core.ModelPricing) CostOption {
	return func(g *CostGuard) { g.pricing = m }
}

// WarnOnly logs instead of halting when the ceiling is crossed.
func WarnOnly() CostOption {
	return func(g *CostGuard) { g.warnOnly = true }
}

// CostResponse sets the canned response returned on halt.
func CostResponse(msg string) CostOption {
	return func(g *CostGuard) { g.response = msg }
}

// CostLogger sets the guard's logger.
func CostLogger(l *slog.Logger) CostOption {
	return func(g *CostGuard) { g.logger = l }
}

// PreLLM halts before a call if the run is already over budget (e.g. resumed).
func (g *CostGuard) PreLLM(ctx context.Context, _ *core.ChatRequest) error {
	return g.check(ctx)
}

// PostLLM halts after a call once cumulative spend crosses the ceiling.
func (g *CostGuard) PostLLM(ctx context.Context, _ *core.ChatResponse) error {
	return g.check(ctx)
}

func (g *CostGuard) check(ctx context.Context) error {
	if g.maxUSD <= 0 {
		return nil
	}
	if len(g.pricing) == 0 {
		return nil
	}
	usage, ok := core.RunUsageByModel(ctx)
	if !ok {
		return nil // outside the run loop: nothing to price
	}
	total := 0.0
	for model, u := range usage {
		total += g.cost(model, u)
	}
	if total < g.maxUSD {
		return nil
	}
	if g.warnOnly {
		g.logger.Warn("cost budget exceeded", "spent_usd", total, "max_usd", g.maxUSD)
		return nil
	}
	g.logger.Warn("cost budget exceeded — halting", "spent_usd", total, "max_usd", g.maxUSD)
	return &core.ErrHalt{Response: g.response}
}

// cost prices one model's usage. Unknown models cost 0 (fail open). Mirrors
// observer.CostCalculator: cached input tokens billed at the cache-read rate
// when the model exposes one.
func (g *CostGuard) cost(model string, u core.Usage) float64 {
	p, ok := g.pricing[model]
	if !ok {
		return 0
	}
	var input float64
	if u.CachedTokens > 0 && p.CacheReadPerMillion > 0 {
		nonCached := u.InputTokens - u.CachedTokens
		if nonCached < 0 {
			nonCached = 0
		}
		input = float64(nonCached)/1_000_000*p.InputPerMillion +
			float64(u.CachedTokens)/1_000_000*p.CacheReadPerMillion
	} else {
		input = float64(u.InputTokens) / 1_000_000 * p.InputPerMillion
	}
	return input + float64(u.OutputTokens)/1_000_000*p.OutputPerMillion
}
