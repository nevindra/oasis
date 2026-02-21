package observer

import (
	"context"
	"fmt"

	oasis "github.com/nevindra/oasis"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// otelTracer implements oasis.Tracer using OpenTelemetry.
type otelTracer struct {
	inner trace.Tracer
}

// NewTracer returns an oasis.Tracer backed by the global OTEL TracerProvider.
// Call observer.Init() first to configure the provider; otherwise spans go to
// a no-op backend.
func NewTracer() oasis.Tracer {
	return &otelTracer{inner: otel.Tracer(scopeName)}
}

func (t *otelTracer) Start(ctx context.Context, name string, attrs ...oasis.SpanAttr) (context.Context, oasis.Span) {
	otelAttrs := make([]attribute.KeyValue, len(attrs))
	for i, a := range attrs {
		otelAttrs[i] = toOTELAttr(a)
	}
	ctx, span := t.inner.Start(ctx, name, trace.WithAttributes(otelAttrs...))
	return ctx, &otelSpan{inner: span}
}

// otelSpan implements oasis.Span using an OTEL trace.Span.
type otelSpan struct {
	inner trace.Span
}

func (s *otelSpan) SetAttr(attrs ...oasis.SpanAttr) {
	otelAttrs := make([]attribute.KeyValue, len(attrs))
	for i, a := range attrs {
		otelAttrs[i] = toOTELAttr(a)
	}
	s.inner.SetAttributes(otelAttrs...)
}

func (s *otelSpan) Event(name string, attrs ...oasis.SpanAttr) {
	otelAttrs := make([]attribute.KeyValue, len(attrs))
	for i, a := range attrs {
		otelAttrs[i] = toOTELAttr(a)
	}
	s.inner.AddEvent(name, trace.WithAttributes(otelAttrs...))
}

func (s *otelSpan) Error(err error) {
	s.inner.RecordError(err)
	s.inner.SetStatus(codes.Error, err.Error())
}

func (s *otelSpan) End() {
	s.inner.End()
}

// toOTELAttr converts an oasis.SpanAttr to an OTEL attribute.KeyValue.
func toOTELAttr(a oasis.SpanAttr) attribute.KeyValue {
	switch v := a.Value.(type) {
	case string:
		return attribute.String(a.Key, v)
	case int:
		return attribute.Int(a.Key, v)
	case int64:
		return attribute.Int64(a.Key, v)
	case float64:
		return attribute.Float64(a.Key, v)
	case bool:
		return attribute.Bool(a.Key, v)
	default:
		return attribute.String(a.Key, fmt.Sprintf("%v", v))
	}
}

// compile-time checks
var (
	_ oasis.Tracer = (*otelTracer)(nil)
	_ oasis.Span   = (*otelSpan)(nil)
)
