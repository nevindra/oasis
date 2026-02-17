package schedule

import (
	"context"
	"testing"
)

func TestBuildScheduleString(t *testing.T) {
	if s := buildScheduleString("14:30", "daily", ""); s != "14:30 daily" {
		t.Errorf("got %q", s)
	}
	if s := buildScheduleString("08:00", "once", ""); s != "08:00 once" {
		t.Errorf("got %q", s)
	}
	if s := buildScheduleString("09:00", "weekly", "friday"); s != "09:00 weekly(friday)" {
		t.Errorf("got %q", s)
	}
	if s := buildScheduleString("10:00", "custom", "Mon, Wed, Fri"); s != "10:00 custom(mon,wed,fri)" {
		t.Errorf("got %q", s)
	}
}

func TestBuildScheduleStringEmptyTime(t *testing.T) {
	// Empty time should default to "08:00"
	s := buildScheduleString("", "daily", "")
	if s != "08:00 daily" {
		t.Errorf("expected '08:00 daily', got %q", s)
	}
}

func TestBuildRecurrencePart(t *testing.T) {
	tests := []struct {
		recurrence string
		day        string
		want       string
	}{
		{"once", "", "once"},
		{"daily", "", "daily"},
		{"weekly", "friday", "weekly(friday)"},
		{"weekly", "", "weekly(monday)"},        // default day
		{"monthly", "15", "monthly(15)"},
		{"monthly", "", "monthly(1)"},            // default day
		{"custom", "Mon,Wed,Fri", "custom(mon,wed,fri)"},
		{"custom", "", "custom(monday,wednesday,friday)"}, // default
		{"unknown", "", "daily"},                 // unknown defaults to daily
	}
	for _, tt := range tests {
		got := buildRecurrencePart(tt.recurrence, tt.day)
		if got != tt.want {
			t.Errorf("buildRecurrencePart(%q, %q) = %q, want %q",
				tt.recurrence, tt.day, got, tt.want)
		}
	}
}

func TestNormalizeDayList(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Mon, Wed, Fri", "mon,wed,fri"},
		{"monday", "monday"},
		{" TUESDAY , thursday ", "tuesday,thursday"},
		{"Sun", "sun"},
	}
	for _, tt := range tests {
		got := normalizeDayList(tt.input)
		if got != tt.want {
			t.Errorf("normalizeDayList(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestScheduleDefinitions(t *testing.T) {
	tool := New(nil, 7)
	defs := tool.Definitions()
	if len(defs) != 4 {
		t.Fatalf("expected 4 definitions, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"schedule_create", "schedule_list", "schedule_update", "schedule_delete"} {
		if !names[want] {
			t.Errorf("missing definition %q", want)
		}
	}
}

func TestScheduleUnknownToolName(t *testing.T) {
	tool := New(nil, 7)
	result, err := tool.Execute(context.Background(), "schedule_nonexistent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Error("expected error for unknown tool name")
	}
}
