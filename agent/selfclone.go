// agent/selfclone.go
//
// Self-cloning: an agent spawns ephemeral copies of itself — same prompt,
// tools, and provider, but a FRESH context (no conversation memory, no input
// handler) — to work on self-contained subtasks. Emitting several
// spawn_subagent calls in one assistant message runs the copies concurrently
// (the loop's dispatchParallel), mirroring the deepagents "general-purpose
// subagent" pattern where parallelism falls out of multi-tool-call messages.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/internal/runtime"
	"github.com/nevindra/oasis/memory"
)

// WithSelfClone registers the spawn_subagent built-in: the agent may spawn up
// to maxPerRun ephemeral copies of itself per Execute run, each bounded by
// timeout (0 = unbounded). Copies share the agent's prompt/tools/provider but
// start with a fresh context, cannot persist to the agent's memory, cannot
// ask the user questions, and cannot spawn further copies.
func WithSelfClone(maxPerRun int, timeout time.Duration) AgentOption {
	return func(c *Config) {
		c.SelfCloneMax = maxPerRun
		c.SelfCloneTimeout = timeout
	}
}

type selfCloneArgs struct {
	Task string `json:"task" describe:"The complete, self-contained assignment for your copy. It CANNOT see this conversation: include every fact, constraint, and piece of context it needs, plus what its final report must contain."`
}

var selfCloneSchema = core.DeriveSchema[selfCloneArgs]()

// selfCloneToolDef is the LLM-facing definition of the spawn_subagent tool.
func selfCloneToolDef(maxPerRun int) core.ToolDefinition {
	return core.ToolDefinition{
		Name: core.ToolSelfClone,
		Description: fmt.Sprintf(
			"Spawn an ephemeral copy of yourself to work on a self-contained subtask. "+
				"The copy has your same instructions and tools but a FRESH context — it cannot see this conversation. "+
				"To parallelize independent subtasks, issue SEVERAL spawn_subagent calls together in ONE response; they run concurrently. "+
				"Each call blocks until its copy finishes and returns the copy's final report as the tool result (\"error: ...\" on failure). "+
				"Copies cannot spawn further copies or ask the user questions. At most %d copies per run — split the work accordingly.",
			maxPerRun),
		Parameters: selfCloneSchema,
	}
}

// cloneCounterKey carries the per-run spawn counter. Attached by executeRaw
// so the budget is scoped to exactly one Execute run even when the dispatch
// closure is cached across runs.
type cloneCounterKey struct{}

func withCloneCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, cloneCounterKey{}, &atomic.Int32{})
}

func cloneCounterFrom(ctx context.Context) *atomic.Int32 {
	v, _ := ctx.Value(cloneCounterKey{}).(*atomic.Int32)
	return v
}

// dispatchSelfClone handles one spawn_subagent call: builds a fresh copy of
// the agent, runs the task to completion, and returns the copy's final report
// as the tool result. Emits agent-start/agent-finish stream events so
// consumers render clones exactly like network delegations.
func (a *LLMAgent) dispatchSelfClone(ctx context.Context, tc core.ToolCall, ch chan<- core.StreamEvent, cfg *Config) DispatchResult {
	var args selfCloneArgs
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		return DispatchResult{Content: "error: invalid spawn_subagent args: " + err.Error(), IsError: true}
	}
	if args.Task == "" {
		return DispatchResult{Content: "error: spawn_subagent requires a non-empty task", IsError: true}
	}

	counter := cloneCounterFrom(ctx)
	if counter == nil {
		// Defensive: no per-run counter means no budget tracking — refuse
		// rather than allow unbounded recursion.
		return DispatchResult{Content: "error: spawn_subagent unavailable in this run", IsError: true}
	}
	n := counter.Add(1)
	if int(n) > cfg.SelfCloneMax {
		return DispatchResult{
			Content: fmt.Sprintf("error: spawn_subagent budget exhausted (max %d copies per run) — do the remaining work yourself or consolidate subtasks", cfg.SelfCloneMax),
			IsError: true,
		}
	}

	cloneName := fmt.Sprintf("%s-%d", a.Name(), n)
	parentTask, _ := TaskFromContext(ctx)
	subTask := parentTask
	subTask.Input = args.Task

	a.Logger().Info("spawning self-clone", "agent", a.Name(), "clone", cloneName, "task", TruncateStr(args.Task, 80))

	if ch != nil {
		select {
		case ch <- core.StreamEvent{Type: core.EventAgentStart, Name: cloneName, Content: args.Task}:
		case <-ctx.Done():
			return DispatchResult{Content: ctx.Err().Error(), IsError: true}
		}
	}

	_, provider := a.ResolvePromptAndProviderWith(ctx, parentTask, cfg)
	clone := newCloneAgent(cloneName, a.Description(), provider, cfg)

	execCtx := ctx
	if cfg.SelfCloneTimeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, cfg.SelfCloneTimeout)
		defer cancel()
	}

	start := time.Now()
	result, err := ExecuteAgent(execCtx, clone, cloneName, subTask, ch, a.Logger())
	elapsed := time.Since(start)
	if err != nil && ctx.Err() == nil && execCtx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("subagent %q timed out after %s: %w", cloneName, cfg.SelfCloneTimeout, err)
	}

	if ch != nil {
		output := result.Output
		if err != nil {
			output = "error: " + err.Error()
		}
		select {
		case ch <- core.StreamEvent{
			Type:     core.EventAgentFinish,
			Name:     cloneName,
			Content:  output,
			Usage:    result.Usage,
			Duration: elapsed,
			IsError:  err != nil,
		}:
		case <-ctx.Done():
		}
	}

	if err != nil {
		a.Logger().Error("self-clone failed", "agent", a.Name(), "clone", cloneName, "error", err, "duration", elapsed)
		return DispatchResult{Content: "error: " + err.Error(), IsError: true}
	}
	a.Logger().Info("self-clone completed", "agent", a.Name(), "clone", cloneName,
		"duration", elapsed,
		"input_tokens", result.Usage.InputTokens,
		"output_tokens", result.Usage.OutputTokens)
	return DispatchResult{Content: result.Output, Usage: result.Usage, Attachments: result.Attachments}
}

// newCloneAgent builds the ephemeral copy: the parent's Config minus memory
// (no thread history load, no double-persist), minus the input handler (a
// parallel copy must not stall on human questions), and minus self-cloning
// itself (no recursive spawning). runtime.Init copies the Config by value,
// so the parent's live Config is never mutated.
func newCloneAgent(name, description string, provider core.Provider, cfg *Config) *LLMAgent {
	cloneCfg := *cfg
	cloneCfg.SelfCloneMax = 0
	cloneCfg.SelfCloneTimeout = 0
	cloneCfg.InputHandler = nil
	cloneCfg.MemoryConfig = memory.AgentMemoryConfig{}
	cloneCfg.MemoryInitialized = false
	clone := &LLMAgent{}
	runtime.Init(&clone.Runtime, name, description, provider, &cloneCfg)
	return clone
}
