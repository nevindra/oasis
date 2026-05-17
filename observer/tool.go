package observer

import (
	"context"
	"encoding/json"
	"time"

	oasis "github.com/nevindra/oasis"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oasislog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ObservedTool wraps an oasis.AnyTool with OTEL instrumentation.
type ObservedTool struct {
	inner oasis.AnyTool
	inst  *Instruments
}

// WrapTool returns an instrumented tool.
func WrapTool(inner oasis.AnyTool, inst *Instruments) *ObservedTool {
	return &ObservedTool{inner: inner, inst: inst}
}

func (o *ObservedTool) Name() string { return o.inner.Name() }

func (o *ObservedTool) Definition() oasis.ToolDefinition {
	return o.inner.Definition()
}

func (o *ObservedTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
	name := o.inner.Name()
	ctx, span := o.inst.Tracer.Start(ctx, "tool.execute", trace.WithAttributes(
		AttrToolName.String(name),
	))
	defer span.End()
	start := time.Now()

	result, err := o.inner.ExecuteRaw(ctx, args)

	durationMs := float64(time.Since(start).Milliseconds())
	status := "ok"
	if result.Error != "" {
		status = "tool_error"
	}
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	span.SetAttributes(
		AttrToolStatus.String(status),
		AttrToolResultLength.Int(len(result.Content)),
	)

	o.inst.ToolExecutions.Add(ctx, 1, metric.WithAttributes(
		AttrToolName.String(name),
		attribute.String("status", status),
	))
	o.inst.ToolDuration.Record(ctx, durationMs, metric.WithAttributes(
		AttrToolName.String(name),
	))

	// Structured log
	var rec oasislog.Record
	rec.SetSeverity(oasislog.SeverityInfo)
	rec.SetBody(oasislog.StringValue("tool executed"))
	rec.AddAttributes(
		oasislog.String("tool.name", name),
		oasislog.String("tool.status", status),
		oasislog.Int("tool.result_length", len(result.Content)),
		oasislog.Float64("tool.duration_ms", durationMs),
	)
	o.inst.Logger.Emit(ctx, rec)

	return result, err
}

// compile-time check
var _ oasis.AnyTool = (*ObservedTool)(nil)
