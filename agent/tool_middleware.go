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
