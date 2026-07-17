package core

import "sync/atomic"

// traceContent controls whether tracing instrumentation (observer wrappers,
// tool span middleware) records prompt/completion/tool payloads as span
// attributes. Enabled by default; hosts handling sensitive data can disable
// it so spans carry only structural metadata (model, tokens, durations).
var traceContent atomic.Bool

func init() { traceContent.Store(true) }

// SetTraceContentCapture toggles recording of message/tool payloads on spans.
func SetTraceContentCapture(enabled bool) { traceContent.Store(enabled) }

// TraceContentEnabled reports whether span payload capture is on.
func TraceContentEnabled() bool { return traceContent.Load() }
