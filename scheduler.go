package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// scheduler executes due scheduled actions in the background.
// It polls the Store every 60 seconds for actions whose NextRun has passed,
// executes their tool calls via the ToolRegistry, optionally synthesizes
// results using a Provider, and delivers them through the Frontend.
type scheduler struct {
	store    Store
	tools    *ToolRegistry
	frontend Frontend
	provider Provider // used for result synthesis
	tzOffset int
}

// run starts the scheduler loop, checking for due actions every 60 seconds.
// It blocks until ctx is cancelled.
func (s *scheduler) run(ctx context.Context) {
	log.Println("oasis: scheduler started")
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("oasis: scheduler stopped")
			return
		case <-ticker.C:
			if err := s.checkAndRun(ctx); err != nil {
				log.Printf("oasis: scheduler: %v", err)
			}
		}
	}
}

func (s *scheduler) checkAndRun(ctx context.Context) error {
	now := NowUnix()
	due, err := s.store.GetDueScheduledActions(ctx, now)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}

	// The owner_user_id config determines who receives scheduled results.
	ownerID, err := s.store.GetConfig(ctx, "owner_user_id")
	if err != nil || ownerID == "" {
		return nil
	}

	for _, action := range due {
		log.Printf("oasis: scheduler executing: %s", action.Description)
		s.execute(ctx, action, ownerID, now)
	}
	return nil
}

func (s *scheduler) execute(ctx context.Context, action ScheduledAction, ownerID string, now int64) {
	// Parse the tool calls stored as JSON in the scheduled action.
	toolCalls, ok := parseScheduledToolCalls(action.ToolCalls)
	if !ok {
		log.Printf("oasis: scheduler: invalid tool_calls for %q", action.Description)
		return
	}

	// Execute each tool and collect results.
	var results []string
	for _, tc := range toolCalls {
		log.Printf("oasis: scheduler tool: %s", tc.Tool)
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

	// Format the message: use LLM synthesis if a prompt is provided,
	// otherwise wrap tool output with the action description as a header.
	var message string
	if action.SynthesisPrompt != "" {
		message = s.synthesize(ctx, combined, action.SynthesisPrompt, action.Description)
	} else {
		message = fmt.Sprintf("**%s**\n\n%s", action.Description, combined)
	}

	if _, err := s.frontend.Send(ctx, ownerID, message); err != nil {
		log.Printf("oasis: scheduler send failed: %v", err)
	}

	// Advance schedule: disable one-shot actions, compute next run for recurring ones.
	if strings.HasSuffix(action.Schedule, " once") {
		_ = s.store.UpdateScheduledActionEnabled(ctx, action.ID, false)
		log.Printf("oasis: scheduler done (once): %s", action.Description)
	} else {
		nextRun, ok := ComputeNextRun(action.Schedule, now, s.tzOffset)
		if !ok {
			nextRun = now + 86400 // fallback: retry in 24h
		}
		action.NextRun = nextRun
		_ = s.store.UpdateScheduledAction(ctx, action)
		log.Printf("oasis: scheduler done: %s, next: %s",
			action.Description, FormatLocalTime(nextRun, s.tzOffset))
	}
}

func (s *scheduler) synthesize(ctx context.Context, toolResults, synthesisPrompt, description string) string {
	tz := s.tzOffset
	now := time.Now().UTC().Add(time.Duration(tz) * time.Hour)
	timeStr := now.Format("2006-01-02 15:04")
	tzStr := fmt.Sprintf("UTC+%d", tz)

	system := fmt.Sprintf(
		"You are a personal AI assistant. Current time: %s (%s).\n\n"+
			"You are generating a scheduled report: %q.\n"+
			"User's formatting instruction: %s\n\n"+
			"Based on the tool results below, create a concise, well-formatted message.\n\n"+
			"Tool results:\n%s",
		timeStr, tzStr, description, synthesisPrompt, toolResults)

	req := ChatRequest{
		Messages: []ChatMessage{
			SystemMessage(system),
			UserMessage("Generate the report."),
		},
	}

	resp, err := s.provider.Chat(ctx, req)
	if err != nil {
		log.Printf("oasis: scheduler synthesis failed: %v", err)
		return fmt.Sprintf("**%s**\n\n%s", description, toolResults)
	}
	return resp.Content
}

// parseScheduledToolCalls parses tool calls from a scheduled action's JSON.
// Handles both []ScheduledToolCall and []string (legacy string-encoded) formats.
func parseScheduledToolCalls(raw string) ([]ScheduledToolCall, bool) {
	var calls []ScheduledToolCall
	if err := json.Unmarshal([]byte(raw), &calls); err == nil && len(calls) > 0 {
		return calls, true
	}
	calls = nil // reset — json.Unmarshal may partially populate on error

	// Legacy fallback: array of JSON-encoded strings.
	var strs []string
	if err := json.Unmarshal([]byte(raw), &strs); err == nil {
		for _, s := range strs {
			var tc ScheduledToolCall
			if err := json.Unmarshal([]byte(s), &tc); err == nil {
				calls = append(calls, tc)
			}
		}
	}
	return calls, len(calls) > 0
}

// --- App integration ---

// WithScheduler enables the background scheduler that executes due scheduled
// actions automatically. The scheduler starts when App.Run is called and
// stops when the context is cancelled — no orphaned goroutines.
//
// tzOffset is the user's timezone offset from UTC in whole hours.
// Common values: 7 (WIB/Jakarta), 8 (WITA/Makassar), 9 (WIT/Jayapura),
// -5 (EST), 0 (UTC), 1 (CET).
//
// By default the scheduler uses the app's main Provider for synthesis.
// Use WithSchedulerProvider to override with a cheaper/faster model.
func WithScheduler(tzOffset int) Option {
	return func(a *App) {
		a.schedEnabled = true
		a.schedTZOffset = tzOffset
	}
}

// WithSchedulerProvider sets a separate LLM provider for synthesizing
// scheduled action results. If not set, the app's main Provider is used.
//
// Synthesis is non-interactive (no streaming, no tool calling), so a
// cheaper/faster model is usually sufficient (e.g., Gemini Flash-Lite).
func WithSchedulerProvider(p Provider) Option {
	return func(a *App) { a.schedProvider = p }
}
