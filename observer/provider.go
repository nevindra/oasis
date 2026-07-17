package observer

import (
	"context"
	"time"

	oasis "github.com/nevindra/oasis/core"

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

// ObservedByOasis marks this provider as already instrumented so the agent
// loop skips its own duplicate llm.generate span (see agent.callLLM).
func (o *ObservedProvider) ObservedByOasis() {}

func (o *ObservedProvider) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	startAttrs := []attribute.KeyValue{
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
		AttrGenAIRequestModel.String(o.model),
		AttrGenAISystem.String(o.inner.Name()),
		AttrObservationType.String("generation"),
	}
	if gp := req.GenerationParams; gp != nil {
		if gp.Temperature != nil {
			startAttrs = append(startAttrs, attribute.Float64("gen_ai.request.temperature", *gp.Temperature))
		}
		if gp.TopP != nil {
			startAttrs = append(startAttrs, attribute.Float64("gen_ai.request.top_p", *gp.TopP))
		}
		if gp.MaxTokens != nil {
			startAttrs = append(startAttrs, attribute.Int("gen_ai.request.max_tokens", *gp.MaxTokens))
		}
	}
	if n := len(req.Tools); n > 0 {
		startAttrs = append(startAttrs,
			AttrToolCount.Int(n),
			// Which tools the model could call on THIS request — the advertised
			// set varies per call (selector filtering, mid-run expansion), so
			// record it as filterable observation metadata.
			attribute.String("langfuse.observation.metadata.advertised_tools", toolNamesList(req.Tools)),
		)
	}
	if oasis.TraceContentEnabled() {
		startAttrs = append(startAttrs, AttrObservationInput.String(ChatInputJSON(req)))
	}
	ctx, span := o.inst.Tracer.Start(ctx, "llm.generate", trace.WithAttributes(startAttrs...))
	defer span.End()
	start := time.Now()

	var resp oasis.ChatResponse
	var err error
	chunks := 0
	var firstChunk time.Time

	if ch != nil {
		bufSize := max(cap(ch), 64)
		wrappedCh := make(chan oasis.StreamEvent, bufSize)
		done := make(chan struct{})
		go func() {
			defer close(ch)
			defer close(done)
			for ev := range wrappedCh {
				if chunks == 0 {
					firstChunk = time.Now()
				}
				chunks++
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}()
		resp, err = o.inner.ChatStream(ctx, req, wrappedCh)
		<-done
	} else {
		resp, err = o.inner.ChatStream(ctx, req, nil)
	}

	durationMs := float64(time.Since(start).Milliseconds())
	status := "ok"
	if err != nil {
		status = "error"
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	span.SetAttributes(AttrStreamChunks.Int(chunks))
	if !firstChunk.IsZero() {
		span.SetAttributes(AttrCompletionStartTime.String(firstChunk.UTC().Format(time.RFC3339Nano)))
	}
	if err == nil && oasis.TraceContentEnabled() {
		span.SetAttributes(AttrObservationOutput.String(ChatOutputJSON(resp)))
	}
	if resp.FinishReason != "" {
		span.SetAttributes(attribute.String("gen_ai.response.finish_reasons", string(resp.FinishReason)))
	}
	o.record(ctx, span, "chat_stream", status, durationMs, resp.Usage)
	return resp, err
}

func (o *ObservedProvider) record(ctx context.Context, span trace.Span, method, status string, durationMs float64, usage oasis.Usage) {
	cost := o.inst.Cost.Calculate(o.model, usage.InputTokens, usage.OutputTokens, usage.CachedTokens)

	attrs := metric.WithAttributes(
		AttrLLMModel.String(o.model),
		AttrLLMProvider.String(o.inner.Name()),
		AttrLLMMethod.String(method),
	)

	span.SetAttributes(
		AttrTokensInput.Int(usage.InputTokens),
		AttrTokensOutput.Int(usage.OutputTokens),
		AttrTokensCached.Int(usage.CachedTokens),
		AttrCostUSD.Float64(cost),
		AttrGenAIInputTokens.Int(usage.InputTokens),
		AttrGenAIOutputTokens.Int(usage.OutputTokens),
	)
	if usage.CachedTokens > 0 {
		span.SetAttributes(AttrGenAICachedTokens.Int(usage.CachedTokens))
	}
	if cost > 0 {
		span.SetAttributes(AttrGenAICost.Float64(cost))
	}

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
		oasislog.Int("llm.tokens.cached", usage.CachedTokens),
		oasislog.Float64("llm.cost_usd", cost),
		oasislog.Float64("llm.duration_ms", durationMs),
		oasislog.String("status", status),
	)
	o.inst.Logger.Emit(ctx, rec)
}
