package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// Tool manages scheduled/recurring actions.
type Tool struct {
	store    oasis.Store
	tzOffset int // hours from UTC (e.g. 7 for WIB)
}

// New creates a ScheduleTool.
func New(store oasis.Store, tzOffset int) *Tool {
	return &Tool{store: store, tzOffset: tzOffset}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{
		{
			Name:        "schedule_create",
			Description: "Create a scheduled/recurring action that runs automatically. Use when the user wants something done periodically (daily briefings, recurring searches, regular summaries).",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"description":{"type":"string","description":"Human-readable description of what this scheduled action does"},
				"time":{"type":"string","description":"Time in HH:MM format (24-hour, user's local timezone)"},
				"recurrence":{"type":"string","enum":["once","daily","custom","weekly","monthly"],"description":"How often to run"},
				"day":{"type":"string","description":"For weekly: day name. For custom: comma-separated day names. For monthly: day number (1-31)."},
				"tools":{"type":"array","items":{"type":"object","properties":{"tool":{"type":"string"},"params":{"type":"object"}},"required":["tool","params"]},"description":"Tools to execute when the schedule fires"},
				"synthesis_prompt":{"type":"string","description":"How to format/summarize results"}
			},"required":["description","time","recurrence","tools"]}`),
		},
		{
			Name:        "schedule_list",
			Description: "List all scheduled actions with their schedules, status, and next run time.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "schedule_update",
			Description: "Update a scheduled action: enable/disable it or change its schedule.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"description_query":{"type":"string","description":"Substring to match the scheduled action description"},
				"enabled":{"type":"boolean","description":"Set to true to enable, false to disable/pause"},
				"time":{"type":"string","description":"New time in HH:MM format (optional)"},
				"recurrence":{"type":"string","enum":["once","daily","custom","weekly","monthly"],"description":"New recurrence (optional)"},
				"day":{"type":"string","description":"New day(s) (optional)"}
			},"required":["description_query"]}`),
		},
		{
			Name:        "schedule_delete",
			Description: "Delete a scheduled action. Matches by description substring, or '*' to delete all.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"description_query":{"type":"string","description":"Substring to match the description, or '*' for all"}
			},"required":["description_query"]}`),
		},
	}
}

func (t *Tool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
	var result string
	var err error

	switch name {
	case "schedule_create":
		result, err = t.handleCreate(ctx, args)
	case "schedule_list":
		result, err = t.handleList(ctx)
	case "schedule_update":
		result, err = t.handleUpdate(ctx, args)
	case "schedule_delete":
		result, err = t.handleDelete(ctx, args)
	default:
		return oasis.ToolResult{Error: "unknown schedule tool: " + name}, nil
	}

	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}
	return oasis.ToolResult{Content: result}, nil
}

func (t *Tool) handleCreate(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Description     string          `json:"description"`
		Time            string          `json:"time"`
		Recurrence      string          `json:"recurrence"`
		Day             string          `json:"day"`
		Tools           json.RawMessage `json:"tools"`
		SynthesisPrompt string          `json:"synthesis_prompt"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}

	schedule := buildScheduleString(p.Time, p.Recurrence, p.Day)
	now := oasis.NowUnix()
	nextRun, ok := ComputeNextRun(schedule, now, t.tzOffset)
	if !ok {
		return "", fmt.Errorf("invalid schedule format: %s", schedule)
	}

	action := oasis.ScheduledAction{
		ID:              oasis.NewID(),
		Description:     p.Description,
		Schedule:        schedule,
		ToolCalls:       string(p.Tools),
		SynthesisPrompt: p.SynthesisPrompt,
		NextRun:         nextRun,
		Enabled:         true,
		CreatedAt:       now,
	}

	if err := t.store.CreateScheduledAction(ctx, action); err != nil {
		return "", err
	}

	return fmt.Sprintf("Scheduled: %s\nSchedule: %s\nNext run: %s",
		p.Description, schedule, formatLocalTime(nextRun, t.tzOffset)), nil
}

func (t *Tool) handleList(ctx context.Context) (string, error) {
	actions, err := t.store.ListScheduledActions(ctx)
	if err != nil {
		return "", err
	}
	if len(actions) == 0 {
		return "No scheduled actions.", nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%d scheduled action(s):\n\n", len(actions))
	for i, a := range actions {
		status := "active"
		if !a.Enabled {
			status = "paused"
		}
		fmt.Fprintf(&out, "%d. %s [%s]\n   Schedule: %s | Next: %s\n",
			i+1, a.Description, status, a.Schedule,
			formatLocalTime(a.NextRun, t.tzOffset))
	}
	return out.String(), nil
}

func (t *Tool) handleUpdate(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		DescriptionQuery string  `json:"description_query"`
		Enabled          *bool   `json:"enabled"`
		Time             *string `json:"time"`
		Recurrence       *string `json:"recurrence"`
		Day              *string `json:"day"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}

	matches, err := t.store.FindScheduledActionsByDescription(ctx, p.DescriptionQuery)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return fmt.Sprintf("No scheduled action matching %q.", p.DescriptionQuery), nil
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, a := range matches {
			names[i] = a.Description
		}
		return fmt.Sprintf("Multiple matches: %s. Be more specific.", strings.Join(names, ", ")), nil
	}

	action := matches[0]
	var changes []string

	if p.Enabled != nil {
		if err := t.store.UpdateScheduledActionEnabled(ctx, action.ID, *p.Enabled); err != nil {
			return "", err
		}
		if *p.Enabled {
			changes = append(changes, "enabled")
		} else {
			changes = append(changes, "paused")
		}
	}

	if p.Time != nil || p.Recurrence != nil {
		// Parse current schedule
		parts := strings.SplitN(action.Schedule, " ", 2)
		currentTime := "08:00"
		currentRec := "daily"
		if len(parts) >= 1 {
			currentTime = parts[0]
		}
		if len(parts) >= 2 {
			currentRec = parts[1]
		}

		newTime := currentTime
		if p.Time != nil {
			newTime = *p.Time
		}

		newRec := currentRec
		if p.Recurrence != nil {
			day := ""
			if p.Day != nil {
				day = *p.Day
			}
			newRec = buildRecurrencePart(*p.Recurrence, day)
		}

		newSchedule := newTime + " " + newRec
		now := oasis.NowUnix()
		nextRun, ok := ComputeNextRun(newSchedule, now, t.tzOffset)
		if !ok {
			return "", fmt.Errorf("invalid schedule: %s", newSchedule)
		}

		action.Schedule = newSchedule
		action.NextRun = nextRun
		if err := t.store.UpdateScheduledAction(ctx, action); err != nil {
			return "", err
		}
		changes = append(changes, "schedule updated")
	}

	if len(changes) == 0 {
		return "No changes specified.", nil
	}

	return fmt.Sprintf("Updated %q: %s", action.Description, strings.Join(changes, ", ")), nil
}

func (t *Tool) handleDelete(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		DescriptionQuery string `json:"description_query"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}

	if p.DescriptionQuery == "*" {
		count, err := t.store.DeleteAllScheduledActions(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Deleted all %d scheduled action(s).", count), nil
	}

	matches, err := t.store.FindScheduledActionsByDescription(ctx, p.DescriptionQuery)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return fmt.Sprintf("No scheduled action matching %q.", p.DescriptionQuery), nil
	}

	for _, a := range matches {
		if err := t.store.DeleteScheduledAction(ctx, a.ID); err != nil {
			return "", err
		}
	}

	if len(matches) == 1 {
		return fmt.Sprintf("Deleted: %s", matches[0].Description), nil
	}
	return fmt.Sprintf("Deleted %d scheduled action(s).", len(matches)), nil
}

// --- Schedule helpers ---

func buildScheduleString(timeStr, recurrence, day string) string {
	if timeStr == "" {
		timeStr = "08:00"
	}
	return timeStr + " " + buildRecurrencePart(recurrence, day)
}

func buildRecurrencePart(recurrence, day string) string {
	switch recurrence {
	case "once":
		return "once"
	case "custom":
		if day == "" {
			day = "monday,wednesday,friday"
		}
		return fmt.Sprintf("custom(%s)", normalizeDayList(day))
	case "weekly":
		if day == "" {
			day = "monday"
		}
		return fmt.Sprintf("weekly(%s)", strings.ToLower(strings.TrimSpace(day)))
	case "monthly":
		if day == "" {
			day = "1"
		}
		return fmt.Sprintf("monthly(%s)", day)
	default:
		return "daily"
	}
}

// ComputeNextRun calculates the next UTC timestamp for a schedule string.
// Schedule format: "HH:MM <recurrence>" where recurrence is:
// once, daily, custom(mon,wed,fri), weekly(monday), monthly(15)
func ComputeNextRun(schedule string, nowUnix int64, tzOffset int) (int64, bool) {
	parts := strings.SplitN(schedule, " ", 2)
	if len(parts) != 2 {
		return 0, false
	}

	timeParts := strings.Split(parts[0], ":")
	if len(timeParts) != 2 {
		return 0, false
	}
	hour := parseInt(timeParts[0])
	minute := parseInt(timeParts[1])
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
		targetDOM := parseInt(domStr)
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

func normalizeDayList(input string) string {
	parts := strings.Split(input, ",")
	for i, p := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(p))
	}
	return strings.Join(parts, ",")
}

func parseInt(s string) int {
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

func formatLocalTime(unix int64, tzOffset int) string {
	local := unix + int64(tzOffset)*3600
	days := local / 86400
	remainder := local % 86400
	hour := remainder / 3600
	minute := (remainder % 3600) / 60
	y, m, d := unixDaysToDate(days)
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d", y, m, d, hour, minute)
}

func unixDaysToDate(days int64) (year, month, day int) {
	// Algorithm from http://howardhinnant.github.io/date_algorithms.html
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
