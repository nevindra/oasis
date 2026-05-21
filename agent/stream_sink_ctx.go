package agent

import (
	"context"

	"github.com/nevindra/oasis/core"
)

// streamSinkKey is the typed context key for the per-call StreamEvent sink.
// Stored on a derived context.Context so middleware that needs to emit
// stream events (e.g. tool approval) can recover the channel without
// threading it through every interface.
type streamSinkKey struct{}

// contextWithStreamSink stores ch on ctx so middleware that needs to emit
// stream events can recover it. Returns a derived context.
//
// The agent dispatch path is responsible for calling this before invoking
// dispatch when a stream channel is configured (non-nil). Callers may pass
// a nil channel to clear an inherited sink — the helper checks for nil
// before storing.
func contextWithStreamSink(ctx context.Context, ch chan<- core.StreamEvent) context.Context {
	if ch == nil {
		return ctx
	}
	return context.WithValue(ctx, streamSinkKey{}, ch)
}

// streamSinkFromContext returns the StreamEvent sink stored on ctx by
// contextWithStreamSink, or nil if none is set.
func streamSinkFromContext(ctx context.Context) chan<- core.StreamEvent {
	v := ctx.Value(streamSinkKey{})
	if v == nil {
		return nil
	}
	ch, ok := v.(chan<- core.StreamEvent)
	if !ok {
		return nil
	}
	return ch
}
