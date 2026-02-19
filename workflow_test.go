package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// --- WorkflowContext tests ---

func TestWorkflowContextGetSet(t *testing.T) {
	ctx := newWorkflowContext(AgentTask{Input: "hello"})

	if ctx.Input() != "hello" {
		t.Errorf("Input() = %q, want %q", ctx.Input(), "hello")
	}

	// Get on missing key.
	v, ok := ctx.Get("missing")
	if ok || v != nil {
		t.Errorf("Get(missing) = (%v, %v), want (nil, false)", v, ok)
	}

	// Set and Get.
	ctx.Set("key", "value")
	v, ok = ctx.Get("key")
	if !ok || v != "value" {
		t.Errorf("Get(key) = (%v, %v), want (value, true)", v, ok)
	}

	// Overwrite.
	ctx.Set("key", 42)
	v, _ = ctx.Get("key")
	if v != 42 {
		t.Errorf("Get(key) after overwrite = %v, want 42", v)
	}
}

func TestWorkflowContextAddUsage(t *testing.T) {
	ctx := newWorkflowContext(AgentTask{})
	ctx.addUsage(Usage{InputTokens: 10, OutputTokens: 5})
	ctx.addUsage(Usage{InputTokens: 20, OutputTokens: 15})

	v, ok := ctx.Get("_usage")
	if !ok {
		t.Fatal("expected _usage in context")
	}
	u := v.(Usage)
	if u.InputTokens != 30 || u.OutputTokens != 20 {
		t.Errorf("usage = %+v, want {InputTokens:30 OutputTokens:20}", u)
	}
}

// --- NewWorkflow validation tests ---

func TestNewWorkflowDuplicateStep(t *testing.T) {
	_, err := NewWorkflow("test", "test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
	)
	if err == nil {
		t.Fatal("expected error for duplicate step name")
	}
	if want := `workflow test: duplicate step name "a"`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestNewWorkflowUnknownDependency(t *testing.T) {
	_, err := NewWorkflow("test", "test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("b", func(_ context.Context, _ *WorkflowContext) error { return nil }, After("c")),
	)
	if err == nil {
		t.Fatal("expected error for unknown dependency")
	}
	if want := `workflow test: step "b" depends on unknown step "c"`; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestNewWorkflowCycleDetection(t *testing.T) {
	_, err := NewWorkflow("test", "test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }, After("b")),
		Step("b", func(_ context.Context, _ *WorkflowContext) error { return nil }, After("a")),
	)
	if err == nil {
		t.Fatal("expected error for cycle")
	}
	if want := "workflow test: cycle detected in step dependencies"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestNewWorkflowThreeNodeCycle(t *testing.T) {
	noop := func(_ context.Context, _ *WorkflowContext) error { return nil }
	_, err := NewWorkflow("test", "test",
		Step("a", noop, After("c")),
		Step("b", noop, After("a")),
		Step("c", noop, After("b")),
	)
	if err == nil {
		t.Fatal("expected error for 3-node cycle")
	}
}

func TestNewWorkflowValidGraph(t *testing.T) {
	noop := func(_ context.Context, _ *WorkflowContext) error { return nil }
	wf, err := NewWorkflow("test", "test",
		Step("a", noop),
		Step("b", noop, After("a")),
		Step("c", noop, After("a")),
		Step("d", noop, After("b", "c")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Name() != "test" {
		t.Errorf("Name() = %q, want %q", wf.Name(), "test")
	}
	if wf.Description() != "test" {
		t.Errorf("Description() = %q, want %q", wf.Description(), "test")
	}
}

// --- Sequential execution tests ---

func TestWorkflowSequential(t *testing.T) {
	var order []string

	wf, err := NewWorkflow("seq", "sequential test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			order = append(order, "a")
			wCtx.Set("a.output", "from-a")
			return nil
		}),
		Step("b", func(_ context.Context, wCtx *WorkflowContext) error {
			order = append(order, "b")
			v, _ := wCtx.Get("a.output")
			wCtx.Set("b.output", fmt.Sprintf("from-b(%v)", v))
			return nil
		}, After("a")),
		Step("c", func(_ context.Context, wCtx *WorkflowContext) error {
			order = append(order, "c")
			v, _ := wCtx.Get("b.output")
			wCtx.Set("c.output", fmt.Sprintf("from-c(%v)", v))
			return nil
		}, After("b")),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "start"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify order.
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("execution order = %v, want [a b c]", order)
	}

	// Verify output propagation.
	if result.Output != "from-c(from-b(from-a))" {
		t.Errorf("Output = %q, want %q", result.Output, "from-c(from-b(from-a))")
	}
}

// --- Parallel execution tests ---

func TestWorkflowParallel(t *testing.T) {
	var started atomic.Int32

	wf, err := NewWorkflow("par", "parallel test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "root")
			return nil
		}),
		Step("b", func(_ context.Context, wCtx *WorkflowContext) error {
			started.Add(1)
			time.Sleep(10 * time.Millisecond)
			wCtx.Set("b.output", "b-done")
			return nil
		}, After("a")),
		Step("c", func(_ context.Context, wCtx *WorkflowContext) error {
			started.Add(1)
			time.Sleep(10 * time.Millisecond)
			wCtx.Set("c.output", "c-done")
			return nil
		}, After("a")),
		Step("d", func(_ context.Context, wCtx *WorkflowContext) error {
			// d should only start after both b and c finished.
			if started.Load() != 2 {
				t.Error("d started before b and c completed")
			}
			wCtx.Set("d.output", "join-done")
			return nil
		}, After("b", "c")),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "join-done" {
		t.Errorf("Output = %q, want %q", result.Output, "join-done")
	}
}

// --- Conditional (When) tests ---

func TestWorkflowConditionalBranch(t *testing.T) {
	wf, err := NewWorkflow("cond", "conditional test",
		Step("init", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("type", "digital")
			return nil
		}),
		Step("physical", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("physical.output", "shipped")
			return nil
		}, After("init"), When(func(wCtx *WorkflowContext) bool {
			v, _ := wCtx.Get("type")
			return v == "physical"
		})),
		Step("digital", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("digital.output", "delivered")
			return nil
		}, After("init"), When(func(wCtx *WorkflowContext) bool {
			v, _ := wCtx.Get("type")
			return v == "digital"
		})),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "order"})
	if err != nil {
		t.Fatal(err)
	}

	// Only digital should have run.
	if result.Output != "delivered" {
		t.Errorf("Output = %q, want %q", result.Output, "delivered")
	}
}

func TestWorkflowSkippedByConditionDoesNotCascadeFailure(t *testing.T) {
	// When a step is skipped by When() condition, its dependents should still run.
	wf, err := NewWorkflow("cond-cascade", "condition cascade test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("b", func(_ context.Context, _ *WorkflowContext) error {
			return nil
		}, After("a"), When(func(_ *WorkflowContext) bool { return false })), // always skipped
		Step("c", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("c.output", "c-ran")
			return nil
		}, After("b")), // should still run because b was skipped by condition, not failure
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "c-ran" {
		t.Errorf("Output = %q, want %q", result.Output, "c-ran")
	}
}

// --- Failure cascade tests ---

func TestWorkflowFailFast(t *testing.T) {
	stepErr := errors.New("step b exploded")
	cRan := false

	wf, err := NewWorkflow("fail", "fail-fast test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("b", func(_ context.Context, _ *WorkflowContext) error {
			return stepErr
		}, After("a")),
		Step("c", func(_ context.Context, _ *WorkflowContext) error {
			cRan = true
			return nil
		}, After("b")),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "fail"})

	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
	if wfErr.StepName != "b" {
		t.Errorf("WorkflowError.StepName = %q, want %q", wfErr.StepName, "b")
	}
	if !errors.Is(err, stepErr) {
		t.Errorf("Unwrap chain should contain stepErr, got %v", wfErr.Err)
	}
	if cRan {
		t.Error("step c should not have run after b failed")
	}
}

func TestWorkflowFailureCascadesThroughMultipleLevels(t *testing.T) {
	// A -> B (fails) -> C -> D
	// C and D should both be skipped.
	dRan := false
	cRan := false

	wf, err := NewWorkflow("cascade", "failure cascade test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
		Step("b", func(_ context.Context, _ *WorkflowContext) error {
			return errors.New("b failed")
		}, After("a")),
		Step("c", func(_ context.Context, _ *WorkflowContext) error {
			cRan = true
			return nil
		}, After("b")),
		Step("d", func(_ context.Context, _ *WorkflowContext) error {
			dRan = true
			return nil
		}, After("c")),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "test"})

	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
	if cRan {
		t.Error("step c should not have run (upstream b failed)")
	}
	if dRan {
		t.Error("step d should not have run (upstream b->c cascade)")
	}
}

// --- Retry tests ---

func TestWorkflowRetry(t *testing.T) {
	attempts := 0

	wf, err := NewWorkflow("retry", "retry test",
		Step("flaky", func(_ context.Context, wCtx *WorkflowContext) error {
			attempts++
			if attempts < 3 {
				return errors.New("transient error")
			}
			wCtx.Set("flaky.output", "recovered")
			return nil
		}, Retry(2, time.Millisecond)),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if result.Output != "recovered" {
		t.Errorf("Output = %q, want %q", result.Output, "recovered")
	}
}

func TestWorkflowRetryExhausted(t *testing.T) {
	attempts := 0

	wf, err := NewWorkflow("retry-fail", "retry exhausted test",
		Step("always-fail", func(_ context.Context, _ *WorkflowContext) error {
			attempts++
			return errors.New("permanent error")
		}, Retry(2, time.Millisecond)),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "go"})
	// 1 initial + 2 retries = 3.
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
}

func TestWorkflowDefaultRetry(t *testing.T) {
	attempts := 0

	wf, err := NewWorkflow("default-retry", "default retry test",
		Step("flaky", func(_ context.Context, wCtx *WorkflowContext) error {
			attempts++
			if attempts < 2 {
				return errors.New("transient")
			}
			wCtx.Set("flaky.output", "ok")
			return nil
		}),
		WithDefaultRetry(1, time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

// --- AgentStep tests ---

func TestWorkflowAgentStep(t *testing.T) {
	agent := &stubAgent{
		name: "echo",
		desc: "Echoes input",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{
				Output: "agent says: " + task.Input,
				Usage:  Usage{InputTokens: 10, OutputTokens: 5},
			}, nil
		},
	}

	wf, err := NewWorkflow("agent-test", "agent step test",
		AgentStep("research", agent),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "agent says: hello" {
		t.Errorf("Output = %q, want %q", result.Output, "agent says: hello")
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 {
		t.Errorf("Usage = %+v, want {InputTokens:10 OutputTokens:5}", result.Usage)
	}
}

func TestWorkflowAgentStepInputFrom(t *testing.T) {
	agent := &stubAgent{
		name: "echo",
		desc: "Echoes input",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: "got: " + task.Input}, nil
		},
	}

	wf, err := NewWorkflow("agent-input", "agent inputfrom test",
		Step("prepare", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("query", "custom input")
			return nil
		}),
		AgentStep("process", agent, After("prepare"), InputFrom("query")),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "original"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "got: custom input" {
		t.Errorf("Output = %q, want %q", result.Output, "got: custom input")
	}
}

// --- ToolStep tests ---

func TestWorkflowToolStep(t *testing.T) {
	wf, err := NewWorkflow("tool-test", "tool step test",
		Step("prepare", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("args", `{"name":"world"}`)
			return nil
		}),
		ToolStep("greet", mockTool{}, "greet", After("prepare"), ArgsFrom("args")),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "hello from greet" {
		t.Errorf("Output = %q, want %q", result.Output, "hello from greet")
	}
}

func TestWorkflowToolStepNoArgs(t *testing.T) {
	wf, err := NewWorkflow("tool-noargs", "tool no args test",
		ToolStep("greet", mockTool{}, "greet"),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "hello from greet" {
		t.Errorf("Output = %q, want %q", result.Output, "hello from greet")
	}
}

// --- ForEach tests ---

func TestWorkflowForEachSequential(t *testing.T) {
	wf, err := NewWorkflow("foreach-seq", "foreach sequential test",
		Step("seed", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("items", []any{"a", "b", "c"})
			return nil
		}),
		ForEach("process", func(ctx context.Context, wCtx *WorkflowContext) error {
			item, ok := ForEachItem(ctx)
			if !ok {
				return errors.New("no item in context")
			}
			// Accumulate results in a thread-safe way.
			v, _ := wCtx.Get("results")
			var results []string
			if v != nil {
				results = v.([]string)
			}
			results = append(results, fmt.Sprintf("processed-%v", item))
			wCtx.Set("results", results)
			return nil
		}, After("seed"), IterOver("items")),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowForEachConcurrent(t *testing.T) {
	var processed atomic.Int32

	wf, err := NewWorkflow("foreach-conc", "foreach concurrent test",
		Step("seed", func(_ context.Context, wCtx *WorkflowContext) error {
			items := make([]any, 10)
			for i := range items {
				items[i] = i
			}
			wCtx.Set("items", items)
			return nil
		}),
		ForEach("process", func(ctx context.Context, _ *WorkflowContext) error {
			item, ok := ForEachItem(ctx)
			if !ok {
				return errors.New("no item")
			}
			idx, ok := ForEachIndex(ctx)
			if !ok {
				return errors.New("no index")
			}
			// Verify item matches index.
			if item.(int) != idx {
				return fmt.Errorf("item %v != index %d", item, idx)
			}
			processed.Add(1)
			return nil
		}, After("seed"), IterOver("items"), Concurrency(4)),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if processed.Load() != 10 {
		t.Errorf("processed = %d, want 10", processed.Load())
	}
}

func TestWorkflowForEachNoItemRace(t *testing.T) {
	// Verify that concurrent ForEach iterations don't see each other's items.
	// Each item is a unique int; we collect what each goroutine saw.
	type seen struct {
		item  int
		index int
	}
	ch := make(chan seen, 100)

	wf, err := NewWorkflow("foreach-race", "foreach race test",
		Step("seed", func(_ context.Context, wCtx *WorkflowContext) error {
			items := make([]any, 100)
			for i := range items {
				items[i] = i
			}
			wCtx.Set("items", items)
			return nil
		}),
		ForEach("check", func(ctx context.Context, _ *WorkflowContext) error {
			item, _ := ForEachItem(ctx)
			idx, _ := ForEachIndex(ctx)
			ch <- seen{item: item.(int), index: idx}
			return nil
		}, After("seed"), IterOver("items"), Concurrency(10)),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	close(ch)

	for s := range ch {
		if s.item != s.index {
			t.Errorf("goroutine saw item=%d but index=%d (race detected)", s.item, s.index)
		}
	}
}

func TestWorkflowForEachMissingIterOver(t *testing.T) {
	wf, err := NewWorkflow("foreach-missing", "foreach missing iterover",
		ForEach("bad", func(_ context.Context, _ *WorkflowContext) error {
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "go"})
	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
}

// --- DoUntil tests ---

func TestWorkflowDoUntil(t *testing.T) {
	wf, err := NewWorkflow("dountil", "do until test",
		DoUntil("count", func(_ context.Context, wCtx *WorkflowContext) error {
			v, _ := wCtx.Get("counter")
			counter := 0
			if v != nil {
				counter = v.(int)
			}
			counter++
			wCtx.Set("counter", counter)
			return nil
		}, Until(func(wCtx *WorkflowContext) bool {
			v, _ := wCtx.Get("counter")
			return v.(int) >= 5
		}), MaxIter(20)),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	_ = result
}

func TestWorkflowDoUntilMaxIter(t *testing.T) {
	iterations := 0

	wf, err := NewWorkflow("dountil-max", "do until max iter test",
		DoUntil("infinite", func(_ context.Context, _ *WorkflowContext) error {
			iterations++
			return nil
		}, Until(func(_ *WorkflowContext) bool {
			return false // never true
		}), MaxIter(5)),
	)
	if err != nil {
		t.Fatal(err)
	}

	wf.Execute(context.Background(), AgentTask{Input: "go"})
	if iterations != 5 {
		t.Errorf("iterations = %d, want 5", iterations)
	}
}

func TestWorkflowDoUntilMissingCondition(t *testing.T) {
	wf, err := NewWorkflow("dountil-nocond", "do until no condition",
		DoUntil("bad", func(_ context.Context, _ *WorkflowContext) error {
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "go"})
	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
}

// --- DoWhile tests ---

func TestWorkflowDoWhile(t *testing.T) {
	iterations := 0

	wf, err := NewWorkflow("dowhile", "do while test",
		DoWhile("count", func(_ context.Context, wCtx *WorkflowContext) error {
			iterations++
			wCtx.Set("counter", iterations)
			return nil
		}, While(func(wCtx *WorkflowContext) bool {
			v, _ := wCtx.Get("counter")
			return v.(int) < 3
		}), MaxIter(10)),
	)
	if err != nil {
		t.Fatal(err)
	}

	wf.Execute(context.Background(), AgentTask{Input: "go"})
	if iterations != 3 {
		t.Errorf("iterations = %d, want 3", iterations)
	}
}

func TestWorkflowDoWhileMissingCondition(t *testing.T) {
	wf, err := NewWorkflow("dowhile-nocond", "do while no condition",
		DoWhile("bad", func(_ context.Context, _ *WorkflowContext) error {
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = wf.Execute(context.Background(), AgentTask{Input: "go"})
	var wfErr *WorkflowError
	if !errors.As(err, &wfErr) {
		t.Fatalf("expected *WorkflowError, got %v", err)
	}
}

// --- Callback tests ---

func TestWorkflowOnFinishCallback(t *testing.T) {
	var callbackResult WorkflowResult

	wf, err := NewWorkflow("callback", "callback test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "done")
			return nil
		}),
		WithOnFinish(func(r WorkflowResult) {
			callbackResult = r
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	wf.Execute(context.Background(), AgentTask{Input: "go"})

	if callbackResult.Status != StepSuccess {
		t.Errorf("callback status = %q, want %q", callbackResult.Status, StepSuccess)
	}
	if len(callbackResult.Steps) != 1 {
		t.Errorf("callback steps count = %d, want 1", len(callbackResult.Steps))
	}
}

func TestWorkflowOnErrorCallback(t *testing.T) {
	var errorStep string
	var errorErr error

	wf, err := NewWorkflow("error-cb", "error callback test",
		Step("fail", func(_ context.Context, _ *WorkflowContext) error {
			return errors.New("boom")
		}),
		WithOnError(func(step string, err error) {
			errorStep = step
			errorErr = err
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	wf.Execute(context.Background(), AgentTask{Input: "go"})

	if errorStep != "fail" {
		t.Errorf("onError step = %q, want %q", errorStep, "fail")
	}
	if errorErr == nil || errorErr.Error() != "boom" {
		t.Errorf("onError err = %v, want boom", errorErr)
	}
}

func TestWorkflowCallbackPanicRecovery(t *testing.T) {
	wf, err := NewWorkflow("panic-cb", "panic callback test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "ok")
			return nil
		}),
		WithOnFinish(func(_ WorkflowResult) {
			panic("callback exploded")
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Should not panic.
	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "ok" {
		t.Errorf("Output = %q, want %q", result.Output, "ok")
	}
}

// --- OutputTo tests ---

func TestWorkflowOutputTo(t *testing.T) {
	agent := &stubAgent{
		name: "echo",
		desc: "Echoes input",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: task.Input}, nil
		},
	}

	wf, err := NewWorkflow("output-to", "output-to test",
		AgentStep("a", agent, OutputTo("custom_key")),
		Step("b", func(_ context.Context, wCtx *WorkflowContext) error {
			v, ok := wCtx.Get("custom_key")
			if !ok {
				return errors.New("custom_key not found")
			}
			wCtx.Set("b.output", fmt.Sprintf("got: %v", v))
			return nil
		}, After("a")),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "got: hello" {
		t.Errorf("Output = %q, want %q", result.Output, "got: hello")
	}
}

// --- Context cancellation tests ---

func TestWorkflowContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bRan := false

	wf, err := NewWorkflow("cancel", "cancellation test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error {
			cancel() // cancel context during step a
			return nil
		}),
		Step("b", func(_ context.Context, _ *WorkflowContext) error {
			bRan = true
			return nil
		}, After("a")),
	)
	if err != nil {
		t.Fatal(err)
	}

	wf.Execute(ctx, AgentTask{Input: "go"})
	if bRan {
		t.Error("step b should not have run after context cancellation")
	}
}

// --- Agent interface compliance ---

func TestWorkflowImplementsAgent(t *testing.T) {
	wf, err := NewWorkflow("test", "test",
		Step("a", func(_ context.Context, _ *WorkflowContext) error { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	var _ Agent = wf
}

// --- Integration: Workflow as agent in Network ---

func TestWorkflowAsNetworkAgent(t *testing.T) {
	wf, err := NewWorkflow("inner", "Inner workflow",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "workflow result: "+wCtx.Input())
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Use workflow as a subagent in a Network.
	router := &mockProvider{
		name: "router",
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{
				ID:   "1",
				Name: "agent_inner",
				Args: json.RawMessage(`{"task":"do the thing"}`),
			}}},
			{Content: "network got workflow result"},
		},
	}

	network := NewNetwork("outer", "Outer network", router, WithAgents(wf))
	result, err := network.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "network got workflow result" {
		t.Errorf("Output = %q, want %q", result.Output, "network got workflow result")
	}
}

// --- Input propagation ---

func TestWorkflowInputPropagation(t *testing.T) {
	wf, err := NewWorkflow("input", "input test",
		Step("a", func(_ context.Context, wCtx *WorkflowContext) error {
			wCtx.Set("a.output", "input was: "+wCtx.Input())
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "input was: hello world" {
		t.Errorf("Output = %q, want %q", result.Output, "input was: hello world")
	}
}

// --- Empty workflow ---

func TestWorkflowEmptySteps(t *testing.T) {
	wf, err := NewWorkflow("empty", "empty workflow")
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "" {
		t.Errorf("Output = %q, want empty", result.Output)
	}
}

// --- Resolve tests ---

func TestWorkflowContextResolve(t *testing.T) {
	tests := []struct {
		name     string
		template string
		values   map[string]any
		want     string
	}{
		{"no placeholders", "hello world", nil, "hello world"},
		{"single placeholder", "hello {{name}}", map[string]any{"name": "Alice"}, "hello Alice"},
		{"multiple placeholders", "{{a}} and {{b}}", map[string]any{"a": "X", "b": "Y"}, "X and Y"},
		{"missing key", "hello {{unknown}}", nil, "hello "},
		{"numeric value", "count: {{n}}", map[string]any{"n": 42}, "count: 42"},
		{"empty template", "", nil, ""},
		{"adjacent placeholders", "{{a}}{{b}}", map[string]any{"a": "1", "b": "2"}, "12"},
		{"unclosed brace", "hello {{name", nil, "hello {{name"},
		{"whitespace in key", "{{ name }}", map[string]any{"name": "Bob"}, "Bob"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wCtx := newWorkflowContext(AgentTask{})
			for k, v := range tt.values {
				wCtx.Set(k, v)
			}
			got := wCtx.Resolve(tt.template)
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}

func TestWorkflowContextResolveJSON(t *testing.T) {
	wCtx := newWorkflowContext(AgentTask{})
	wCtx.Set("name", "Alice")
	wCtx.Set("data", map[string]any{"x": 1, "y": 2})

	// Single placeholder with string value -> JSON string.
	got := string(wCtx.ResolveJSON("{{name}}"))
	if got != `"Alice"` {
		t.Errorf("ResolveJSON(string) = %s, want %q", got, `"Alice"`)
	}

	// Single placeholder with structured value -> JSON object.
	got = string(wCtx.ResolveJSON("{{data}}"))
	if got != `{"x":1,"y":2}` {
		t.Errorf("ResolveJSON(map) = %s, want %s", got, `{"x":1,"y":2}`)
	}

	// Mixed text -> JSON string.
	got = string(wCtx.ResolveJSON("hello {{name}}"))
	if got != `"hello Alice"` {
		t.Errorf("ResolveJSON(mixed) = %s, want %q", got, `"hello Alice"`)
	}

	// Missing key -> null.
	got = string(wCtx.ResolveJSON("{{missing}}"))
	if got != "null" {
		t.Errorf("ResolveJSON(missing) = %s, want null", got)
	}
}

// --- Expression evaluator tests ---

func TestEvalExpression(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		values  map[string]any
		want    bool
		wantErr bool
	}{
		{"string equal", "{{status}} == 'active'", map[string]any{"status": "active"}, true, false},
		{"string not equal", "{{status}} != 'active'", map[string]any{"status": "inactive"}, true, false},
		{"string equal false", "{{status}} == 'active'", map[string]any{"status": "inactive"}, false, false},
		{"numeric greater", "{{score}} > 0.5", map[string]any{"score": 0.8}, true, false},
		{"numeric less", "{{score}} < 0.5", map[string]any{"score": 0.3}, true, false},
		{"numeric equal", "{{score}} == 1", map[string]any{"score": 1.0}, true, false},
		{"numeric gte", "{{score}} >= 0.5", map[string]any{"score": 0.5}, true, false},
		{"numeric lte", "{{score}} <= 0.5", map[string]any{"score": 0.5}, true, false},
		{"contains true", "{{text}} contains 'urgent'", map[string]any{"text": "this is urgent"}, true, false},
		{"contains false", "{{text}} contains 'urgent'", map[string]any{"text": "this is normal"}, false, false},
		{"empty string check", "{{result}} != ''", map[string]any{"result": "data"}, true, false},
		{"empty string empty", "{{result}} != ''", map[string]any{"result": ""}, false, false},
		{"no operator", "just a string", nil, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wCtx := newWorkflowContext(AgentTask{})
			for k, v := range tt.values {
				wCtx.Set(k, v)
			}
			got, err := evalExpression(tt.expr, wCtx)
			if (err != nil) != tt.wantErr {
				t.Fatalf("evalExpression(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("evalExpression(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

// --- FromDefinition tests ---

func TestFromDefinitionLLMNode(t *testing.T) {
	agent := &stubAgent{
		name: "writer",
		desc: "Writes text",
		fn: func(task AgentTask) (AgentResult, error) {
			return AgentResult{Output: "wrote: " + task.Input}, nil
		},
	}

	def := WorkflowDefinition{
		Name:        "llm-test",
		Description: "LLM node test",
		Nodes: []NodeDefinition{
			{ID: "write", Type: NodeLLM, Agent: "writer", Input: "Summarize: {{input}}"},
		},
		Edges: [][2]string{},
	}

	reg := DefinitionRegistry{
		Agents: map[string]Agent{"writer": agent},
	}

	wf, err := FromDefinition(def, reg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "wrote: Summarize: hello" {
		t.Errorf("Output = %q, want %q", result.Output, "wrote: Summarize: hello")
	}
}

func TestFromDefinitionToolNode(t *testing.T) {
	tool := mockTool{} // returns "hello from <name>"

	def := WorkflowDefinition{
		Name:        "tool-test",
		Description: "Tool node test",
		Nodes: []NodeDefinition{
			{ID: "greet", Type: NodeTool, Tool: "greeter", ToolName: "greet"},
		},
		Edges: [][2]string{},
	}

	reg := DefinitionRegistry{
		Tools: map[string]Tool{"greeter": tool},
	}

	wf, err := FromDefinition(def, reg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "hello from greet" {
		t.Errorf("Output = %q, want %q", result.Output, "hello from greet")
	}
}

func TestFromDefinitionTemplateNode(t *testing.T) {
	def := WorkflowDefinition{
		Name:        "tmpl-test",
		Description: "Template node test",
		Nodes: []NodeDefinition{
			{ID: "set", Type: NodeTemplate, Template: "no templates here"},
			{ID: "fmt", Type: NodeTemplate, Template: "Result: {{set.output}}", OutputTo: "final"},
		},
		Edges: [][2]string{{"set", "fmt"}},
	}

	wf, err := FromDefinition(def, DefinitionRegistry{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "Result: no templates here" {
		t.Errorf("Output = %q, want %q", result.Output, "Result: no templates here")
	}
}

func TestFromDefinitionConditionBranching(t *testing.T) {
	def := WorkflowDefinition{
		Name:        "cond-test",
		Description: "Condition branching test",
		Nodes: []NodeDefinition{
			{ID: "setup", Type: NodeTemplate, Template: "data"},
			{ID: "check", Type: NodeCondition,
				Expression:  "{{setup.output}} == 'data'",
				TrueBranch:  []string{"yes"},
				FalseBranch: []string{"no"},
			},
			{ID: "yes", Type: NodeTemplate, Template: "took true branch"},
			{ID: "no", Type: NodeTemplate, Template: "took false branch"},
		},
		Edges: [][2]string{{"setup", "check"}, {"check", "yes"}, {"check", "no"}},
	}

	wf, err := FromDefinition(def, DefinitionRegistry{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "took true branch" {
		t.Errorf("Output = %q, want %q", result.Output, "took true branch")
	}
}

func TestFromDefinitionConditionFalseBranch(t *testing.T) {
	def := WorkflowDefinition{
		Name:        "cond-false",
		Description: "Condition false branch test",
		Nodes: []NodeDefinition{
			{ID: "setup", Type: NodeTemplate, Template: "other"},
			{ID: "check", Type: NodeCondition,
				Expression:  "{{setup.output}} == 'data'",
				TrueBranch:  []string{"yes"},
				FalseBranch: []string{"no"},
			},
			{ID: "yes", Type: NodeTemplate, Template: "took true branch"},
			{ID: "no", Type: NodeTemplate, Template: "took false branch"},
		},
		Edges: [][2]string{{"setup", "check"}, {"check", "yes"}, {"check", "no"}},
	}

	wf, err := FromDefinition(def, DefinitionRegistry{})
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "took false branch" {
		t.Errorf("Output = %q, want %q", result.Output, "took false branch")
	}
}

func TestFromDefinitionRegisteredCondition(t *testing.T) {
	def := WorkflowDefinition{
		Name:        "cond-func",
		Description: "Registered condition function test",
		Nodes: []NodeDefinition{
			{ID: "check", Type: NodeCondition,
				Expression:  "always_true",
				TrueBranch:  []string{"yes"},
				FalseBranch: []string{"no"},
			},
			{ID: "yes", Type: NodeTemplate, Template: "true path"},
			{ID: "no", Type: NodeTemplate, Template: "false path"},
		},
		Edges: [][2]string{{"check", "yes"}, {"check", "no"}},
	}

	reg := DefinitionRegistry{
		Conditions: map[string]func(*WorkflowContext) bool{
			"always_true": func(_ *WorkflowContext) bool { return true },
		},
	}

	wf, err := FromDefinition(def, reg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "true path" {
		t.Errorf("Output = %q, want %q", result.Output, "true path")
	}
}

// --- FromDefinition validation tests ---

func TestFromDefinitionValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		def  WorkflowDefinition
		reg  DefinitionRegistry
	}{
		{
			"no nodes",
			WorkflowDefinition{Name: "empty"},
			DefinitionRegistry{},
		},
		{
			"duplicate node ID",
			WorkflowDefinition{Name: "dup", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeTemplate, Template: "x"},
				{ID: "a", Type: NodeTemplate, Template: "y"},
			}},
			DefinitionRegistry{},
		},
		{
			"unknown edge target",
			WorkflowDefinition{Name: "bad-edge", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeTemplate, Template: "x"},
			}, Edges: [][2]string{{"a", "b"}}},
			DefinitionRegistry{},
		},
		{
			"unknown agent",
			WorkflowDefinition{Name: "bad-agent", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeLLM, Agent: "missing"},
			}},
			DefinitionRegistry{Agents: map[string]Agent{}},
		},
		{
			"unknown tool",
			WorkflowDefinition{Name: "bad-tool", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeTool, Tool: "missing"},
			}},
			DefinitionRegistry{Tools: map[string]Tool{}},
		},
		{
			"condition no branches",
			WorkflowDefinition{Name: "bad-cond", Nodes: []NodeDefinition{
				{ID: "a", Type: NodeCondition, Expression: "{{x}} == 1"},
			}},
			DefinitionRegistry{},
		},
		{
			"unknown node type",
			WorkflowDefinition{Name: "bad-type", Nodes: []NodeDefinition{
				{ID: "a", Type: "invalid"},
			}},
			DefinitionRegistry{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := FromDefinition(tt.def, tt.reg)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestFromDefinitionToolWithTemplateArgs(t *testing.T) {
	// Tool that echoes its args as content.
	echoTool := &argEchoTool{}

	def := WorkflowDefinition{
		Name:        "tool-args",
		Description: "Tool with template args",
		Nodes: []NodeDefinition{
			{ID: "setup", Type: NodeTemplate, Template: "world"},
			{ID: "call", Type: NodeTool, Tool: "echo", ToolName: "echo_args",
				Args: map[string]any{"greeting": "hello {{setup.output}}"}},
		},
		Edges: [][2]string{{"setup", "call"}},
	}

	reg := DefinitionRegistry{
		Tools: map[string]Tool{"echo": echoTool},
	}

	wf, err := FromDefinition(def, reg)
	if err != nil {
		t.Fatal(err)
	}

	result, err := wf.Execute(context.Background(), AgentTask{Input: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// The argEchoTool returns the raw JSON args as content.
	if result.Output == "" {
		t.Error("expected non-empty output from tool with template args")
	}
}

// argEchoTool is a test tool that returns its arguments as the result content.
type argEchoTool struct{}

func (a *argEchoTool) Definitions() []ToolDefinition {
	return []ToolDefinition{{Name: "echo_args", Description: "Echoes args"}}
}

func (a *argEchoTool) Execute(_ context.Context, _ string, args json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: string(args)}, nil
}
