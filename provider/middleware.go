// Package provider hosts shared provider helpers and the middleware shape
// for wrapping core.Provider with extra behavior.
package provider

import "github.com/nevindra/oasis/core"

// Middleware wraps a Provider with extra behavior (retry, rate-limit,
// logging, tracing). Implementations should call the inner provider's
// methods and may add behavior before, after, or around the calls.
//
// Compose middlewares with Chain. The order of arguments is the order of
// application: Chain(a, b)(p) gives a(b(p)). So a runs outermost and
// sees b's behavior layered on top of p.
//
// Thread-safety: the Middleware func itself must be safe to call from
// multiple goroutines. The returned Provider inherits the thread-safety
// guarantees of the inner Provider.
type Middleware func(core.Provider) core.Provider

// Chain composes middlewares into one. Earlier arguments wrap further out.
// nil entries are silently skipped. Calling Chain with no arguments returns
// an identity middleware that leaves the provider unchanged.
//
//	p := provider.Chain(
//	    agent.RetryMiddleware(...),
//	    ratelimit.RateLimitMiddleware(...),
//	)(baseProvider)
//
// Why: outer-to-inner ordering (apply in reverse) means the first argument
// in the Chain call is the first to execute at call time — matching the
// intuitive reading "retry wraps ratelimit wraps base".
func Chain(mws ...Middleware) Middleware {
	return func(p core.Provider) core.Provider {
		for i := len(mws) - 1; i >= 0; i-- {
			if mws[i] != nil {
				p = mws[i](p)
			}
		}
		return p
	}
}
