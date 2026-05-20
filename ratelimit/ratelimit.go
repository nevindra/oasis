package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
)

// rateLimitProvider wraps a Provider with proactive rate limiting.
// Requests are blocked until the rate budget allows them to proceed.
type rateLimitProvider struct {
	inner core.Provider
	mu    sync.Mutex

	// RPM state: sliding window of request timestamps.
	rpm       int
	rpmWindow []time.Time

	// TPM state: sliding window of (timestamp, tokenCount) pairs.
	tpm       int
	tpmWindow []tpmEntry
}

type tpmEntry struct {
	at     time.Time
	tokens int
}

// RateLimitOption configures a rateLimitProvider.
type RateLimitOption func(*rateLimitProvider)

// RPM sets the maximum requests per minute.
func RPM(n int) RateLimitOption {
	return func(r *rateLimitProvider) { r.rpm = n }
}

// TPM sets the maximum tokens per minute (input + output combined).
// Token counts are recorded from ChatResponse.Usage after each request.
// This is a soft limit — the request that exceeds the budget completes,
// but subsequent requests block until the window slides.
func TPM(n int) RateLimitOption {
	return func(r *rateLimitProvider) { r.tpm = n }
}

// WithRateLimit wraps p with proactive rate limiting. Compose with other wrappers:
//
//	chatLLM = ratelimit.WithRateLimit(provider, ratelimit.RPM(60))
//	chatLLM = ratelimit.WithRateLimit(provider, ratelimit.RPM(60), ratelimit.TPM(100000))
//	chatLLM = ratelimit.WithRateLimit(oasisRetry.WithRetry(provider), ratelimit.RPM(60))
func WithRateLimit(p core.Provider, opts ...RateLimitOption) core.Provider {
	r := &rateLimitProvider{inner: p}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *rateLimitProvider) Name() string { return r.inner.Name() }

func (r *rateLimitProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	if err := r.waitForBudget(ctx); err != nil {
		close(ch)
		return core.ChatResponse{}, err
	}
	resp, err := r.inner.ChatStream(ctx, req, ch)
	if err == nil {
		r.recordUsage(resp.Usage)
	}
	return resp, err
}

// waitForBudget blocks until both RPM and TPM budgets allow a request.
// Returns ctx.Err() if the context is cancelled while waiting.
func (r *rateLimitProvider) waitForBudget(ctx context.Context) error {
	for {
		r.mu.Lock()
		now := time.Now()
		cutoff := now.Add(-time.Minute)

		// Prune expired RPM entries.
		r.rpmWindow = pruneTime(r.rpmWindow, cutoff)

		// Prune expired TPM entries.
		r.tpmWindow = pruneTpm(r.tpmWindow, cutoff)

		// Check RPM.
		rpmOK := r.rpm <= 0 || len(r.rpmWindow) < r.rpm

		// Check TPM.
		tpmOK := true
		if r.tpm > 0 {
			var total int
			for _, e := range r.tpmWindow {
				total += e.tokens
			}
			tpmOK = total < r.tpm
		}

		if rpmOK && tpmOK {
			// Record this request in RPM window.
			if r.rpm > 0 {
				r.rpmWindow = append(r.rpmWindow, now)
			}
			r.mu.Unlock()
			return nil
		}

		// Calculate wait time: time until the oldest entry in the blocking window expires.
		var wait time.Duration
		if !rpmOK && len(r.rpmWindow) > 0 {
			wait = r.rpmWindow[0].Add(time.Minute).Sub(now)
		}
		if !tpmOK && len(r.tpmWindow) > 0 {
			w := r.tpmWindow[0].at.Add(time.Minute).Sub(now)
			if wait == 0 || w < wait {
				wait = w
			}
		}
		if wait <= 0 {
			wait = 10 * time.Millisecond
		}
		r.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// recordUsage adds token counts to the TPM sliding window.
func (r *rateLimitProvider) recordUsage(u core.Usage) {
	if r.tpm <= 0 {
		return
	}
	total := u.InputTokens + u.OutputTokens
	if total <= 0 {
		return
	}
	r.mu.Lock()
	r.tpmWindow = append(r.tpmWindow, tpmEntry{at: time.Now(), tokens: total})
	r.mu.Unlock()
}

// pruneTime removes entries older than cutoff from a sorted time slice.
func pruneTime(s []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(s) && s[i].Before(cutoff) {
		i++
	}
	return s[i:]
}

// pruneTpm removes entries older than cutoff from a sorted tpmEntry slice.
func pruneTpm(s []tpmEntry, cutoff time.Time) []tpmEntry {
	i := 0
	for i < len(s) && s[i].at.Before(cutoff) {
		i++
	}
	return s[i:]
}

// compile-time check
var _ core.Provider = (*rateLimitProvider)(nil)
