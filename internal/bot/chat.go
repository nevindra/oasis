package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	oasis "github.com/nevindra/oasis"
)

const maxStreamRetries = 3

// handleChatStream handles a chat intent with streaming response.
func (a *App) handleChatStream(ctx context.Context, chatID, message string, thread oasis.Thread) string {
	return a.handleChatStreamWithContext(ctx, chatID, message, thread, "")
}

// handleChatStreamWithContext handles chat with optional extra context (e.g. file content).
func (a *App) handleChatStreamWithContext(ctx context.Context, chatID, message string, thread oasis.Thread, extraContext string) string {
	// Build memory context
	memoryContext := ""
	if a.memory != nil && a.embedding != nil {
		embs, err := a.embedding.Embed(ctx, []string{message})
		if err == nil && len(embs) > 0 {
			mc, err := a.memory.BuildContext(ctx, embs[0])
			if err == nil {
				memoryContext = mc
			}
		}
	}

	// Combine contexts
	fullContext := memoryContext
	if extraContext != "" {
		if fullContext != "" {
			fullContext += "\n" + extraContext
		} else {
			fullContext = extraContext
		}
	}

	// Build messages
	messages := a.buildSystemPrompt(ctx, fullContext, thread)
	messages = append(messages, oasis.UserMessage(message))

	req := oasis.ChatRequest{Messages: messages}

	// Send placeholder
	msgID, err := a.frontend.Send(ctx, chatID, "Thinking...")
	if err != nil {
		log.Printf(" [chat] failed to send placeholder: %v", err)
		return ""
	}

	var lastErr error

	for attempt := 0; attempt <= maxStreamRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<(attempt-1)) * time.Second
			log.Printf(" [chat] retry %d/%d in %s", attempt, maxStreamRetries, delay)
			_ = a.frontend.Edit(ctx, chatID, msgID,
				fmt.Sprintf("Retrying... (attempt %d/%d)", attempt+1, maxStreamRetries+1))
			time.Sleep(delay)
		}

		ch := make(chan string, 100)

		// Start streaming in goroutine
		type streamResult struct {
			resp oasis.ChatResponse
			err  error
		}
		resultCh := make(chan streamResult, 1)
		go func() {
			resp, err := a.chatLLM.ChatStream(ctx, req, ch)
			resultCh <- streamResult{resp, err}
		}()

		// Consume stream, edit message periodically
		var accumulated strings.Builder
		lastEdit := time.Now()
		editInterval := time.Second

		for chunk := range ch {
			accumulated.WriteString(chunk)
			if time.Since(lastEdit) >= editInterval {
				_ = a.frontend.Edit(ctx, chatID, msgID, accumulated.String())
				lastEdit = time.Now()
			}
		}

		// Final formatted edit
		text := accumulated.String()
		if text != "" {
			_ = a.frontend.EditFormatted(ctx, chatID, msgID, text)
		}
		log.Printf(" [send] %d chars (streamed)", len(text))

		// Check result
		result := <-resultCh
		if result.err != nil {
			log.Printf(" [chat] stream error: %v", result.err)
			if text == "" && isTransientError(result.err) && attempt < maxStreamRetries {
				lastErr = result.err
				continue
			}
			if text == "" {
				_ = a.frontend.Edit(ctx, chatID, msgID,
					"Sorry, something went wrong. Please try again.")
				return ""
			}
		}

		if text == "" {
			_ = a.frontend.Edit(ctx, chatID, msgID,
				"Sorry, I got an empty response. Please try again.")
			return ""
		}

		return text
	}

	// All retries exhausted
	_ = a.frontend.Edit(ctx, chatID, msgID,
		"Sorry, the service is temporarily unavailable. Please try again later.")
	log.Printf(" [chat] all retries exhausted: %v", lastErr)
	return ""
}

// buildSystemPrompt constructs the system message with context and history.
func (a *App) buildSystemPrompt(ctx context.Context, memContext string, thread oasis.Thread) []oasis.ChatMessage {
	tz := a.config.Brain.TimezoneOffset
	now := time.Now().UTC().Add(time.Duration(tz) * time.Hour)
	timeStr := now.Format("2006-01-02 15:04")
	tzStr := fmt.Sprintf("UTC+%d", tz)

	system := fmt.Sprintf("You are Oasis, a personal AI assistant. You are helpful, concise, and friendly.\nCurrent date and time: %s (%s)\n", timeStr, tzStr)

	if memContext != "" {
		system += "\n" + memContext + "\n"
	}

	// Add conversation history
	history, err := a.store.GetMessages(ctx, thread.ID, a.config.Brain.ContextWindow)
	if err == nil && len(history) > 0 {
		system += "\n## Recent conversation (for context only — respond to the user's NEW message, not these)\n"
		for _, msg := range history {
			roleLabel := "User"
			if msg.Role == "assistant" {
				roleLabel = "Oasis"
			}
			system += fmt.Sprintf("%s: %s\n", roleLabel, msg.Content)
		}
	}

	return []oasis.ChatMessage{oasis.SystemMessage(system)}
}

// isTransientError checks if an error is retryable (429, 5xx).
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "temporarily")
}
