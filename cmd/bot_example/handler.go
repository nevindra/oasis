package main

import (
	"context"
	"strings"
	"time"
)

// streamToTelegram consumes token chunks from ch and edits a Telegram message
// with accumulated content every editInterval. Performs a final formatted edit
// when the channel closes.
func (a *App) streamToTelegram(ctx context.Context, chatID, msgID string, ch <-chan string) {
	const editInterval = time.Second

	var accumulated strings.Builder
	lastEdit := time.Now()

	for chunk := range ch {
		accumulated.WriteString(chunk)
		if time.Since(lastEdit) >= editInterval {
			_ = a.frontend.Edit(ctx, chatID, msgID, accumulated.String())
			lastEdit = time.Now()
		}
	}

	text := accumulated.String()
	if text != "" {
		_ = a.frontend.EditFormatted(ctx, chatID, msgID, text)
	}
}
