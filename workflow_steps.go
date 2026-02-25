package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// --- ForEach iteration helpers ---

// forEachIterCtxKey is the context key for per-iteration ForEach data.
type forEachIterCtxKey struct{}

// forEachIter carries the current element and index for a ForEach iteration.
type forEachIter struct {
	item  any
	index int
}

// ForEachItem retrieves the current iteration element inside a ForEach step function.
// Returns the element and true if called from within a ForEach step, or nil and false otherwise.
// The element is carried via context.Context (not WorkflowContext), so it is safe
// for concurrent iterations — each goroutine sees its own element.
func ForEachItem(ctx context.Context) (any, bool) {
	if v, ok := ctx.Value(forEachIterCtxKey{}).(forEachIter); ok {
		return v.item, true
	}
	return nil, false
}

// ForEachIndex retrieves the current iteration index (0-based) inside a ForEach step function.
// Returns the index and true if called from within a ForEach step, or -1 and false otherwise.
func ForEachIndex(ctx context.Context) (int, bool) {
	if v, ok := ctx.Value(forEachIterCtxKey{}).(forEachIter); ok {
		return v.index, true
	}
	return -1, false
}

// --- Agent and Tool step wrappers ---

// agentStepFunc wraps an Agent into a StepFunc. Input is read from context
// (via InputFrom key) or from the original task input. Output and usage are
// written back to context.
func agentStepFunc(agent Agent, cfg *stepConfig) StepFunc {
	return func(ctx context.Context, wCtx *WorkflowContext) error {
		input := wCtx.Input()
		if cfg.inputFrom != "" {
			if v, ok := wCtx.Get(cfg.inputFrom); ok {
				input = stringifyValue(v)
			}
		}

		result, err := agent.Execute(ctx, AgentTask{
			Input:       input,
			Attachments: wCtx.task.Attachments,
			Context:     wCtx.task.Context,
		})
		if err != nil {
			return err
		}

		outputKey := cfg.name + outputSuffix
		if cfg.outputTo != "" {
			outputKey = cfg.outputTo
		}
		wCtx.Set(outputKey, result.Output)

		// Accumulate usage via atomic helper.
		wCtx.addUsage(result.Usage)

		return nil
	}
}

// toolStepFunc wraps a Tool call into a StepFunc. Args are read from context
// (via ArgsFrom key) and the tool result is written back to context.
func toolStepFunc(tool Tool, toolName string, cfg *stepConfig) StepFunc {
	return func(ctx context.Context, wCtx *WorkflowContext) error {
		var args json.RawMessage
		if cfg.argsFrom != "" {
			if v, ok := wCtx.Get(cfg.argsFrom); ok {
				switch a := v.(type) {
				case json.RawMessage:
					args = a
				case string:
					args = json.RawMessage(a)
				default:
					b, err := json.Marshal(v)
					if err != nil {
						return fmt.Errorf("step %s: marshal tool args: %w", cfg.name, err)
					}
					args = b
				}
			}
		}
		if args == nil {
			args = json.RawMessage(`{}`)
		}

		result, err := tool.Execute(ctx, toolName, args)
		if err != nil {
			return err
		}
		if result.Error != "" {
			return fmt.Errorf("step %s: tool %s returned error: %s", cfg.name, toolName, result.Error)
		}

		outputKey := cfg.name + resultSuffix
		if cfg.outputTo != "" {
			outputKey = cfg.outputTo
		}
		wCtx.Set(outputKey, result.Content)
		return nil
	}
}

// --- Loop executors ---

// executeForEach iterates over a collection from context, running the step
// function once per element. Concurrency is bounded by the step's concurrency setting.
func (w *Workflow) executeForEach(ctx context.Context, s *stepConfig, state *executionState) error {
	if s.iterOver == "" {
		return fmt.Errorf("step %s: ForEach requires IterOver option", s.name)
	}

	v, ok := state.wCtx.Get(s.iterOver)
	if !ok {
		return fmt.Errorf("step %s: IterOver key %q not found in context", s.name, s.iterOver)
	}

	items, ok := v.([]any)
	if !ok {
		return fmt.Errorf("step %s: IterOver key %q is not []any", s.name, s.iterOver)
	}

	if len(items) == 0 {
		return nil
	}

	concurrency := s.concurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	// Cancel remaining iterations on first failure.
	iterCtx, iterCancel := context.WithCancel(ctx)
	defer iterCancel()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	for i, item := range items {
		// The select races context cancellation against semaphore acquisition.
		// On cancellation the zero-value case fires, falls through past the
		// continue, and hits the break to exit the loop. On semaphore
		// acquisition the goroutine is launched and continue skips the break.
		select {
		case <-iterCtx.Done():
		case sem <- struct{}{}:
			wg.Add(1)
			go func(elem any, idx int) {
				defer wg.Done()
				defer func() { <-sem }()

				if iterCtx.Err() != nil {
					return
				}

				elemCtx := context.WithValue(iterCtx, forEachIterCtxKey{}, forEachIter{
					item:  elem,
					index: idx,
				})

				if err := s.fn(elemCtx, state.wCtx); err != nil {
					errOnce.Do(func() { firstErr = err })
					iterCancel()
				}
			}(item, i)
			continue
		}
		break
	}

	wg.Wait()
	return firstErr
}

// executeDoUntil repeats a step function until the condition returns true,
// capped by maxIter.
func (w *Workflow) executeDoUntil(ctx context.Context, s *stepConfig, state *executionState) error {
	if s.until == nil {
		return fmt.Errorf("step %s: DoUntil requires Until option", s.name)
	}

	maxIter := s.maxIter
	if maxIter <= 0 {
		maxIter = defaultLoopMaxIter
	}

	for i := 0; i < maxIter; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := s.fn(ctx, state.wCtx); err != nil {
			return err
		}

		if s.until(state.wCtx) {
			return nil
		}
	}

	w.logger.Warn("step reached max iterations", "workflow", w.name, "step", s.name, "max_iter", maxIter)
	return fmt.Errorf("step %s: %w", s.name, ErrMaxIterExceeded)
}

// executeDoWhile repeats a step function while the condition returns true,
// capped by maxIter. The first iteration always runs.
func (w *Workflow) executeDoWhile(ctx context.Context, s *stepConfig, state *executionState) error {
	if s.whileFn == nil {
		return fmt.Errorf("step %s: DoWhile requires While option", s.name)
	}

	maxIter := s.maxIter
	if maxIter <= 0 {
		maxIter = defaultLoopMaxIter
	}

	for i := 0; i < maxIter; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// After the first iteration, check the while condition.
		if i > 0 && !s.whileFn(state.wCtx) {
			return nil
		}

		if err := s.fn(ctx, state.wCtx); err != nil {
			return err
		}
	}

	w.logger.Warn("step reached max iterations", "workflow", w.name, "step", s.name, "max_iter", maxIter)
	return fmt.Errorf("step %s: %w", s.name, ErrMaxIterExceeded)
}
