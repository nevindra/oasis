package main

import (
	"context"
	"strings"
	"time"
)

const telegramMaxLen = 4096

// streamToTelegram consumes token chunks from ch and edits a Telegram message
// with accumulated content every editInterval. Performs a final formatted edit
// when the channel closes. If the final response exceeds Telegram's 4096-char
// limit, the placeholder is filled with the first chunk and the remainder is
// sent as a new message (Send handles further splitting automatically).
func (a *App) streamToTelegram(ctx context.Context, chatID, msgID string, ch <-chan string) {
	const editInterval = time.Second

	var accumulated strings.Builder
	lastEdit := time.Now()

	for chunk := range ch {
		accumulated.WriteString(chunk)
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
