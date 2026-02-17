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
	nextRun, ok := oasis.ComputeNextRun(schedule, now, t.tzOffset)
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
		p.Description, schedule, oasis.FormatLocalTime(nextRun, t.tzOffset)), nil
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
			oasis.FormatLocalTime(a.NextRun, t.tzOffset))
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
		nextRun, ok := oasis.ComputeNextRun(newSchedule, now, t.tzOffset)
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

// --- Schedule string builders (tool-specific) ---

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

func normalizeDayList(input string) string {
	parts := strings.Split(input, ",")
	for i, p := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(p))
	}
	return strings.Join(parts, ",")
}
