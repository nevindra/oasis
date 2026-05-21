package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	"github.com/nevindra/oasis/core"
)

// LoggingMiddleware logs tool.start and tool.finish events with name, duration,
// result content length, and error (if any) at slog.LevelInfo. Use logger==nil
// to install a no-op logger.
func LoggingMiddleware(logger *slog.Logger) core.ToolMiddleware {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	return func(inner core.AnyTool) core.AnyTool {
		return &loggingWrapper{inner: inner, logger: logger}
	}
}

type loggingWrapper struct {
	inner  core.AnyTool
	logger *slog.Logger
}

func (l *loggingWrapper) Name() string                    { return l.inner.Name() }
func (l *loggingWrapper) Definition() core.ToolDefinition { return l.inner.Definition() }
func (l *loggingWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	start := time.Now()
	l.logger.Info("tool.start", "name", l.inner.Name(), "args_bytes", len(args))
	result, err := l.inner.ExecuteRaw(ctx, args)
	l.logger.Info("tool.finish",
		"name", l.inner.Name(),
		"duration", time.Since(start),
		"result_bytes", len(result.Content),
		"has_error", err != nil || result.Error != "",
	)
	return result, err
}

// TimingMiddleware adds a slog.Debug timing record. Mostly redundant with
// StepTrace; kept as a reference implementation users can copy.
func TimingMiddleware() core.ToolMiddleware {
	return func(inner core.AnyTool) core.AnyTool {
		return &timingWrapper{inner: inner}
	}
}

type timingWrapper struct{ inner core.AnyTool }

func (t *timingWrapper) Name() string                    { return t.inner.Name() }
func (t *timingWrapper) Definition() core.ToolDefinition { return t.inner.Definition() }
func (t *timingWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	start := time.Now()
	r, err := t.inner.ExecuteRaw(ctx, args)
	slog.Debug("tool.timing", "name", t.inner.Name(), "duration", time.Since(start))
	return r, err
}

// TransformMiddleware applies fn to the ToolResult before it is returned.
// fn receives the tool name and the result; the returned value replaces the
// original. Use this to mask sensitive fields, truncate large outputs, or
// inject computed metadata.
//
// fn is not called when the inner tool returned a Go error.
func TransformMiddleware(fn func(name string, r core.ToolResult) core.ToolResult) core.ToolMiddleware {
	return func(inner core.AnyTool) core.AnyTool {
		return &transformWrapper{inner: inner, fn: fn}
	}
}

type transformWrapper struct {
	inner core.AnyTool
	fn    func(string, core.ToolResult) core.ToolResult
}

func (w *transformWrapper) Name() string                    { return w.inner.Name() }
func (w *transformWrapper) Definition() core.ToolDefinition { return w.inner.Definition() }
func (w *transformWrapper) ExecuteRaw(ctx context.Context, a json.RawMessage) (core.ToolResult, error) {
	r, err := w.inner.ExecuteRaw(ctx, a)
	if err != nil {
		return r, err
	}
	return w.fn(w.inner.Name(), r), nil
}

// OTelSpanMiddleware emits a tracing span named "tool.execute" for each tool
// call, with attributes for tool name and arg byte length. Errors are recorded
// on the span. Pass the tracer the agent was built with.
//
// This middleware is automatically applied when the agent has a Tracer
// configured (via WithTracer) and the user did not already include an
// OTelSpanMiddleware in their WithToolMiddleware list. To opt out of
// auto-application, include a custom OTelSpanMiddleware (e.g. one with
// extra attributes) explicitly.
//
// When tracer is nil, returns a pass-through middleware.
func OTelSpanMiddleware(tracer Tracer) core.ToolMiddleware {
	if tracer == nil {
		return func(t core.AnyTool) core.AnyTool { return t }
	}
	return func(inner core.AnyTool) core.AnyTool {
		return &otelSpanWrapper{inner: inner, tracer: tracer}
	}
}

type otelSpanWrapper struct {
	inner  core.AnyTool
	tracer Tracer
}

func (w *otelSpanWrapper) Name() string                    { return w.inner.Name() }
func (w *otelSpanWrapper) Definition() core.ToolDefinition { return w.inner.Definition() }
func (w *otelSpanWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	ctx, span := w.tracer.Start(ctx, "tool.execute",
		StringAttr("tool.name", w.inner.Name()),
		IntAttr("tool.args_bytes", len(args)),
	)
	defer span.End()
	r, err := w.inner.ExecuteRaw(ctx, args)
	if err != nil {
		span.Error(err)
	}
	if r.Error != "" {
		span.SetAttr(StringAttr("tool.error", r.Error))
	}
	return r, err
}

// hasOTelSpanMiddleware reports whether the chain already includes an
// OTelSpanMiddleware. Used by the auto-wiring path in InitCore to avoid
// double-spanning.
//
// Detection works by applying each middleware to a sentinel AnyTool and
// checking whether the resulting wrapper is an *otelSpanWrapper. Function
// values cannot be compared for equality in Go, so type-tagging the wrapper
// is the only reliable approach.
func hasOTelSpanMiddleware(mws []core.ToolMiddleware) bool {
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

// otelDetectSentinel is a no-op core.AnyTool used only by hasOTelSpanMiddleware.
type otelDetectSentinel struct{}

func (*otelDetectSentinel) Name() string                    { return "" }
func (*otelDetectSentinel) Definition() core.ToolDefinition { return core.ToolDefinition{} }
func (*otelDetectSentinel) ExecuteRaw(context.Context, json.RawMessage) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}
