package oasis

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

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
