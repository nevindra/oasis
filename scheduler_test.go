package oasis

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// --- ComputeNextRun tests ---

func TestComputeNextRunDaily(t *testing.T) {
	// 2026-02-17 10:00 UTC (unix: 1771322400) — it's afternoon in WIB (+7)
	now := int64(1771322400)
	tz := 7

	next, ok := ComputeNextRun("08:00 daily", now, tz)
	if !ok {
		t.Fatal("expected ok")
	}
	if next <= now {
		t.Error("next run should be after now")
	}
	// 08:00 WIB = 01:00 UTC. Since now is 10:00 UTC (17:00 WIB),
	// 08:00 WIB has already passed today -> should be tomorrow.
	// Tomorrow 08:00 WIB = 2026-02-18 01:00 UTC
	expected := int64(1771376400)
	diff := next - expected
	if diff < -60 || diff > 60 {
		t.Errorf("next run off by %d seconds (got %d, expected ~%d)", diff, next, expected)
	}
}

func TestComputeNextRunOnce(t *testing.T) {
	now := int64(1771322400)
	next, ok := ComputeNextRun("08:00 once", now, 7)
	if !ok {
		t.Fatal("expected ok")
	}
	if next <= now {
		t.Error("once should still schedule next run")
	}
}

func TestComputeNextRunWeekly(t *testing.T) {
	now := int64(1771322400) // Tuesday 2026-02-17
	next, ok := ComputeNextRun("09:00 weekly(friday)", now, 7)
	if !ok {
		t.Fatal("expected ok")
	}
	if next <= now {
		t.Error("next run should be after now")
	}
}

func TestComputeNextRunWeeklyIndonesian(t *testing.T) {
	now := int64(1771322400)
	next, ok := ComputeNextRun("09:00 weekly(jumat)", now, 7)
	if !ok {
		t.Fatal("expected ok for Indonesian day name")
	}
	if next <= now {
		t.Error("next run should be after now")
	}
}

func TestComputeNextRunCustom(t *testing.T) {
	now := int64(1771322400)
	next, ok := ComputeNextRun("10:00 custom(senin,rabu,jumat)", now, 7)
	if !ok {
		t.Fatal("expected ok")
	}
	if next <= now {
		t.Error("next run should be after now")
	}
}

func TestComputeNextRunMonthly(t *testing.T) {
	now := int64(1771322400) // Feb 17
	next, ok := ComputeNextRun("08:00 monthly(20)", now, 7)
	if !ok {
		t.Fatal("expected ok")
	}
	if next <= now {
		t.Error("next run should be after now")
	}
}

func TestComputeNextRunInvalid(t *testing.T) {
	_, ok := ComputeNextRun("invalid", 0, 0)
	if ok {
		t.Error("expected not ok for invalid format")
	}

	_, ok = ComputeNextRun("25:00 daily", 0, 0)
	if ok {
		t.Error("expected not ok for invalid hour")
	}
}

func TestDayNameToDOW(t *testing.T) {
	cases := []struct {
		name string
		want int64
	}{
		{"monday", 0}, {"senin", 0},
		{"tuesday", 1}, {"selasa", 1},
		{"wednesday", 2}, {"rabu", 2},
		{"thursday", 3}, {"kamis", 3},
		{"friday", 4}, {"jumat", 4},
		{"saturday", 5}, {"sabtu", 5},
		{"sunday", 6}, {"minggu", 6},
	}
	for _, c := range cases {
		got, ok := dayNameToDOW(c.name)
		if !ok {
			t.Errorf("dayNameToDOW(%q) not ok", c.name)
		}
		if got != c.want {
			t.Errorf("dayNameToDOW(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestUnixDaysToDateAndBack(t *testing.T) {
	// 2026-02-17 = 20501 days from epoch
	days := dateToUnixDays(2026, 2, 17)
	y, m, d := unixDaysToDate(days)
	if y != 2026 || m != 2 || d != 17 {
		t.Errorf("roundtrip failed: %d-%d-%d", y, m, d)
	}
}

func TestFormatLocalTime(t *testing.T) {
	// 2026-02-17 01:00 UTC -> 08:00 WIB (+7)
	unix := int64(1771290000) // 2026-02-17 01:00 UTC
	got := FormatLocalTime(unix, 7)
	if got != "2026-02-17 08:00" {
		t.Errorf("FormatLocalTime(+7) = %q, want %q", got, "2026-02-17 08:00")
	}
}

func TestFormatLocalTimeNegativeOffset(t *testing.T) {
	// 2026-02-17 15:00 UTC -> 10:00 EST (-5)
	unix := int64(1771340400) // 2026-02-17 15:00 UTC
	got := FormatLocalTime(unix, -5)
	if got != "2026-02-17 10:00" {
		t.Errorf("FormatLocalTime(-5) = %q, want %q", got, "2026-02-17 10:00")
	}
}

func TestFormatLocalTimeUTC(t *testing.T) {
	// 2026-02-17 12:30 UTC -> same in UTC (offset=0)
	unix := int64(1771331400) // 2026-02-17 12:30 UTC
	got := FormatLocalTime(unix, 0)
	if got != "2026-02-17 12:30" {
		t.Errorf("FormatLocalTime(0) = %q, want %q", got, "2026-02-17 12:30")
	}
}

func TestComputeNextRunNegativeTimezone(t *testing.T) {
	// 2026-02-17 15:00 UTC = 10:00 EST (-5)
	now := int64(1771340400)
	next, ok := ComputeNextRun("08:00 daily", now, -5)
	if !ok {
		t.Fatal("expected ok for negative timezone")
	}
	if next <= now {
		t.Error("next run should be after now")
	}
	// 08:00 EST = 13:00 UTC. Since 10:00 EST > 08:00 EST, should be tomorrow.
	// Tomorrow 08:00 EST = 2026-02-18 13:00 UTC
	expected := int64(1771419600)
	diff := next - expected
	if diff < -60 || diff > 60 {
		t.Errorf("negative tz: off by %d seconds (got %d, expected ~%d)", diff, next, expected)
	}
}

func TestComputeNextRunMonthlyPastDay(t *testing.T) {
	// Feb 17, monthly(15) -> 15 already passed, should go to March 15
	now := int64(1771322400) // 2026-02-17 10:00 UTC
	next, ok := ComputeNextRun("08:00 monthly(15)", now, 7)
	if !ok {
		t.Fatal("expected ok")
	}
	if next <= now {
		t.Error("next run should be after now")
	}
	// March 15 08:00 WIB = March 15 01:00 UTC
	y, m, d := unixDaysToDate((next + 7*3600) / 86400)
	if m != 3 || d != 15 {
		t.Errorf("expected March 15, got %d-%02d-%02d", y, m, d)
	}
}

func TestComputeNextRunMonthlyDecToJan(t *testing.T) {
	// Dec 20, monthly(15) -> should wrap to Jan 15 next year
	// 2026-12-20 10:00 UTC
	decDays := dateToUnixDays(2026, 12, 20)
	now := decDays*86400 + 10*3600
	next, ok := ComputeNextRun("08:00 monthly(15)", now, 0)
	if !ok {
		t.Fatal("expected ok")
	}
	y, m, d := unixDaysToDate(next / 86400)
	if y != 2027 || m != 1 || d != 15 {
		t.Errorf("expected 2027-01-15, got %d-%02d-%02d", y, m, d)
	}
}

func TestComputeNextRunWeeklyInvalidDay(t *testing.T) {
	now := int64(1771322400)
	_, ok := ComputeNextRun("09:00 weekly(notaday)", now, 7)
	if ok {
		t.Error("expected not ok for invalid day name")
	}
}

func TestComputeNextRunCustomInvalidDay(t *testing.T) {
	now := int64(1771322400)
	_, ok := ComputeNextRun("09:00 custom(mon,badday)", now, 7)
	if ok {
		t.Error("expected not ok for invalid day in custom")
	}
}

func TestComputeNextRunMonthlyInvalidDOM(t *testing.T) {
	now := int64(1771322400)
	_, ok := ComputeNextRun("08:00 monthly(0)", now, 7)
	if ok {
		t.Error("expected not ok for day-of-month 0")
	}
	_, ok = ComputeNextRun("08:00 monthly(32)", now, 7)
	if ok {
		t.Error("expected not ok for day-of-month 32")
	}
}

func TestComputeNextRunInvalidMinute(t *testing.T) {
	_, ok := ComputeNextRun("12:60 daily", 0, 0)
	if ok {
		t.Error("expected not ok for minute=60")
	}
}

func TestComputeNextRunUnknownRecurrence(t *testing.T) {
	_, ok := ComputeNextRun("08:00 biweekly", 0, 7)
	if ok {
		t.Error("expected not ok for unknown recurrence")
	}
}

func TestDayNameToDOWInvalid(t *testing.T) {
	_, ok := dayNameToDOW("notaday")
	if ok {
		t.Error("expected not ok for invalid day name")
	}
}

func TestSchedParseInt(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"0", 0},
		{"15", 15},
		{"99", 99},
		{"", 0},     // empty string -> 0 (loop doesn't execute)
		{"abc", -1}, // non-digit
		{"1a2", -1}, // mixed
	}
	for _, tt := range tests {
		got := schedParseInt(tt.input)
		if got != tt.want {
			t.Errorf("schedParseInt(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestComputeNextRunDailyBeforeTime(t *testing.T) {
	// If it's before the target time today, schedule for today
	// 2026-02-17 00:00 UTC = 07:00 WIB, target 08:00 WIB -> today
	now := int64(1771286400) // 2026-02-17 00:00 UTC
	next, ok := ComputeNextRun("08:00 daily", now, 7)
	if !ok {
		t.Fatal("expected ok")
	}
	// 08:00 WIB = 01:00 UTC on same day
	expected := int64(1771290000)
	diff := next - expected
	if diff < -60 || diff > 60 {
		t.Errorf("before-time: off by %d seconds (got %d, expected ~%d)", diff, next, expected)
	}
}

func TestUnixDaysToDateEpoch(t *testing.T) {
	y, m, d := unixDaysToDate(0)
	if y != 1970 || m != 1 || d != 1 {
		t.Errorf("epoch: got %d-%02d-%02d, want 1970-01-01", y, m, d)
	}
}

func TestDateToUnixDaysAndBackMultiple(t *testing.T) {
	dates := [][3]int{
		{1970, 1, 1},
		{2000, 2, 29}, // leap year
		{2024, 12, 31},
		{2026, 6, 15},
	}
	for _, dt := range dates {
		days := dateToUnixDays(dt[0], dt[1], dt[2])
		y, m, d := unixDaysToDate(days)
		if y != dt[0] || m != dt[1] || d != dt[2] {
			t.Errorf("roundtrip %v: got %d-%02d-%02d", dt, y, m, d)
		}
	}
}

// --- Scheduler runtime tests ---

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
