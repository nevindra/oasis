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

// ObservedProvider wraps an oasis.Provider with OTEL instrumentation.
type ObservedProvider struct {
	inner oasis.Provider
	inst  *Instruments
	model string
}

// WrapProvider returns an instrumented provider that emits traces, metrics, and logs.
func WrapProvider(inner oasis.Provider, model string, inst *Instruments) *ObservedProvider {
	return &ObservedProvider{inner: inner, inst: inst, model: model}
}

func (o *ObservedProvider) Name() string { return o.inner.Name() }

func (o *ObservedProvider) Chat(ctx context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
	spanAttrs := []trace.SpanStartOption{
		trace.WithAttributes(
			AttrLLMModel.String(o.model),
			AttrLLMProvider.String(o.inner.Name()),
		),
	}
	spanName := "llm.chat"
	method := "chat"
	if len(req.Tools) > 0 {
		toolNames := make([]string, len(req.Tools))
		for i, t := range req.Tools {
			toolNames[i] = t.Name
		}
		spanAttrs = append(spanAttrs, trace.WithAttributes(
			AttrToolCount.Int(len(req.Tools)),
			AttrToolNames.StringSlice(toolNames),
		))
		spanName = "llm.chat_with_tools"
		method = "chat_with_tools"
	}

	ctx, span := o.inst.Tracer.Start(ctx, spanName, spanAttrs...)
	defer span.End()
	start := time.Now()

	resp, err := o.inner.Chat(ctx, req)

	durationMs := float64(time.Since(start).Milliseconds())
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	o.record(ctx, span, method, status, durationMs, resp.Usage)
	return resp, err
}

func (o *ObservedProvider) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	ctx, span := o.inst.Tracer.Start(ctx, "llm.chat_stream", trace.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
	))
	defer span.End()
	start := time.Now()

	// Wrap channel to count chunks.
	// The goroutine forwards events from wrappedCh to the caller's ch.
	// We use select with ctx.Done to avoid hanging if the context is cancelled.
	// Buffer wrappedCh generously so the inner provider never blocks on send,
	// preventing a deadlock where the goroutine can't drain wrappedCh because
	// ch is full and nobody reads ch until ChatStream returns.
	bufSize := max(cap(ch), 64)
	wrappedCh := make(chan oasis.StreamEvent, bufSize)
	chunks := 0
	done := make(chan struct{})
	go func() {
		defer close(ch)
		defer close(done)
		for ev := range wrappedCh {
			chunks++
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	resp, err := o.inner.ChatStream(ctx, req, wrappedCh)
	<-done // wait for goroutine to finish before reading chunks

	durationMs := float64(time.Since(start).Milliseconds())
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	span.SetAttributes(AttrStreamChunks.Int(chunks))
	o.record(ctx, span, "chat_stream", status, durationMs, resp.Usage)
	return resp, err
}

func (o *ObservedProvider) record(ctx context.Context, span trace.Span, method, status string, durationMs float64, usage oasis.Usage) {
	cost := o.inst.Cost.Calculate(o.model, usage.InputTokens, usage.OutputTokens)

	attrs := metric.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
		AttrLLMMethod.String(method),
	)

	span.SetAttributes(
		AttrTokensInput.Int(usage.InputTokens),
		AttrTokensOutput.Int(usage.OutputTokens),
		AttrCostUSD.Float64(cost),
	)

	o.inst.TokenUsage.Add(ctx, int64(usage.InputTokens), metric.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
		attribute.String("direction", "input"),
	))
	o.inst.TokenUsage.Add(ctx, int64(usage.OutputTokens), metric.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
		attribute.String("direction", "output"),
	))
	o.inst.CostTotal.Add(ctx, cost, attrs)
	o.inst.LLMRequests.Add(ctx, 1, metric.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
		AttrLLMMethod.String(method),
		attribute.String("status", status),
	))
	o.inst.LLMDuration.Record(ctx, durationMs, attrs)

	// Structured log
	var rec oasislog.Record
	rec.SetSeverity(oasislog.SeverityInfo)
	rec.SetBody(oasislog.StringValue("llm call completed"))
	rec.AddAttributes(
		oasislog.String("llm.model", o.model),
		oasislog.String("llm.provider", o.inner.Name()),
		oasislog.String("llm.method", method),
		oasislog.Int("llm.tokens.input", usage.InputTokens),
		oasislog.Int("llm.tokens.output", usage.OutputTokens),
		oasislog.Float64("llm.cost_usd", cost),
		oasislog.Float64("llm.duration_ms", durationMs),
		oasislog.String("status", status),
	)
	o.inst.Logger.Emit(ctx, rec)
}
