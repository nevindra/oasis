package core

import (
	"context"
	"sync"
)

type runUsageKeyType struct{}

var runUsageKey runUsageKeyType

// runUsage is a run-scoped, per-model usage accumulator. Concurrency-guarded
// because the loop goroutine writes while processors read.
type runUsage struct {
	mu    sync.Mutex
	byMod map[string]Usage
}

// WithRunUsage returns a context carrying a fresh per-model usage accumulator.
// The agent run loop calls this once at the start of each run so that per-run
// cost/usage state never leaks across runs that share a processor instance.
func WithRunUsage(ctx context.Context) context.Context {
	return context.WithValue(ctx, runUsageKey, &runUsage{byMod: make(map[string]Usage)})
}

// AddRunUsage adds one call's usage under model. No-op if ctx has no
// accumulator (e.g. a processor invoked outside the run loop).
func AddRunUsage(ctx context.Context, model string, u Usage) {
	ru, _ := ctx.Value(runUsageKey).(*runUsage)
	if ru == nil {
		return
	}
	ru.mu.Lock()
	cur := ru.byMod[model]
	cur.InputTokens += u.InputTokens
	cur.OutputTokens += u.OutputTokens
	cur.CachedTokens += u.CachedTokens
	cur.CacheCreationTokens += u.CacheCreationTokens
	ru.byMod[model] = cur
	ru.mu.Unlock()
}

// RunUsageByModel returns a copy of the run's cumulative per-model usage, and
// false if no accumulator is present on ctx.
func RunUsageByModel(ctx context.Context) (map[string]Usage, bool) {
	ru, _ := ctx.Value(runUsageKey).(*runUsage)
	if ru == nil {
		return nil, false
	}
	ru.mu.Lock()
	out := make(map[string]Usage, len(ru.byMod))
	for k, v := range ru.byMod {
		out[k] = v
	}
	ru.mu.Unlock()
	return out, true
}
