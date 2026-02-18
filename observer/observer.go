// Package observer provides OTEL-based observability for Oasis LLM operations.
//
// It wraps Provider, EmbeddingProvider, and ToolRegistry with instrumented
// versions that emit traces, metrics, and logs via OpenTelemetry. Users export
// to any OTEL-compatible backend by setting standard OTEL env vars.
package observer

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	oasislog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const scopeName = "github.com/nevindra/oasis/observer"

// Instruments holds all OTEL instruments used by the observer wrappers.
type Instruments struct {
	Tracer trace.Tracer
	Meter  metric.Meter
	Logger oasislog.Logger

	// Counters
	TokenUsage     metric.Int64Counter
	CostTotal      metric.Float64Counter
	LLMRequests    metric.Int64Counter
	ToolExecutions metric.Int64Counter
	EmbedRequests  metric.Int64Counter

	// Histograms
	LLMDuration   metric.Float64Histogram
	ToolDuration  metric.Float64Histogram
	EmbedDuration metric.Float64Histogram

	// Agent-level
	AgentExecutions metric.Int64Counter
	AgentDuration   metric.Float64Histogram

	Cost *CostCalculator
}

// Init sets up OTEL trace, metric, and log providers with OTLP HTTP exporters.
// Configuration comes from standard OTEL env vars (OTEL_EXPORTER_OTLP_ENDPOINT, etc.).
// Returns a shutdown function that must be called on application exit.
func Init(ctx context.Context, pricing map[string]ModelPricing) (*Instruments, func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("oasis")),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, nil, err
	}

	// Trace provider
	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// Metric provider
	metricExp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// Log provider
	logExp, err := otlploghttp.New(ctx)
	if err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil, nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	global.SetLoggerProvider(lp)

	inst, err := newInstruments(pricing)
	if err != nil {
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)
		return nil, nil, err
	}

	shutdown := func(ctx context.Context) error {
		return errors.Join(
			tp.Shutdown(ctx),
			mp.Shutdown(ctx),
			lp.Shutdown(ctx),
		)
	}

	return inst, shutdown, nil
}

func newInstruments(pricing map[string]ModelPricing) (*Instruments, error) {
	tracer := otel.Tracer(scopeName)
	meter := otel.Meter(scopeName)
	logger := global.GetLoggerProvider().Logger(scopeName)

	tokenUsage, err := meter.Int64Counter("llm.token.usage",
		metric.WithDescription("Total tokens consumed"),
		metric.WithUnit("{token}"))
	if err != nil {
		return nil, err
	}

	costTotal, err := meter.Float64Counter("llm.cost.total",
		metric.WithDescription("Cumulative LLM cost in USD"),
		metric.WithUnit("USD"))
	if err != nil {
		return nil, err
	}

	llmRequests, err := meter.Int64Counter("llm.requests",
		metric.WithDescription("LLM request count"),
		metric.WithUnit("{request}"))
	if err != nil {
		return nil, err
	}

	toolExecutions, err := meter.Int64Counter("tool.executions",
		metric.WithDescription("Tool execution count"),
		metric.WithUnit("{execution}"))
	if err != nil {
		return nil, err
	}

	embedRequests, err := meter.Int64Counter("embedding.requests",
		metric.WithDescription("Embedding request count"),
		metric.WithUnit("{request}"))
	if err != nil {
		return nil, err
	}

	llmDuration, err := meter.Float64Histogram("llm.duration",
		metric.WithDescription("LLM call duration"),
		metric.WithUnit("ms"))
	if err != nil {
		return nil, err
	}

	toolDuration, err := meter.Float64Histogram("tool.duration",
		metric.WithDescription("Tool execution duration"),
		metric.WithUnit("ms"))
	if err != nil {
		return nil, err
	}

	embedDuration, err := meter.Float64Histogram("embedding.duration",
		metric.WithDescription("Embedding call duration"),
		metric.WithUnit("ms"))
	if err != nil {
		return nil, err
	}

	agentExecutions, err := meter.Int64Counter("agent.executions",
		metric.WithDescription("Agent execution count"),
		metric.WithUnit("{execution}"))
	if err != nil {
		return nil, err
	}

	agentDuration, err := meter.Float64Histogram("agent.duration",
		metric.WithDescription("Agent execution duration"),
		metric.WithUnit("ms"))
	if err != nil {
		return nil, err
	}

	return &Instruments{
		Tracer:         tracer,
		Meter:          meter,
		Logger:         logger,
		TokenUsage:     tokenUsage,
		CostTotal:      costTotal,
		LLMRequests:    llmRequests,
		ToolExecutions: toolExecutions,
		EmbedRequests:  embedRequests,
		LLMDuration:    llmDuration,
		ToolDuration:   toolDuration,
		EmbedDuration:   embedDuration,
		AgentExecutions: agentExecutions,
		AgentDuration:   agentDuration,
		Cost:            NewCostCalculator(pricing),
	}, nil
}
