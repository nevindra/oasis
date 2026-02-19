// Package openaicompat provides shared types, body building, response parsing,
// and SSE streaming for OpenAI-compatible API providers (OpenAI, OpenRouter).
package openaicompat

import "encoding/json"

// --- Request types ---

// ChatRequest is the OpenAI chat completions request body.
type ChatRequest struct {
	Model            string          `json:"model"`
	Messages         []Message       `json:"messages"`
	Tools            []Tool          `json:"tools,omitempty"`
	Stream           bool            `json:"stream,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	MaxTokens        int             `json:"max_tokens,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	Stop             []string        `json:"stop,omitempty"`
	Seed             *int            `json:"seed,omitempty"`
	ResponseFormat   *ResponseFormat `json:"response_format,omitempty"`
	ToolChoice       any             `json:"tool_choice,omitempty"`
	// When streaming, request usage in the final chunk.
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// ResponseFormat controls the output format (e.g. structured JSON).
type ResponseFormat struct {
	Type       string      `json:"type"`                  // "json_schema"
	JSONSchema *JSONSchema `json:"json_schema,omitempty"`
}

// JSONSchema describes the expected JSON output shape.
type JSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

// StreamOptions controls streaming behavior.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Message is a single message in the OpenAI chat format.
type Message struct {
	Role       string          `json:"role"`
	Content    any             `json:"content"`                // string or []ContentBlock
	ToolCalls  []ToolCallRequest `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

// ContentBlock represents a typed content block for multimodal messages.
type ContentBlock struct {
	Type     string    `json:"type"`               // "text" or "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL holds the URL (or data URI) for an image content block.
type ImageURL struct {
	URL string `json:"url"`
}

// Tool wraps a function definition in the OpenAI tool format.
type Tool struct {
	Type     string   `json:"type"` // always "function"
	Function Function `json:"function"`
}

// Function describes a callable function for tool use.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCallRequest represents a tool call in an OpenAI API response or request.
// During streaming, Index indicates which tool call is being updated.
type ToolCallRequest struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the function name and arguments (as a JSON string).
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// --- Response types ---

// ChatResponse is the OpenAI chat completions response.
type ChatResponse struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice is a single completion choice.
type Choice struct {
	Index        int            `json:"index"`
	Message      *ChoiceMessage `json:"message,omitempty"`
	Delta        *ChoiceMessage `json:"delta,omitempty"`
	FinishReason string         `json:"finish_reason,omitempty"`
}

// ChoiceMessage is the message content within a choice (used for both message and delta).
type ChoiceMessage struct {
	Role      string            `json:"role,omitempty"`
	Content   string            `json:"content,omitempty"`
	ToolCalls []ToolCallRequest `json:"tool_calls,omitempty"`
	Refusal   string            `json:"refusal,omitempty"`
}

// Usage contains token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
