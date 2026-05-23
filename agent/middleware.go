package agent

import "github.com/nevindra/oasis/core"

// Middleware wraps an Agent with extra behavior (logging, retry, tracing).
// Same shape as provider.Middleware and core.ToolMiddleware — compose with Chain.
//
// Thread-safety: the Middleware func itself must be safe to call from multiple
// goroutines. The returned Agent inherits the thread-safety guarantees of the
// inner Agent.
type Middleware func(core.Agent) core.Agent

// Chain composes Agent middlewares into one. Earlier arguments wrap further out.
// nil entries are silently skipped. Calling Chain with no arguments returns an
// identity middleware that leaves the agent unchanged.
//
//	a := agent.Chain(
//	    loggingMiddleware,
//	    tracingMiddleware,
//	)(base)
//
// Why: outer-to-inner ordering (apply in reverse) means the first argument in
// the Chain call is the first to execute at call time — matching the intuitive
// reading "logging wraps tracing wraps base".
func Chain(mws ...Middleware) Middleware {
	return func(a core.Agent) core.Agent {
		for i := len(mws) - 1; i >= 0; i-- {
			if mws[i] != nil {
				a = mws[i](a)
			}
		}
		return a
	}
}
