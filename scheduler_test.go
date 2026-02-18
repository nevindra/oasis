package oasis

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// --- schedulerStore: minimal Store stub for Scheduler tests ---

// schedulerStore implements Store with configurable behavior for the methods
// used by Scheduler. All other Store methods are no-ops.
type schedulerStore struct {
	nopStore
	dueActions    []ScheduledAction
	dueErr        error
	updateErr     error
	disableErr    error
	updatedAction *ScheduledAction
	disabledID    string
	disabledValue bool
}

func (s *schedulerStore) GetDueScheduledActions(_ context.Context, _ int64) ([]ScheduledAction, error) {
	return s.dueActions, s.dueErr
}

func (s *schedulerStore) UpdateScheduledAction(_ context.Context, action ScheduledAction) error {
	s.updatedAction = &action
	return s.updateErr
}

func (s *schedulerStore) UpdateScheduledActionEnabled(_ context.Context, id string, enabled bool) error {
	s.disabledID = id
	s.disabledValue = enabled
	return s.disableErr
}

// --- Tests ---

func TestSchedulerRunsAgent(t *testing.T) {
	action := ScheduledAction{
		ID:          "1",
		Description: "daily briefing",
		Schedule:    "08:00 daily",
		Enabled:     true,
	}

	store := &schedulerStore{dueActions: []ScheduledAction{action}}
	var callCount int32
	agent := &schedulerMockAgent{
		name: "test",
		executeFunc: func(_ context.Context, task AgentTask) (AgentResult, error) {
			atomic.AddInt32(&callCount, 1)
			if task.Input != action.Description {
				t.Errorf("task.Input = %q, want %q", task.Input, action.Description)
			}
			if task.Context["scheduled_action_id"] != action.ID {
				t.Errorf("scheduled_action_id = %v, want %q", task.Context["scheduled_action_id"], action.ID)
			}
			return AgentResult{Output: "briefing done"}, nil
		},
	}

	var hookAction ScheduledAction
	var hookResult AgentResult
	var hookErr error
	scheduler := NewScheduler(store, agent,
		WithSchedulerInterval(time.Hour), // long interval — we drive manually
		WithOnRun(func(a ScheduledAction, r AgentResult, e error) {
			hookAction = a
			hookResult = r
			hookErr = e
		}),
	)

	scheduler.tick(context.Background())

	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("agent Execute called %d times, want 1", callCount)
	}
	if hookAction.ID != action.ID {
		t.Errorf("hook action ID = %q, want %q", hookAction.ID, action.ID)
	}
	if hookResult.Output != "briefing done" {
		t.Errorf("hook result output = %q, want %q", hookResult.Output, "briefing done")
	}
	if hookErr != nil {
		t.Errorf("hook error = %v, want nil", hookErr)
	}
}

func TestSchedulerUpdatesNextRunBeforeExecution(t *testing.T) {
	action := ScheduledAction{
		ID:       "1",
		Schedule: "08:00 daily",
		Enabled:  true,
	}
	store := &schedulerStore{dueActions: []ScheduledAction{action}}

	var executionOrder []string
	agent := &schedulerMockAgent{
		name: "test",
		executeFunc: func(_ context.Context, _ AgentTask) (AgentResult, error) {
			executionOrder = append(executionOrder, "execute")
			return AgentResult{}, nil
		},
	}

	// Intercept UpdateScheduledAction to track call order.
	origUpdate := store.updateErr // zero, but we track via a wrapper
	_ = origUpdate

	var updateCalledBeforeExecute bool
	trackingStore := &trackingSchedulerStore{
		schedulerStore: store,
		onUpdate: func() {
			if len(executionOrder) == 0 {
				updateCalledBeforeExecute = true
			}
			executionOrder = append(executionOrder, "update")
		},
	}

	scheduler := NewScheduler(trackingStore, agent, WithSchedulerInterval(time.Hour))
	scheduler.tick(context.Background())

	if !updateCalledBeforeExecute {
		t.Error("NextRun should be updated before agent execution")
	}
	if len(executionOrder) < 2 || executionOrder[0] != "update" || executionOrder[1] != "execute" {
		t.Errorf("execution order = %v, want [update execute]", executionOrder)
	}
}

// trackingSchedulerStore wraps schedulerStore and calls onUpdate before delegating.
type trackingSchedulerStore struct {
	*schedulerStore
	onUpdate func()
}

func (s *trackingSchedulerStore) UpdateScheduledAction(ctx context.Context, action ScheduledAction) error {
	s.onUpdate()
	return s.schedulerStore.UpdateScheduledAction(ctx, action)
}

func TestSchedulerDisablesOnceAction(t *testing.T) {
	action := ScheduledAction{
		ID:       "once-1",
		Schedule: "09:00 once",
		Enabled:  true,
	}
	store := &schedulerStore{dueActions: []ScheduledAction{action}}
	agent := &mockAgent{name: "test", result: AgentResult{}}

	scheduler := NewScheduler(store, agent, WithSchedulerInterval(time.Hour))
	scheduler.tick(context.Background())

	if store.disabledID != action.ID {
		t.Errorf("disabled ID = %q, want %q", store.disabledID, action.ID)
	}
	if store.disabledValue != false {
		t.Error("once action should be disabled (enabled=false)")
	}
}

func TestSchedulerDoesNotDisableDailyAction(t *testing.T) {
	action := ScheduledAction{
		ID:       "daily-1",
		Schedule: "09:00 daily",
		Enabled:  true,
	}
	store := &schedulerStore{dueActions: []ScheduledAction{action}}
	agent := &mockAgent{name: "test", result: AgentResult{}}

	scheduler := NewScheduler(store, agent, WithSchedulerInterval(time.Hour))
	scheduler.tick(context.Background())

	if store.disabledID != "" {
		t.Errorf("daily action should not be disabled, got disabledID = %q", store.disabledID)
	}
}

func TestSchedulerAgentErrorPassedToHook(t *testing.T) {
	wantErr := errors.New("agent failed")
	action := ScheduledAction{
		ID:       "1",
		Schedule: "08:00 daily",
		Enabled:  true,
	}
	store := &schedulerStore{dueActions: []ScheduledAction{action}}
	agent := &mockAgent{name: "test", err: wantErr}

	var gotErr error
	var callCount int32
	scheduler := NewScheduler(store, agent,
		WithSchedulerInterval(time.Hour),
		WithOnRun(func(_ ScheduledAction, _ AgentResult, e error) {
			atomic.AddInt32(&callCount, 1)
			gotErr = e
		}),
	)
	scheduler.tick(context.Background())

	if !errors.Is(gotErr, wantErr) {
		t.Errorf("hook error = %v, want %v", gotErr, wantErr)
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("hook called %d times, want 1", callCount)
	}
}

func TestSchedulerContinuesAfterOneActionFails(t *testing.T) {
	wantErr := errors.New("action 1 failed")
	actions := []ScheduledAction{
		{ID: "1", Schedule: "08:00 daily", Description: "first"},
		{ID: "2", Schedule: "09:00 daily", Description: "second"},
	}
	store := &schedulerStore{dueActions: actions}

	callOrder := make([]string, 0, 2)
	agent := &schedulerMockAgent{
		name: "test",
		executeFunc: func(_ context.Context, task AgentTask) (AgentResult, error) {
			callOrder = append(callOrder, task.Input)
			if task.Input == "first" {
				return AgentResult{}, wantErr
			}
			return AgentResult{Output: "ok"}, nil
		},
	}

	var hookCalls int32
	scheduler := NewScheduler(store, agent,
		WithSchedulerInterval(time.Hour),
		WithOnRun(func(_ ScheduledAction, _ AgentResult, _ error) {
			atomic.AddInt32(&hookCalls, 1)
		}),
	)
	scheduler.tick(context.Background())

	if len(callOrder) != 2 {
		t.Errorf("agent called %d times, want 2 (should continue after error)", len(callOrder))
	}
	if atomic.LoadInt32(&hookCalls) != 2 {
		t.Errorf("hook called %d times, want 2", hookCalls)
	}
}

func TestSchedulerSkipsTickOnStoreError(t *testing.T) {
	storeErr := errors.New("db error")
	store := &schedulerStore{dueErr: storeErr}
	var callCount int32
	agent := &schedulerMockAgent{
		name: "test",
		executeFunc: func(_ context.Context, _ AgentTask) (AgentResult, error) {
			atomic.AddInt32(&callCount, 1)
			return AgentResult{}, nil
		},
	}

	scheduler := NewScheduler(store, agent, WithSchedulerInterval(time.Hour))
	scheduler.tick(context.Background())

	if atomic.LoadInt32(&callCount) != 0 {
		t.Error("agent should not be called when store returns error")
	}
}

func TestSchedulerStoreUpdateErrorDoesNotPreventExecution(t *testing.T) {
	action := ScheduledAction{
		ID:       "1",
		Schedule: "08:00 daily",
		Enabled:  true,
	}
	store := &schedulerStore{
		dueActions: []ScheduledAction{action},
		updateErr:  errors.New("update failed"),
	}
	var callCount int32
	agent := &schedulerMockAgent{
		name: "test",
		executeFunc: func(_ context.Context, _ AgentTask) (AgentResult, error) {
			atomic.AddInt32(&callCount, 1)
			return AgentResult{Output: "ok"}, nil
		},
	}

	scheduler := NewScheduler(store, agent, WithSchedulerInterval(time.Hour))
	scheduler.tick(context.Background())

	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("agent called %d times, want 1 (update error should not prevent execution)", callCount)
	}
}

func TestSchedulerStartShutdown(t *testing.T) {
	store := &schedulerStore{} // no due actions
	agent := &mockAgent{name: "test"}

	scheduler := NewScheduler(store, agent, WithSchedulerInterval(10*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := scheduler.Start(ctx)
	if err != nil {
		t.Errorf("Start returned error on clean shutdown: %v", err)
	}
}

func TestSchedulerDefaultInterval(t *testing.T) {
	store := &schedulerStore{}
	agent := &mockAgent{name: "test"}
	s := NewScheduler(store, agent)
	if s.interval != time.Minute {
		t.Errorf("default interval = %v, want %v", s.interval, time.Minute)
	}
}

func TestSchedulerTZOffset(t *testing.T) {
	store := &schedulerStore{}
	agent := &mockAgent{name: "test"}
	s := NewScheduler(store, agent, WithSchedulerTZOffset(7))
	if s.tzOffset != 7 {
		t.Errorf("tzOffset = %d, want 7", s.tzOffset)
	}
}

func TestScheduleIsOnce(t *testing.T) {
	tests := []struct {
		schedule string
		want     bool
	}{
		{"08:00 once", true},
		{"08:00 daily", false},
		{"08:00 weekly(monday)", false},
		{"08:00 custom(mon,wed)", false},
		{"08:00 monthly(15)", false},
		{"invalid", false},
	}
	for _, tt := range tests {
		got := scheduleIsOnce(tt.schedule)
		if got != tt.want {
			t.Errorf("scheduleIsOnce(%q) = %v, want %v", tt.schedule, got, tt.want)
		}
	}
}

// schedulerMockAgent is a test Agent with a configurable execute function,
// used when test-specific behavior is needed beyond mockAgent's fixed result/err.
type schedulerMockAgent struct {
	name        string
	executeFunc func(context.Context, AgentTask) (AgentResult, error)
}

func (m *schedulerMockAgent) Name() string        { return m.name }
func (m *schedulerMockAgent) Description() string { return "" }
func (m *schedulerMockAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return m.executeFunc(ctx, task)
}
