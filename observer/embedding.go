package observer

import (
	"context"
	"time"

	oasis "github.com/nevindra/oasis"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oasislog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ObservedEmbedding wraps an oasis.EmbeddingProvider with OTEL instrumentation.
type ObservedEmbedding struct {
	inner oasis.EmbeddingProvider
	inst  *Instruments
	model string
}

// WrapEmbedding returns an instrumented embedding provider.
func WrapEmbedding(inner oasis.EmbeddingProvider, model string, inst *Instruments) *ObservedEmbedding {
	return &ObservedEmbedding{inner: inner, inst: inst, model: model}
}

func (o *ObservedEmbedding) Name() string       { return o.inner.Name() }
func (o *ObservedEmbedding) Dimensions() int     { return o.inner.Dimensions() }

func (o *ObservedEmbedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, span := o.inst.Tracer.Start(ctx, "llm.embed", trace.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
		AttrEmbedTextCount.Int(len(texts)),
		AttrEmbedDimensions.Int(o.inner.Dimensions()),
	))
	defer span.End()
	start := time.Now()

	result, err := o.inner.Embed(ctx, texts)

	durationMs := float64(time.Since(start).Milliseconds())
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	attrs := metric.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
	)

	o.inst.EmbedRequests.Add(ctx, 1, metric.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
		attribute.String("status", status),
	))
	o.inst.EmbedDuration.Record(ctx, durationMs, attrs)

	// Structured log
	var rec oasislog.Record
	rec.SetSeverity(oasislog.SeverityInfo)
	rec.SetBody(oasislog.StringValue("embedding completed"))
	rec.AddAttributes(
		oasislog.String("llm.model", o.model),
		oasislog.String("llm.provider", o.inner.Name()),
		oasislog.Int("llm.embed.text_count", len(texts)),
		oasislog.Float64("llm.duration_ms", durationMs),
		oasislog.String("status", status),
	)
	o.inst.Logger.Emit(ctx, rec)

	return result, err
}
