// Package ratelimit decorates an oasis.Provider with request-per-minute
// (RPM) and token-per-minute (TPM) budget enforcement.
//
// The decorator blocks the calling goroutine when budgets are exhausted,
// resuming as soon as the rolling-window budget frees up. Both RPM and
// TPM are tracked independently; either can block on its own.
//
// Basic usage:
//
//	provider := someProvider()
//	limited := ratelimit.WithRateLimit(provider,
//	    ratelimit.RPM(60),
//	    ratelimit.TPM(100_000),
//	)
//
// limited satisfies oasis.Provider and can be passed anywhere a Provider
// is expected. The decorator is safe for concurrent use.
package ratelimit
