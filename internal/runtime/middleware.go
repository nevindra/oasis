package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nevindra/oasis/core"
)

// ---- Stream sink context ----

// streamSinkKey is the typed context key for the per-call StreamEvent sink.
type streamSinkKey struct{}

// ContextWithStreamSink stores ch on ctx so middleware that needs to emit
// stream events can recover it. Returns a derived context.
func ContextWithStreamSink(ctx context.Context, ch chan<- core.StreamEvent) context.Context {
	if ch == nil {
		return ctx
	}
	return context.WithValue(ctx, streamSinkKey{}, ch)
}

// StreamSinkFromContext returns the StreamEvent sink stored on ctx, or nil.
func StreamSinkFromContext(ctx context.Context) chan<- core.StreamEvent {
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

// ---- OTel span middleware ----

// OTelSpanMiddleware emits a tracing span named "tool.execute" for each tool
// call. When tracer is nil, returns a pass-through middleware.
//
// This middleware is automatically applied when the agent has a Tracer
// configured (via WithTracer) and the user did not already include one in
// their WithToolMiddleware list.
func OTelSpanMiddleware(tracer core.Tracer) core.ToolMiddleware {
	if tracer == nil {
		return func(t core.AnyTool) core.AnyTool { return t }
	}
	return func(inner core.AnyTool) core.AnyTool {
		return &otelSpanWrapper{inner: inner, tracer: tracer}
	}
}

type otelSpanWrapper struct {
	inner  core.AnyTool
	tracer core.Tracer
}

func (w *otelSpanWrapper) Name() string                    { return w.inner.Name() }
func (w *otelSpanWrapper) Definition() core.ToolDefinition { return w.inner.Definition() }
func (w *otelSpanWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	attrs := []core.SpanAttr{
		core.StringAttr("tool.name", w.inner.Name()),
		core.IntAttr("tool.args_bytes", len(args)),
		core.StringAttr("langfuse.observation.type", "tool"),
	}
	if core.TraceContentEnabled() {
		attrs = append(attrs, core.StringAttr("langfuse.observation.input", truncateForSpan(string(args))))
	}
	// Span named after the tool (a bounded set): observability backends
	// filter and target observations by name, and "tool.execute" for every
	// tool would make per-tool analysis impossible.
	ctx, span := w.tracer.Start(ctx, w.inner.Name(), attrs...)
	defer span.End()
	r, err := w.inner.ExecuteRaw(ctx, args)
	if err != nil {
		span.Error(err)
	}
	if r.Error != "" {
		span.SetAttr(core.StringAttr("tool.error", r.Error))
	} else if err == nil && core.TraceContentEnabled() {
		span.SetAttr(core.StringAttr("langfuse.observation.output", truncateForSpan(r.Content)))
	}
	return r, err
}

// toolSpanPayloadCap bounds tool arg/result payloads recorded on spans.
const toolSpanPayloadCap = 40_000

func truncateForSpan(s string) string {
	if len(s) <= toolSpanPayloadCap {
		return s
	}
	return s[:toolSpanPayloadCap] + "…(truncated)"
}

// HasOTelSpanMiddleware reports whether the chain already includes an
// OTelSpanMiddleware. Used by Init to avoid double-spanning.
func HasOTelSpanMiddleware(mws []core.ToolMiddleware) bool {
	sentinel := &otelDetectSentinel{}
	for _, mw := range mws {
		if mw == nil {
			continue
		}
		wrapped := mw(sentinel)
		if _, ok := wrapped.(*otelSpanWrapper); ok {
			return true
		}
	}
	return false
}

// otelDetectSentinel is a no-op core.AnyTool used only by HasOTelSpanMiddleware.
type otelDetectSentinel struct{}

func (*otelDetectSentinel) Name() string                    { return "" }
func (*otelDetectSentinel) Definition() core.ToolDefinition { return core.ToolDefinition{} }
func (*otelDetectSentinel) ExecuteRaw(context.Context, json.RawMessage) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}

// ---- Approval middleware ----

// ApprovalMiddleware returns a middleware that gates the named tool with an
// approval request.
func ApprovalMiddleware(cfg ApprovalConfig, ih InputHandler) core.ToolMiddleware {
	return func(inner core.AnyTool) core.AnyTool {
		if inner.Name() != cfg.ToolName {
			return inner
		}
		return &approvalWrapper{inner: inner, cfg: cfg, handler: ih}
	}
}

type approvalWrapper struct {
	inner   core.AnyTool
	cfg     ApprovalConfig
	handler InputHandler
}

func (a *approvalWrapper) Name() string                    { return a.inner.Name() }
func (a *approvalWrapper) Definition() core.ToolDefinition { return a.inner.Definition() }
func (a *approvalWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	if a.handler == nil {
		return core.ToolResult{}, fmt.Errorf("approval required but no InputHandler configured")
	}

	// Emit pending event on the stream if a sink is configured.
	if ch := StreamSinkFromContext(ctx); ch != nil {
		ev := core.StreamEvent{
			Type: core.EventToolApprovalPending,
			Name: a.inner.Name(),
			Args: args,
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return core.ToolResult{}, ctx.Err()
		}
	}

	call := core.ToolCall{Name: a.inner.Name(), Args: args}
	question := a.cfg.Prompt(call)

	resp, err := a.handler.RequestInput(ctx, InputRequest{
		Question: question,
		Options:  []string{"approve", "deny"},
		Metadata: map[string]string{
			"kind": "tool-approval",
			"tool": a.inner.Name(),
			"args": string(args),
		},
	})
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("approval request: %w", err)
	}

	switch resp.Value {
	case "approve":
		return a.inner.ExecuteRaw(ctx, args)
	case "deny":
		if a.cfg.OnDeny == DenyHalt {
			return core.ToolResult{}, &core.ErrHalt{Response: fmt.Sprintf("user denied call to %s", a.inner.Name())}
		}
		return core.ToolResult{Error: fmt.Sprintf("user denied call to %s", a.inner.Name())}, nil
	default:
		return core.ToolResult{Error: fmt.Sprintf("approval response %q not recognized; treating as deny", resp.Value)}, nil
	}
}
