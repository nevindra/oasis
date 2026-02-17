package oasis

import "encoding/json"

// --- Domain types (database records) ---

type Document struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

type Chunk struct {
	ID         string    `json:"id"`
	DocumentID string    `json:"document_id"`
	Content    string    `json:"content"`
	ChunkIndex int       `json:"chunk_index"`
	Embedding  []float32 `json:"-"`
}

type Conversation struct {
	ID        string `json:"id"`
	ChatID    string `json:"chat_id"`
	CreatedAt int64  `json:"created_at"`
}

type Message struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversation_id"`
	Role           string    `json:"role"` // "user" or "assistant"
	Content        string    `json:"content"`
	Embedding      []float32 `json:"-"`
	CreatedAt      int64     `json:"created_at"`
}

type Fact struct {
	ID         string    `json:"id"`
	Fact       string    `json:"fact"`
	Category   string    `json:"category"`
	Confidence float64   `json:"confidence"`
	Embedding  []float32 `json:"-"`
	CreatedAt  int64     `json:"created_at"`
	UpdatedAt  int64     `json:"updated_at"`
}

// Intent for classification
type Intent int

const (
	IntentChat   Intent = iota
	IntentAction
)

// Scheduled action (DB record)
type ScheduledAction struct {
	ID              string `json:"id"`
	Description     string `json:"description"`
	Schedule        string `json:"schedule"`
	ToolCalls       string `json:"tool_calls"`
	SynthesisPrompt string `json:"synthesis_prompt"`
	NextRun         int64  `json:"next_run"`
	Enabled         bool   `json:"enabled"`
	CreatedAt       int64  `json:"created_at"`
}

type ScheduledToolCall struct {
	Tool   string          `json:"tool"`
	Params json.RawMessage `json:"params"`
}

// --- LLM protocol types ---

type ChatMessage struct {
	Role       string          `json:"role"` // "system", "user", "assistant", "tool"
	Content    string          `json:"content"`
	Images     []ImageData     `json:"images,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"` // provider-specific (e.g. Gemini thoughtSignature)
}

type ImageData struct {
	MimeType string `json:"mime_type"`
	Base64   string `json:"base64"`
}

type ToolCall struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Args     json.RawMessage `json:"args"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type ChatRequest struct {
	Messages []ChatMessage `json:"messages"`
}

type ChatResponse struct {
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     Usage      `json:"usage"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// --- Incoming message from frontend ---

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

type FileInfo struct {
	FileID   string
	FileName string
	MimeType string
	FileSize int64
}

// --- ChatMessage constructors ---

func UserMessage(text string) ChatMessage {
	return ChatMessage{Role: "user", Content: text}
}

func SystemMessage(text string) ChatMessage {
	return ChatMessage{Role: "system", Content: text}
}

func AssistantMessage(text string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: text}
}

func ToolResultMessage(callID, content string) ChatMessage {
	return ChatMessage{Role: "tool", Content: content, ToolCallID: callID}
}
