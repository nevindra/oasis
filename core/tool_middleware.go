package core

// ToolMiddleware wraps an AnyTool with additional behavior — logging,
// timing, tracing, transformation, approval, etc. Middleware composes by
// function application: the innermost middleware sees the unwrapped tool;
// the outermost wraps every layer below.
//
// A middleware that returns t unchanged is a no-op. Returning a different
// AnyTool with the same Name() is the intended pattern. Returning nil panics
// at registration time.
//
// Implementations should preserve the StreamingAnyTool interface when the
// wrapped tool implements it. The convention is:
//
//	func MyMiddleware(t AnyTool) AnyTool {
//	    if st, ok := t.(StreamingAnyTool); ok {
//	        return &myStreamingWrapper{inner: st}
//	    }
//	    return &myWrapper{inner: t}
//	}
type ToolMiddleware func(AnyTool) AnyTool

// ApplyToolMiddleware applies a chain of middlewares to t. The first
// middleware in mws is innermost (closest to t); the last is outermost.
// Returns t unchanged if mws is empty.
//
// Order rationale: matches net/http middleware composition. nil entries
// in mws are skipped. A middleware that returns nil panics — that is a
// programming error.
func ApplyToolMiddleware(t AnyTool, mws []ToolMiddleware) AnyTool {
	for _, mw := range mws {
		if mw == nil {
			continue
		}
		t = mw(t)
		if t == nil {
			panic("oasis: ToolMiddleware returned nil")
		}
	}
	return t
}
