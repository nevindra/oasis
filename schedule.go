package oasis

import (
	"fmt"
	"strings"
)

// ComputeNextRun calculates the next UTC unix timestamp for a schedule string.
//
// Schedule format is "HH:MM <recurrence>" where recurrence is one of:
//   - once       — fires once, then the action is disabled
//   - daily      — fires every day at the specified time
//   - custom(mon,wed,fri) — fires on specific days of the week
//   - weekly(monday)      — fires once a week on the given day
//   - monthly(15)         — fires once a month on the given day number
//
// The time component is in the user's local timezone. tzOffset is the
// offset from UTC in whole hours (e.g., 7 for WIB/Asia Jakarta, -5 for EST).
// The returned timestamp is always in UTC.
func ComputeNextRun(schedule string, nowUnix int64, tzOffset int) (int64, bool) {
	parts := strings.SplitN(schedule, " ", 2)
	if len(parts) != 2 {
		return 0, false
	}

	timeParts := strings.Split(parts[0], ":")
	if len(timeParts) != 2 {
		return 0, false
	}
	hour := schedParseInt(timeParts[0])
	minute := schedParseInt(timeParts[1])
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}

	offsetSecs := int64(tzOffset) * 3600
	localNow := nowUnix + offsetSecs
	localDays := localNow / 86400
	localTimeOfDay := localNow % 86400
	targetTimeOfDay := int64(hour)*3600 + int64(minute)*60

	recurrence := strings.TrimSpace(parts[1])

	switch {
	case recurrence == "once" || recurrence == "daily":
		targetDay := localDays
		if localTimeOfDay >= targetTimeOfDay {
			targetDay++
		}
		localTS := targetDay*86400 + targetTimeOfDay
		return localTS - offsetSecs, true

	case strings.HasPrefix(recurrence, "custom("):
		daysStr := strings.TrimPrefix(recurrence, "custom(")
		daysStr = strings.TrimSuffix(daysStr, ")")
		currentDOW := ((localDays % 7) + 3) % 7 // Monday=0

		var bestAhead int64 = -1
		for _, dayName := range strings.Split(daysStr, ",") {
			targetDOW, ok := dayNameToDOW(strings.TrimSpace(dayName))
			if !ok {
				return 0, false
			}
			ahead := targetDOW - currentDOW
			if ahead < 0 {
				ahead += 7
			}
			if ahead == 0 && localTimeOfDay >= targetTimeOfDay {
				ahead = 7
			}
			if bestAhead < 0 || ahead < bestAhead {
				bestAhead = ahead
			}
		}
		if bestAhead < 0 {
			return 0, false
		}
		targetDay := localDays + bestAhead
		localTS := targetDay*86400 + targetTimeOfDay
		return localTS - offsetSecs, true

	case strings.HasPrefix(recurrence, "weekly("):
		dayName := strings.TrimPrefix(recurrence, "weekly(")
		dayName = strings.TrimSuffix(dayName, ")")
		targetDOW, ok := dayNameToDOW(dayName)
		if !ok {
			return 0, false
		}
		currentDOW := ((localDays % 7) + 3) % 7
		daysAhead := targetDOW - currentDOW
		if daysAhead < 0 {
			daysAhead += 7
		}
		if daysAhead == 0 && localTimeOfDay >= targetTimeOfDay {
			daysAhead = 7
		}
		targetDay := localDays + daysAhead
		localTS := targetDay*86400 + targetTimeOfDay
		return localTS - offsetSecs, true

	case strings.HasPrefix(recurrence, "monthly("):
		domStr := strings.TrimPrefix(recurrence, "monthly(")
		domStr = strings.TrimSuffix(domStr, ")")
		targetDOM := schedParseInt(domStr)
		if targetDOM < 1 || targetDOM > 31 {
			return 0, false
		}
		y, m, d := unixDaysToDate(localDays)
		targetY, targetM := y, m
		if int64(d) > int64(targetDOM) || (int64(d) == int64(targetDOM) && localTimeOfDay >= targetTimeOfDay) {
			if m == 12 {
				targetY = y + 1
				targetM = 1
			} else {
				targetM = m + 1
			}
		}
		targetDays := dateToUnixDays(targetY, targetM, targetDOM)
		localTS := targetDays*86400 + targetTimeOfDay
		return localTS - offsetSecs, true
	}

	return 0, false
}

// FormatLocalTime formats a UTC unix timestamp as "YYYY-MM-DD HH:MM"
// in the timezone specified by tzOffset (hours from UTC).
func FormatLocalTime(unix int64, tzOffset int) string {
	local := unix + int64(tzOffset)*3600
	days := local / 86400
	remainder := local % 86400
	hour := remainder / 3600
	minute := (remainder % 3600) / 60
	y, m, d := unixDaysToDate(days)
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d", y, m, d, hour, minute)
}

// --- schedule helpers (unexported) ---

// dayNameToDOW maps a day name (English or Indonesian) to day-of-week (Monday=0).
func dayNameToDOW(name string) (int64, bool) {
	switch strings.ToLower(name) {
	case "monday", "mon", "senin":
		return 0, true
	case "tuesday", "tue", "selasa":
		return 1, true
	case "wednesday", "wed", "rabu":
		return 2, true
	case "thursday", "thu", "kamis":
		return 3, true
	case "friday", "fri", "jumat":
		return 4, true
	case "saturday", "sat", "sabtu":
		return 5, true
	case "sunday", "sun", "minggu":
		return 6, true
	}
	return 0, false
}

// schedParseInt parses a non-negative integer from a string.
// Returns -1 if the string contains non-digit characters or is empty.
func schedParseInt(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else {
			return -1
		}
	}
	return n
}

// unixDaysToDate converts days since Unix epoch to year/month/day.
// Algorithm from http://howardhinnant.github.io/date_algorithms.html
func unixDaysToDate(days int64) (year, month, day int) {
	z := days + 719468
	era := z / 146097
	if z < 0 {
		era = (z - 146096) / 146097
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if mp >= 10 {
		m = mp - 9
	}
	if m <= 2 {
		y++
	}
	return int(y), int(m), int(d)
}

// dateToUnixDays converts year/month/day to days since Unix epoch.
// Inverse of unixDaysToDate.
func dateToUnixDays(year, month, day int) int64 {
	y := int64(year)
	m := int64(month)
	d := int64(day)
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	var doy int64
	if m > 2 {
		doy = (153*(m-3)+2)/5 + d - 1
	} else {
		doy = (153*(m+9)+2)/5 + d - 1
	}
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}
