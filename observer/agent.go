package observer

import (
	"context"
	"fmt"
	"time"

	oasis "github.com/nevindra/oasis"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oasislog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// ObservedAgent wraps any Agent to emit OTEL lifecycle spans, metrics, and logs.
// The wrapper creates a parent span for each Execute call that contains all inner
// operations (LLM calls, tool executions, etc.) as child spans via context propagation.
type ObservedAgent struct {
	inner oasis.Agent
	inst  *Instruments
}

// WrapAgent returns an instrumented Agent that emits lifecycle telemetry.
func WrapAgent(inner oasis.Agent, inst *Instruments) *ObservedAgent {
	return &ObservedAgent{inner: inner, inst: inst}
}

func (o *ObservedAgent) Name() string        { return o.inner.Name() }
func (o *ObservedAgent) Description() string { return o.inner.Description() }

// Execute wraps the inner agent's Execute, emitting an agent.execute span
// that serves as the parent for all inner operations.
func (o *ObservedAgent) Execute(ctx context.Context, task oasis.AgentTask) (oasis.AgentResult, error) {
	agentType := detectAgentType(o.inner)

	ctx, span := o.inst.Tracer.Start(ctx, "agent.execute", trace.WithAttributes(
		AttrAgentName.String(o.inner.Name()),
		AttrAgentType.String(agentType),
	))
	defer span.End()
	start := time.Now()

	span.AddEvent("agent.started")

	result, err := o.inner.Execute(ctx, task)

	durationMs := float64(time.Since(start).Milliseconds())
	status := "ok"

	if ctx.Err() != nil && err != nil {
		status = "cancelled"
		span.AddEvent("agent.cancelled")
		span.SetStatus(codes.Error, "cancelled")
	} else if err != nil {
		status = "error"
		span.AddEvent("agent.failed", trace.WithAttributes(
			attribute.String("error", err.Error()),
		))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.AddEvent("agent.completed")
	}

	span.SetAttributes(
		AttrAgentStatus.String(status),
		AttrTokensInput.Int(result.Usage.InputTokens),
		AttrTokensOutput.Int(result.Usage.OutputTokens),
	)

	// Metrics
	attrs := metric.WithAttributes(
		AttrAgentName.String(o.inner.Name()),
		attribute.String("status", status),
	)
	o.inst.AgentExecutions.Add(ctx, 1, attrs)
	o.inst.AgentDuration.Record(ctx, durationMs, metric.WithAttributes(
		AttrAgentName.String(o.inner.Name()),
	))

	// Structured log
	var rec oasislog.Record
	rec.SetSeverity(oasislog.SeverityInfo)
	rec.SetBody(oasislog.StringValue("agent execution completed"))
	rec.AddAttributes(
		oasislog.String("agent.name", o.inner.Name()),
		oasislog.String("agent.type", agentType),
		oasislog.String("agent.status", status),
		oasislog.Int("tokens.input", result.Usage.InputTokens),
		oasislog.Int("tokens.output", result.Usage.OutputTokens),
		oasislog.Float64("duration_ms", durationMs),
	)
	o.inst.Logger.Emit(ctx, rec)

	return result, err
}

// detectAgentType returns a string identifier for the agent's concrete type.
func detectAgentType(a oasis.Agent) string {
	switch a.(type) {
	case *oasis.LLMAgent:
		return "LLMAgent"
	case *oasis.Network:
		return "Network"
	case *oasis.Workflow:
		return "Workflow"
	default:
		return fmt.Sprintf("%T", a)
	}
}

// compile-time check
var _ oasis.Agent = (*ObservedAgent)(nil)
