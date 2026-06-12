package core

import "context"

// Tracer creates spans for tracing agent, workflow, retrieval, and ingest operations.
// The observer package provides an OTEL-backed implementation via NewTracer().
// When no Tracer is configured, span creation is skipped (nil check).
type Tracer interface {
	// Start creates a new span with the given name and optional attributes.
	// Returns a child context carrying the span and the span itself.
	// Callers must call Span.End() when the operation completes.
	Start(ctx context.Context, name string, attrs ...SpanAttr) (context.Context, Span)
}

// Span represents a traced operation. Callers must call End() when the
// operation completes to flush the span to the configured exporter.
type Span interface {
	// SetAttr adds attributes to the span after creation.
	SetAttr(attrs ...SpanAttr)
	// Event records a named event (annotation) on the span timeline.
	Event(name string, attrs ...SpanAttr)
	// Error records an error on the span and marks it as failed.
	Error(err error)
	// End completes the span. Must be called exactly once.
	End()
}

// SpanAttr is a key-value attribute attached to a span or event.
// Construction must go through the typed constructors (StringAttr, IntAttr,
// BoolAttr, Float64Attr) — direct struct literals are not supported and the
// Value field is intentionally unexported to enforce this.
type SpanAttr struct {
	Key   string
	value any
}

// Val returns the attribute value. The dynamic type is always one of
// string, int, float64, or bool — enforced by the constructors.
//
// Why: observer is a separate Go module and cannot access unexported fields
// across module boundaries, so the accessor must be exported. Returning any
// from an accessor whose godoc documents the closed type set is the accepted
// shape here — the construction boundary (not the accessor type) is what
// enforces type safety.
func (a SpanAttr) Val() any { return a.value }

// StringAttr creates a string-typed span attribute.
func StringAttr(k, v string) SpanAttr {
	return SpanAttr{Key: k, value: v}
}

// IntAttr creates an int-typed span attribute.
func IntAttr(k string, v int) SpanAttr {
	return SpanAttr{Key: k, value: v}
}

// BoolAttr creates a bool-typed span attribute.
func BoolAttr(k string, v bool) SpanAttr {
	return SpanAttr{Key: k, value: v}
}

// Float64Attr creates a float64-typed span attribute.
func Float64Attr(k string, v float64) SpanAttr {
	return SpanAttr{Key: k, value: v}
}
