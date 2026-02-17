package schedule

import (
	"testing"
)

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
	expected := int64(1771376400) // approximate
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

func TestUnixDaysToDateAndBack(t *testing.T) {
	// 2026-02-17 = 20501 days from epoch
	days := dateToUnixDays(2026, 2, 17)
	y, m, d := unixDaysToDate(days)
	if y != 2026 || m != 2 || d != 17 {
		t.Errorf("roundtrip failed: %d-%d-%d", y, m, d)
	}
}
