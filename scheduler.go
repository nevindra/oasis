package oasis

import (
	"context"
	"log"
	"strings"
	"time"
)

// RunHook is called after each scheduled action completes — success or failure.
// Use it to route output without coupling Scheduler to a specific destination.
type RunHook func(action ScheduledAction, result AgentResult, err error)

// schedulerConfig holds options accumulated by SchedulerOption calls.
type schedulerConfig struct {
	interval time.Duration
	tzOffset int
	onRun    RunHook
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*schedulerConfig)

// WithSchedulerInterval sets the polling interval. Default: 1 minute.
func WithSchedulerInterval(d time.Duration) SchedulerOption {
	return func(c *schedulerConfig) { c.interval = d }
}

// WithSchedulerTZOffset sets the UTC offset in hours for schedule computation. Default: 0 (UTC).
func WithSchedulerTZOffset(hours int) SchedulerOption {
	return func(c *schedulerConfig) { c.tzOffset = hours }
}

// WithOnRun registers a hook called after each action execution.
// Receives the action, agent result, and error (nil on success).
func WithOnRun(hook RunHook) SchedulerOption {
	return func(c *schedulerConfig) { c.onRun = hook }
}

// Scheduler polls the store for due actions and executes them via the configured Agent.
// It is a framework primitive: compose it with any Agent implementation.
//
// Usage:
//
//	scheduler := oasis.NewScheduler(store, agent,
//	    oasis.WithSchedulerInterval(time.Minute),
//	    oasis.WithSchedulerTZOffset(7),
//	    oasis.WithOnRun(func(action oasis.ScheduledAction, result oasis.AgentResult, err error) {
//	        if err != nil { log.Printf("failed: %v", err); return }
//	        frontend.Send(ctx, chatID, result.Output, nil)
//	    }),
//	)
//	g.Go(func() error { return scheduler.Start(ctx) })
type Scheduler struct {
	store    Store
	agent    Agent
	interval time.Duration
	tzOffset int
	onRun    RunHook
}

// NewScheduler creates a Scheduler.
// store is the source of ScheduledAction records.
// agent executes each due action (LLMAgent, Network, Workflow, or custom).
func NewScheduler(store Store, agent Agent, opts ...SchedulerOption) *Scheduler {
	cfg := schedulerConfig{
		interval: time.Minute,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Scheduler{
		store:    store,
		agent:    agent,
		interval: cfg.interval,
		tzOffset: cfg.tzOffset,
		onRun:    cfg.onRun,
	}
}

// Start begins the polling loop. Blocks until ctx is cancelled.
// Returns nil on clean shutdown.
func (s *Scheduler) Start(ctx context.Context) error {
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.interval):
		}
	}
}

// tick performs one poll cycle: fetch due actions and execute each sequentially.
func (s *Scheduler) tick(ctx context.Context) {
	now := time.Now().Unix()
	actions, err := s.store.GetDueScheduledActions(ctx, now)
	if err != nil {
		log.Printf(" [scheduler] error fetching due actions: %v", err)
		return
	}

	for _, action := range actions {
		if ctx.Err() != nil {
			return
		}
		s.run(ctx, action)
	}
}

// run executes a single scheduled action following the plan's execution order:
// 1. Update NextRun before executing (prevents re-fire if agent is slow).
// 2. Disable the action if it uses "once" recurrence.
// 3. Execute the agent.
// 4. Call the OnRun hook with the result and any error.
func (s *Scheduler) run(ctx context.Context, action ScheduledAction) {
	// 1. Compute and persist new NextRun first.
	nextRun, ok := ComputeNextRun(action.Schedule, time.Now().Unix(), s.tzOffset)
	if ok {
		action.NextRun = nextRun
		if err := s.store.UpdateScheduledAction(ctx, action); err != nil {
			log.Printf(" [scheduler] action %q: error updating next_run: %v", action.Description, err)
			// Continue — better to fire than to silently skip.
		}
	}

	// 2. Disable once-off actions after scheduling.
	if scheduleIsOnce(action.Schedule) {
		if err := s.store.UpdateScheduledActionEnabled(ctx, action.ID, false); err != nil {
			log.Printf(" [scheduler] action %q: error disabling once action: %v", action.Description, err)
		}
	}

	// 3. Execute the agent with the action as task input.
	task := AgentTask{
		Input: action.Description,
		Context: map[string]any{
			"scheduled_action_id":  action.ID,
			"scheduled_tool_calls": action.ToolCalls,
			"scheduled_synthesis":  action.SynthesisPrompt,
		},
	}
	result, err := s.agent.Execute(ctx, task)
	if err != nil {
		log.Printf(" [scheduler] action %q failed: %v", action.Description, err)
	}

	// 4. Notify hook — always called, even on error.
	if s.onRun != nil {
		s.onRun(action, result, err)
	}
}

// scheduleIsOnce reports whether a schedule string uses the "once" recurrence.
func scheduleIsOnce(schedule string) bool {
	parts := strings.SplitN(schedule, " ", 2)
	if len(parts) != 2 {
		return false
	}
	return strings.TrimSpace(parts[1]) == "once"
}
