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
	ctx := newWorkflowContext("hello")

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
	ctx := newWorkflowContext("")
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

	result, err := wf.Execute(context.Background(), AgentTask{Input: "fail"})
	if err != nil {
		t.Fatal(err)
	}

	// Should not have an error at Go level (workflow ran), but output indicates failure.
	if result.Output == "" {
		t.Error("expected error summary in output")
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

	wf.Execute(context.Background(), AgentTask{Input: "test"})

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

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	// 1 initial + 2 retries = 3.
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	// Output should contain error info.
	if result.Output == "" {
		t.Error("expected error summary in output")
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

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	// Should fail with error about missing IterOver.
	if result.Output == "" {
		t.Error("expected error output for missing IterOver")
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

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output == "" {
		t.Error("expected error output for missing Until condition")
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

	result, err := wf.Execute(context.Background(), AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output == "" {
		t.Error("expected error output for missing While condition")
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
