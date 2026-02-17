package oasis

import "context"

// Frontend abstracts the messaging channel (Telegram, Discord, HTTP, CLI).
type Frontend interface {
	// Poll returns a channel of incoming messages. Blocks until ctx is cancelled.
	Poll(ctx context.Context) (<-chan IncomingMessage, error)
	// Send sends a new message, returns the message ID for later editing.
	Send(ctx context.Context, chatID string, text string) (string, error)
	// Edit updates an existing message with plain text.
	Edit(ctx context.Context, chatID string, msgID string, text string) error
	// EditFormatted updates an existing message with rich formatting (HTML).
	EditFormatted(ctx context.Context, chatID string, msgID string, text string) error
	// SendTyping shows a typing indicator.
	SendTyping(ctx context.Context, chatID string) error
	// DownloadFile downloads a file by ID, returns data and filename.
	DownloadFile(ctx context.Context, fileID string) ([]byte, string, error)
}
