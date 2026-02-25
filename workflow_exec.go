package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"sync"
	"time"
)

// executionState tracks the runtime state of a workflow execution.
type executionState struct {
	wCtx           *WorkflowContext
	results        map[string]StepResult
	failedStep     string          // name of first step that failed, empty if none
	failureSkipped map[string]bool // steps skipped due to upstream failure (not When() condition)
	suspendedStep  string          // name of step that suspended
	suspendPayload json.RawMessage // payload from the suspended step
	mu             sync.RWMutex    // protects results, failedStep, failureSkipped
	cancel         context.CancelFunc
}

func (s *executionState) setResult(name string, sr StepResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[name] = sr
}

func (s *executionState) getResult(name string) (StepResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.results[name]
	return r, ok
}

// Execute runs the workflow's step graph. Steps with satisfied dependencies
// are launched concurrently. The first step failure cancels all in-flight steps
// and marks downstream steps as StepSkipped.
// Returns an AgentResult with the last successful step's output.
func (w *Workflow) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return w.execute(ctx, task, nil)
}

// ExecuteStream runs the workflow like Execute, but emits StreamEvent values
// into ch throughout execution. Step start/finish events are emitted for each
// step. When an AgentStep delegates to a StreamingAgent, that agent's events
// are forwarded through ch. The channel is closed when streaming completes.
func (w *Workflow) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	defer close(ch)
	return w.execute(ctx, task, ch)
}

// execute is the shared implementation for Execute and ExecuteStream.
// When ch is non-nil, step-start/step-finish events are emitted.
func (w *Workflow) execute(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var span Span
	if w.tracer != nil {
		ctx, span = w.tracer.Start(ctx, "workflow.execute",
			StringAttr("workflow.name", w.name),
			IntAttr("step_count", len(w.stepOrder)))
		defer span.End()
	}

	state := &executionState{
		wCtx:           newWorkflowContext(task),
		results:        make(map[string]StepResult),
		failureSkipped: make(map[string]bool),
		cancel:         cancel,
	}

	w.runDAG(ctx, state, ch)

	result, err := w.buildResult(state, task, ch)
	if span != nil {
		if err != nil {
			var suspended *ErrSuspended
			if errors.As(err, &suspended) {
				span.SetAttr(StringAttr("workflow.status", "suspended"))
			} else {
				span.Error(err)
				span.SetAttr(StringAttr("workflow.status", "error"))
			}
		} else {
			span.SetAttr(StringAttr("workflow.status", "ok"))
		}
	}
	return result, err
}

// executeResume continues a suspended workflow from the given step.
// Only steps that completed successfully (or were skipped by a When() condition)
// are pre-populated — steps that were skipped due to the suspension (failure-skipped)
// will re-execute on resume. This is intentional: those steps never ran, so they
// must run once the suspended step succeeds.
func (w *Workflow) executeResume(ctx context.Context, task AgentTask, completedResults map[string]StepResult, contextValues map[string]any, data json.RawMessage, ch chan<- StreamEvent) (AgentResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Reconstruct workflow context from snapshot.
	wCtx := newWorkflowContext(task)
	for k, v := range contextValues {
		wCtx.Set(k, v)
	}
	// Inject resume data for the suspended step.
	wCtx.Set(resumeDataKey, data)

	state := &executionState{
		wCtx:           wCtx,
		results:        make(map[string]StepResult),
		failureSkipped: make(map[string]bool),
		cancel:         cancel,
	}

	// Pre-populate completed steps so they don't re-execute.
	maps.Copy(state.results, completedResults)

	// Re-run the DAG — completed steps are skipped, suspended step re-executes.
	w.runDAG(ctx, state, ch)

	// Clear resume data after execution to avoid retaining the payload.
	wCtx.mu.Lock()
	delete(wCtx.values, resumeDataKey)
	wCtx.mu.Unlock()

	return w.buildResult(state, task, ch)
}

// buildResult converts execution state into an AgentResult after the DAG completes.
// Handles suspension (returns ErrSuspended), failure (returns WorkflowError),
// and success. Shared by Execute and executeResume.
func (w *Workflow) buildResult(state *executionState, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	// Check for suspension.
	if state.suspendedStep != "" {
		snapshotResults := make(map[string]StepResult)
		for k, v := range state.results {
			if v.Status == StepSuccess || (v.Status == StepSkipped && !state.failureSkipped[k]) {
				snapshotResults[k] = v
			}
		}
		snapshotValues := make(map[string]any, len(state.wCtx.values))
		state.wCtx.mu.RLock()
		maps.Copy(snapshotValues, state.wCtx.values)
		state.wCtx.mu.RUnlock()

		suspendedStep := state.suspendedStep
		suspendPayload := state.suspendPayload

		return AgentResult{}, &ErrSuspended{
			Step:    suspendedStep,
			Payload: suspendPayload,
			resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
				return w.executeResume(ctx, task, snapshotResults, snapshotValues, data, nil)
			},
			resumeStream: func(ctx context.Context, data json.RawMessage, ch chan<- StreamEvent) (AgentResult, error) {
				defer close(ch)
				return w.executeResume(ctx, task, snapshotResults, snapshotValues, data, ch)
			},
		}
	}

	// Build WorkflowResult.
	var totalUsage Usage
	if u, ok := state.wCtx.Get(usageKey); ok {
		if usage, ok := u.(Usage); ok {
			totalUsage = usage
		}
	}

	wfStatus := StepSuccess
	var lastOutput string
	if state.failedStep != "" {
		wfStatus = StepFailed
	}
	for _, name := range w.stepOrder {
		sr, ok := state.results[name]
		if !ok {
			continue
		}
		if sr.Status == StepSuccess && sr.Output != "" {
			lastOutput = sr.Output
		}
	}

	wfResult := WorkflowResult{
		Status:  wfStatus,
		Steps:   state.results,
		Context: state.wCtx,
		Usage:   totalUsage,
	}

	if w.onFinish != nil {
		w.safeCallback(func() { w.onFinish(wfResult) })
	}

	// Convert workflow step results to StepTrace slice in execution order.
	steps := workflowStepsToTraces(w.stepOrder, state.results)

	if wfStatus == StepFailed {
		var stepErr error
		if sr, ok := state.results[state.failedStep]; ok {
			stepErr = sr.Error
		}
		return AgentResult{Output: lastOutput, Usage: totalUsage, Steps: steps}, &WorkflowError{
			StepName: state.failedStep,
			Err:      stepErr,
			Result:   wfResult,
		}
	}

	return AgentResult{Output: lastOutput, Usage: totalUsage, Steps: steps}, nil
}

// workflowStepsToTraces converts workflow StepResults into StepTrace entries
// in the order defined by stepOrder. Skipped and pending steps are omitted.
func workflowStepsToTraces(order []string, results map[string]StepResult) []StepTrace {
	var traces []StepTrace
	for _, name := range order {
		sr, ok := results[name]
		if !ok || sr.Status == StepPending || sr.Status == StepSkipped {
			continue
		}
		trace := StepTrace{
			Name:     name,
			Type:     "step",
			Output:   truncateStr(sr.Output, 500),
			Duration: sr.Duration,
		}
		if sr.Error != nil {
			trace.Output = truncateStr(sr.Error.Error(), 500)
		}
		traces = append(traces, trace)
	}
	return traces
}

// runDAG executes the step graph reactively. Each step completion immediately
// triggers evaluation of its dependents, avoiding the latency penalty of
// wave-based batching where fast steps must wait for slow siblings to finish.
// Uses the pre-computed w.dependents forward adjacency built at construction time.
func (w *Workflow) runDAG(ctx context.Context, state *executionState, ch chan<- StreamEvent) {
	// Track completed steps and remaining in-degree for each step.
	// All access is serialized through the coordinator goroutine via done channel,
	// so no mutex is needed for these maps.
	completed := make(map[string]bool, len(w.steps))
	remaining := make(map[string]int, len(w.steps))
	for name := range w.steps {
		remaining[name] = len(w.edges[name])
	}

	// Mark pre-populated results (from resume) as completed and adjust in-degrees.
	for name := range w.steps {
		if _, has := state.getResult(name); has {
			completed[name] = true
		}
	}
	for name := range completed {
		for _, dep := range w.dependents[name] {
			if !completed[dep] {
				remaining[dep]--
			}
		}
	}

	// done receives step names as goroutines complete (success or fail).
	// Skipped steps are handled synchronously and never sent to this channel.
	done := make(chan string, len(w.steps))
	inflight := 0

	// skipStep marks a step as skipped due to upstream failure and recursively
	// propagates to any dependents that become ready (they'll also be skipped
	// since they have a failed upstream). Recursion depth is bounded by the
	// validated-acyclic DAG depth.
	var skipStep func(string)
	skipStep = func(name string) {
		state.setResult(name, StepResult{Name: name, Status: StepSkipped})
		state.mu.Lock()
		state.failureSkipped[name] = true
		state.mu.Unlock()
		completed[name] = true
		for _, dep := range w.dependents[name] {
			if !completed[dep] {
				remaining[dep]--
				if remaining[dep] == 0 {
					// Dependent is ready — it will also be skipped (failed upstream).
					skipStep(dep)
				}
			}
		}
	}

	// launch starts a step goroutine, or skips it synchronously if upstream failed.
	launch := func(name string) {
		if completed[name] {
			return
		}
		s := w.steps[name]
		if w.hasFailedUpstream(s, state) {
			skipStep(name)
			return
		}
		completed[name] = true
		inflight++
		go func() {
			w.executeStep(ctx, s, state, ch)
			done <- name
		}()
	}

	// Seed: launch all root steps (zero remaining dependencies).
	for _, name := range w.stepOrder {
		if remaining[name] == 0 && !completed[name] {
			launch(name)
		}
	}

	// React to completions: each finished step immediately unblocks dependents.
	// Propagation and readiness check are merged into a single pass.
	for inflight > 0 {
		name := <-done
		inflight--

		for _, dep := range w.dependents[name] {
			if !completed[dep] {
				remaining[dep]--
				if remaining[dep] == 0 {
					launch(dep)
				}
			}
		}
	}
}

// hasFailedUpstream checks if any upstream dependency of a step has failed,
// suspended, or was itself skipped due to an upstream failure. Steps skipped
// by a When() condition are treated as satisfied and do NOT propagate failure.
func (w *Workflow) hasFailedUpstream(s *stepConfig, state *executionState) bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	for _, dep := range s.after {
		r, ok := state.results[dep]
		if !ok {
			continue
		}
		if r.Status == StepFailed || r.Status == StepSuspended {
			return true
		}
		if r.Status == StepSkipped && state.failureSkipped[dep] {
			return true
		}
	}
	return false
}

// executeStep runs a single step, handling conditions, retries, and step types.
// When ch is non-nil, emits step-start and step-finish events.
func (w *Workflow) executeStep(ctx context.Context, s *stepConfig, state *executionState, ch chan<- StreamEvent) {
	start := time.Now()

	var stepSpan Span
	if w.tracer != nil {
		ctx, stepSpan = w.tracer.Start(ctx, "workflow.step",
			StringAttr("step.name", s.name))
	}
	endSpan := func(status string) {
		if stepSpan != nil {
			stepSpan.SetAttr(
				StringAttr("step.status", status),
				Float64Attr("step.duration_ms", float64(time.Since(start).Milliseconds())))
			stepSpan.End()
		}
	}

	// Check context cancellation before starting.
	if ctx.Err() != nil {
		state.setResult(s.name, StepResult{
			Name:     s.name,
			Status:   StepSkipped,
			Duration: time.Since(start),
		})
		endSpan("skipped")
		return
	}

	// Evaluate When() condition.
	if s.when != nil && !s.when(state.wCtx) {
		state.setResult(s.name, StepResult{
			Name:     s.name,
			Status:   StepSkipped,
			Duration: time.Since(start),
		})
		w.logger.Debug("step skipped (condition not met)", "workflow", w.name, "step", s.name)
		endSpan("skipped")
		return
	}

	// Emit step-start event.
	if ch != nil {
		select {
		case ch <- StreamEvent{Type: EventStepStart, Name: s.name}:
		case <-ctx.Done():
		}
	}

	// Mark as running.
	state.setResult(s.name, StepResult{
		Name:   s.name,
		Status: StepRunning,
	})

	var run func() error
	switch s.stepType {
	case stepTypeForEach:
		run = func() error { return w.executeForEach(ctx, s, state, ch) }
	case stepTypeDoUntil:
		run = func() error { return w.executeDoUntil(ctx, s, state) }
	case stepTypeDoWhile:
		run = func() error { return w.executeDoWhile(ctx, s, state) }
	default:
		run = func() error { return s.fn(ctx, state.wCtx) }
	}
	err := w.executeWithRetry(ctx, s, run)

	w.recordStepOutcome(s, state, err, stepSpan, time.Since(start), endSpan, ch)
}

// recordStepOutcome records the final step result (suspend, failure, or success)
// into the execution state. Handles span annotation, logging, onError callbacks,
// and fail-fast cancellation for failures.
func (w *Workflow) recordStepOutcome(s *stepConfig, state *executionState, err error, stepSpan Span, duration time.Duration, endSpan func(string), ch chan<- StreamEvent) {
	// Check for suspend (before error handling — suspend is not a failure).
	var suspend *errSuspend
	if errors.As(err, &suspend) {
		state.mu.Lock()
		state.results[s.name] = StepResult{
			Name:     s.name,
			Status:   StepSuspended,
			Duration: duration,
		}
		if state.suspendedStep == "" {
			state.suspendedStep = s.name
			state.suspendPayload = suspend.payload
		}
		state.mu.Unlock()
		w.logger.Info("step suspended", "workflow", w.name, "step", s.name)
		if ch != nil {
			select {
			case ch <- StreamEvent{Type: EventStepFinish, Name: s.name, Content: "suspended", Duration: duration}:
			default:
			}
		}
		endSpan("suspended")
		return
	}

	if err != nil {
		state.mu.Lock()
		state.results[s.name] = StepResult{
			Name:     s.name,
			Status:   StepFailed,
			Error:    err,
			Duration: duration,
		}
		if state.failedStep == "" {
			state.failedStep = s.name
		}
		state.mu.Unlock()

		w.logger.Error("step failed", "workflow", w.name, "step", s.name, "error", err)

		if w.onError != nil {
			w.safeCallback(func() { w.onError(s.name, err) })
		}

		// Fail fast: cancel context so in-flight steps get cancelled.
		state.cancel()
		if ch != nil {
			select {
			case ch <- StreamEvent{Type: EventStepFinish, Name: s.name, Content: err.Error(), Duration: duration}:
			default:
			}
		}
		if stepSpan != nil {
			stepSpan.Error(err)
		}
		endSpan("failed")
		return
	}

	// Success — read output from context for the result.
	output := w.readStepOutput(s, state.wCtx)
	state.setResult(s.name, StepResult{
		Name:     s.name,
		Status:   StepSuccess,
		Output:   output,
		Duration: duration,
	})
	w.logger.Info("step completed", "workflow", w.name, "step", s.name, "duration", duration)
	if ch != nil {
		select {
		case ch <- StreamEvent{Type: EventStepFinish, Name: s.name, Content: output, Duration: duration}:
		default:
		}
	}
	endSpan("success")
}

// executeWithRetry wraps a step execution function with retry logic.
// All step types (basic, ForEach, DoUntil, DoWhile) are routed through this.
func (w *Workflow) executeWithRetry(ctx context.Context, s *stepConfig, run func() error) error {
	maxAttempts := 1 + s.retry
	var lastErr error

	for attempt := range maxAttempts {
		if attempt > 0 {
			if s.retryDelay > 0 {
				t := time.NewTimer(s.retryDelay)
				select {
				case <-ctx.Done():
					t.Stop()
					return ctx.Err()
				case <-t.C:
				}
			}
			w.logger.Info("step retry", "workflow", w.name, "step", s.name, "attempt", attempt, "max_retries", s.retry)
		}

		lastErr = run()
		if lastErr == nil {
			return nil
		}

		// Don't retry on suspend — it's not a failure.
		var susp *errSuspend
		if errors.As(lastErr, &susp) {
			return lastErr
		}

		// Don't retry on context cancellation.
		if ctx.Err() != nil {
			return lastErr
		}
	}

	return lastErr
}

// readStepOutput reads the step's output from context based on naming conventions.
func (w *Workflow) readStepOutput(s *stepConfig, wCtx *WorkflowContext) string {
	// Try the explicit output key first.
	key := s.outputTo
	if key == "" {
		// Try common conventions (explicit checks avoid per-call slice allocation).
		if v, ok := wCtx.Get(s.name + outputSuffix); ok {
			return stringifyValue(v)
		}
		if v, ok := wCtx.Get(s.name + resultSuffix); ok {
			return stringifyValue(v)
		}
		return ""
	}
	if v, ok := wCtx.Get(key); ok {
		return stringifyValue(v)
	}
	return ""
}

// safeCallback runs a callback function, recovering from panics to prevent
// callback errors from affecting workflow results.
func (w *Workflow) safeCallback(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("callback panic", "workflow", w.name, "panic", r)
		}
	}()
	fn()
}
