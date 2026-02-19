package main

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

// IncomingMessage represents a message received from the frontend.
type IncomingMessage struct {
	ID           string
	ChatID       string
	UserID       string
	Text         string
	ReplyToMsgID string
	Document     *FileInfo
	Photos       []FileInfo
	Caption      string
}

// FileInfo describes a file or photo attachment.
type FileInfo struct {
	FileID   string
	FileName string
	MimeType string
	FileSize int64
}

// --- Telegram Bot API types ---

// ApiResponse is the generic wrapper for all Telegram Bot API responses.
type ApiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
}

// Update represents an incoming update from the Telegram Bot API.
type Update struct {
	UpdateID int64       `json:"update_id"`
	Message  *TGMessage  `json:"message,omitempty"`
}

// TGMessage represents a Telegram message.
type TGMessage struct {
	MessageID      int64       `json:"message_id"`
	From           *TGUser     `json:"from,omitempty"`
	Chat           TGChat      `json:"chat"`
	Text           string      `json:"text,omitempty"`
	Document       *TGDocument `json:"document,omitempty"`
	Photo          []PhotoSize `json:"photo,omitempty"`
	Caption        string      `json:"caption,omitempty"`
	ReplyToMessage *TGMessage  `json:"reply_to_message,omitempty"`
}

// TGChat represents a Telegram chat.
type TGChat struct {
	ID int64 `json:"id"`
}

// TGUser represents a Telegram user.
type TGUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// TGDocument represents a general file sent in a message.
type TGDocument struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

// PhotoSize represents one size of a photo or file/sticker thumbnail.
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// TGFile represents a file ready to be downloaded from Telegram servers.
type TGFile struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path,omitempty"`
}
