package agent

import (
	"context"

	"github.com/nevindra/oasis/internal/runtime"
	"github.com/nevindra/oasis/core"
)

// contextWithStreamSink stores ch on ctx so middleware that needs to emit
// stream events can recover it. Returns a derived context.
//
// The agent dispatch path is responsible for calling this before invoking
// dispatch when a stream channel is configured (non-nil). Callers may pass
// a nil channel to clear an inherited sink — the helper checks for nil
// before storing.
//
// Delegates to runtime.ContextWithStreamSink so that all middleware in the
// runtime package can retrieve the sink via the same context key.
func contextWithStreamSink(ctx context.Context, ch chan<- core.StreamEvent) context.Context {
	return runtime.ContextWithStreamSink(ctx, ch)
}

// streamSinkFromContext returns the StreamEvent sink stored on ctx by
// contextWithStreamSink, or nil if none is set.
//
// Delegates to runtime.StreamSinkFromContext so that the same key is used
// everywhere.
func streamSinkFromContext(ctx context.Context) chan<- core.StreamEvent {
	return runtime.StreamSinkFromContext(ctx)
}
