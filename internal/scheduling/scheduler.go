package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/tools/schedule"
)

// Scheduler checks for and executes due scheduled actions.
type Scheduler struct {
	store     oasis.VectorStore
	tools     *oasis.ToolRegistry
	frontend  oasis.Frontend
	intentLLM oasis.Provider
	tzOffset  int
}

// New creates a Scheduler.
func New(store oasis.VectorStore, tools *oasis.ToolRegistry, frontend oasis.Frontend, intentLLM oasis.Provider, tzOffset int) *Scheduler {
	return &Scheduler{
		store:     store,
		tools:     tools,
		frontend:  frontend,
		intentLLM: intentLLM,
		tzOffset:  tzOffset,
	}
}

// Run starts the scheduling loop. Blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	log.Println(" [sched] scheduler started")
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println(" [sched] scheduler stopped")
			return
		case <-ticker.C:
			if err := s.checkAndRun(ctx); err != nil {
				log.Printf(" [sched] error: %v", err)
			}
		}
	}
}

func (s *Scheduler) checkAndRun(ctx context.Context) error {
	now := oasis.NowUnix()
	dueActions, err := s.store.GetDueScheduledActions(ctx, now)
	if err != nil {
		return err
	}

	if len(dueActions) == 0 {
		return nil
	}

	// Find owner chat ID
	ownerStr, err := s.store.GetConfig(ctx, "owner_user_id")
	if err != nil || ownerStr == "" {
		return nil
	}

	for _, action := range dueActions {
		log.Printf(" [sched] executing: %s", action.Description)

		// Parse tool calls
		var toolCalls []oasis.ScheduledToolCall
		if err := json.Unmarshal([]byte(action.ToolCalls), &toolCalls); err != nil {
			// Try string-encoded fallback
			var strs []string
			if err2 := json.Unmarshal([]byte(action.ToolCalls), &strs); err2 == nil {
				for _, str := range strs {
					var tc oasis.ScheduledToolCall
					if err3 := json.Unmarshal([]byte(str), &tc); err3 == nil {
						toolCalls = append(toolCalls, tc)
					}
				}
			}
			if len(toolCalls) == 0 {
				log.Printf(" [sched] invalid tool_calls JSON: %v", err)
				continue
			}
		}

		// Execute each tool
		var results []string
		for _, tc := range toolCalls {
			log.Printf(" [sched] tool: %s(%s)", tc.Tool, string(tc.Params))
			result, execErr := s.tools.Execute(ctx, tc.Tool, tc.Params)
			output := result.Content
			if execErr != nil {
				output = "error: " + execErr.Error()
			} else if result.Error != "" {
				output = "error: " + result.Error
			}
			results = append(results, fmt.Sprintf("## %s\n%s", tc.Tool, output))
		}

		combined := strings.Join(results, "\n\n")

		// Synthesize or format results
		var message string
		if action.SynthesisPrompt != "" {
			message = s.synthesize(ctx, combined, action.SynthesisPrompt, action.Description)
		} else {
			message = fmt.Sprintf("**%s**\n\n%s", action.Description, combined)
		}

		// Send to owner
		if _, err := s.frontend.Send(ctx, ownerStr, message); err != nil {
			log.Printf(" [sched] send failed: %v", err)
		}

		// Update next run
		isOnce := strings.HasSuffix(action.Schedule, " once")
		if isOnce {
			// Disable one-shot schedule
			_ = s.store.UpdateScheduledActionEnabled(ctx, action.ID, false)
			log.Printf(" [sched] done (once): %s, disabled", action.Description)
		} else {
			nextRun, ok := schedule.ComputeNextRun(action.Schedule, now, s.tzOffset)
			if !ok {
				nextRun = now + 86400 // fallback: 24h
			}
			action.NextRun = nextRun
			_ = s.store.UpdateScheduledAction(ctx, action)
			log.Printf(" [sched] done: %s, next: %s",
				action.Description, formatLocalTime(nextRun, s.tzOffset))
		}
	}

	return nil
}

func (s *Scheduler) synthesize(ctx context.Context, toolResults, synthesisPrompt, description string) string {
	tz := s.tzOffset
	now := time.Now().UTC().Add(time.Duration(tz) * time.Hour)
	timeStr := now.Format("2006-01-02 15:04")
	tzStr := fmt.Sprintf("UTC+%d", tz)

	system := fmt.Sprintf(
		"You are Oasis, a personal AI assistant. Current time: %s (%s).\n\n"+
			"You are generating a scheduled report: %q.\n"+
			"User's formatting instruction: %s\n\n"+
			"Based on the tool results below, create a concise, well-formatted message.\n\n"+
			"Tool results:\n%s",
		timeStr, tzStr, description, synthesisPrompt, toolResults)

	req := oasis.ChatRequest{
		Messages: []oasis.ChatMessage{
			oasis.SystemMessage(system),
			oasis.UserMessage("Generate the report."),
		},
	}

	resp, err := s.intentLLM.Chat(ctx, req)
	if err != nil {
		log.Printf(" [sched] synthesis failed: %v", err)
		return fmt.Sprintf("**%s**\n\n%s", description, toolResults)
	}
	return resp.Content
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
