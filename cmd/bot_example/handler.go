package main

import (
	"context"
	"strings"
	"time"

	oasis "github.com/nevindra/oasis"
)

const telegramMaxLen = 4096

// streamToTelegram consumes stream events from ch and edits a Telegram message
// with accumulated text content every editInterval. Only text-delta events
// contribute to the displayed message; other events are silently consumed.
// Performs a final formatted edit when the channel closes. If the final response
// exceeds Telegram's 4096-char limit, the placeholder is filled with the first
// chunk and the remainder is sent as a new message.
func (a *App) streamToTelegram(ctx context.Context, chatID, msgID string, ch <-chan oasis.StreamEvent) {
	const editInterval = time.Second

	var accumulated strings.Builder
	lastEdit := time.Now()

	for ev := range ch {
		if ev.Type != oasis.EventTextDelta {
			continue
		}
		accumulated.WriteString(ev.Content)
		if time.Since(lastEdit) >= editInterval {
			preview := accumulated.String()
			if len(preview) > telegramMaxLen {
				preview = preview[:telegramMaxLen]
			}
			_ = a.frontend.Edit(ctx, chatID, msgID, preview)
			lastEdit = time.Now()
		}
	}

	text := accumulated.String()
	if text == "" {
		return
	}

	if len(text) <= telegramMaxLen {
		_ = a.frontend.EditFormatted(ctx, chatID, msgID, text)
		return
	}

	// Response exceeds Telegram's edit limit: fill placeholder with the first
	// portion (split at last newline for cleanliness), send the overflow as a
	// new message. Send handles further splitting automatically.
	splitAt := telegramMaxLen
	if idx := strings.LastIndex(text[:telegramMaxLen], "\n"); idx > 0 {
		splitAt = idx + 1
	}
	_ = a.frontend.EditFormatted(ctx, chatID, msgID, text[:splitAt])
	_, _ = a.frontend.Send(ctx, chatID, text[splitAt:])
}
