package schedule

import (
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
