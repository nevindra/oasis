package telegram

// ApiResponse is the generic wrapper for all Telegram Bot API responses.
type ApiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
}

// Update represents an incoming update from the Telegram Bot API.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

// Message represents a Telegram message.
type Message struct {
	MessageID      int64      `json:"message_id"`
	From           *User      `json:"from,omitempty"`
	Chat           Chat       `json:"chat"`
	Text           string     `json:"text,omitempty"`
	Document       *Document  `json:"document,omitempty"`
	Photo          []PhotoSize `json:"photo,omitempty"`
	Caption        string     `json:"caption,omitempty"`
	ReplyToMessage *Message   `json:"reply_to_message,omitempty"`
}

// Chat represents a Telegram chat.
type Chat struct {
	ID int64 `json:"id"`
}

// User represents a Telegram user.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// Document represents a general file sent in a message.
type Document struct {
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

// File represents a file ready to be downloaded from Telegram servers.
type File struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path,omitempty"`
}
