// Package openaicompat provides a ready-to-use Provider for any OpenAI-compatible
// API, plus shared helpers (types, body building, response parsing, SSE streaming)
// for building custom providers.
//
// For most OpenAI-compatible services (OpenAI, OpenRouter, Groq, Together,
// Fireworks, DeepSeek, Mistral, Ollama, vLLM, LM Studio, Azure OpenAI), use
// NewProvider directly:
//
//	llm := openaicompat.NewProvider("sk-xxx", "gpt-4o", "https://api.openai.com/v1")
//
// For providers that need custom request/response handling, use the shared helpers
// (BuildBody, StreamSSE, ParseResponse) to build a custom oasis.Provider.
package openaicompat

import (
	"bytes"
	"encoding/json"
)

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
	ToolChoice       *ToolChoice     `json:"tool_choice,omitempty"`
	// Modalities requests output modalities (e.g. ["text","image"]). Providers
	// that support image generation (OpenRouter, image-capable gateways) return
	// generated images when "image" is present. Omitted = text only.
	Modalities []string `json:"modalities,omitempty"`
	// When streaming, request usage in the final chunk.
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// ToolChoiceMode is the set of string tool-selection modes the OpenAI chat API
// accepts for `tool_choice`.
type ToolChoiceMode string

const (
	// ToolChoiceNone disables tool calls for this request.
	ToolChoiceNone ToolChoiceMode = "none"
	// ToolChoiceAuto lets the model decide whether to call a tool.
	ToolChoiceAuto ToolChoiceMode = "auto"
	// ToolChoiceRequired forces the model to call at least one tool.
	ToolChoiceRequired ToolChoiceMode = "required"
)

// ToolChoice controls how the model selects tools. It marshals to the OpenAI
// `tool_choice` wire shape: a bare string ("none"/"auto"/"required") for a mode,
// or an object {"type":"function","function":{"name":"..."}} when a specific
// function is named. Use ToolChoiceModeValue or ToolChoiceFunction to build one.
type ToolChoice struct {
	// Mode is the string selection mode. Used when FunctionName is empty.
	Mode ToolChoiceMode
	// FunctionName, when non-empty, forces the named function and overrides Mode.
	FunctionName string
}

// ToolChoiceModeValue builds a string-mode tool choice ("none"/"auto"/"required").
func ToolChoiceModeValue(mode ToolChoiceMode) ToolChoice {
	return ToolChoice{Mode: mode}
}

// ToolChoiceFunction forces the model to call the named function. It marshals to
// {"type":"function","function":{"name":name}}.
func ToolChoiceFunction(name string) ToolChoice {
	return ToolChoice{FunctionName: name}
}

// toolChoiceFunctionWire is the object wire shape for a specific-function choice.
type toolChoiceFunctionWire struct {
	Type     string `json:"type"` // always "function"
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

// MarshalJSON emits the OpenAI wire shape: a JSON string for a mode choice, or a
// {"type":"function","function":{"name":...}} object for a named-function choice.
func (tc ToolChoice) MarshalJSON() ([]byte, error) {
	if tc.FunctionName != "" {
		var w toolChoiceFunctionWire
		w.Type = "function"
		w.Function.Name = tc.FunctionName
		return json.Marshal(w)
	}
	return json.Marshal(string(tc.Mode))
}

// UnmarshalJSON parses either a JSON string mode or a function-choice object.
func (tc *ToolChoice) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*tc = ToolChoice{}
		return nil
	}
	if trimmed[0] == '{' {
		var w toolChoiceFunctionWire
		if err := json.Unmarshal(trimmed, &w); err != nil {
			return err
		}
		*tc = ToolChoice{FunctionName: w.Function.Name}
		return nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return err
	}
	*tc = ToolChoice{Mode: ToolChoiceMode(s)}
	return nil
}

// ResponseFormat controls the output format (e.g. structured JSON).
type ResponseFormat struct {
	Type       string      `json:"type"` // "json_schema"
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
	Role       string            `json:"role"`
	Content    MessageContent    `json:"content"` // marshals to a JSON string or an array of content blocks
	ToolCalls  []ToolCallRequest `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
}

// MessageContentKind discriminates the two OpenAI message-content wire shapes:
// a plain string or an array of typed content blocks.
type MessageContentKind uint8

const (
	// ContentString is plain-text content. It marshals to a JSON string.
	ContentString MessageContentKind = iota
	// ContentBlocks is multimodal content. It marshals to a JSON array of ContentBlock.
	ContentBlocks
)

// MessageContent is a tagged union over the two OpenAI message-content wire
// shapes. The OpenAI chat format accepts `content` as either a plain string or
// an array of typed content blocks (text/image_url/file); MessageContent models
// both without an untyped any. Use StringContent or BlockContent to construct it.
//
// The zero value marshals to an empty JSON string ("") — the same shape a plain
// empty-string message produces — so an unset Content is wire-compatible with
// the historical any-typed default.
type MessageContent struct {
	Kind   MessageContentKind
	String string
	Blocks []ContentBlock
}

// StringContent builds plain-text message content.
func StringContent(s string) MessageContent {
	return MessageContent{Kind: ContentString, String: s}
}

// BlockContent builds multimodal message content from typed content blocks.
func BlockContent(blocks []ContentBlock) MessageContent {
	return MessageContent{Kind: ContentBlocks, Blocks: blocks}
}

// IsString reports whether the content is the plain-string variant.
func (c MessageContent) IsString() bool { return c.Kind == ContentString }

// IsBlocks reports whether the content is the content-block-array variant.
func (c MessageContent) IsBlocks() bool { return c.Kind == ContentBlocks }

// MarshalJSON emits the OpenAI wire shape: a JSON string for ContentString, or a
// JSON array of content blocks for ContentBlocks. A nil block slice still emits
// an empty array ([]) so the array variant is preserved on the wire.
func (c MessageContent) MarshalJSON() ([]byte, error) {
	if c.Kind == ContentBlocks {
		blocks := c.Blocks
		if blocks == nil {
			blocks = []ContentBlock{}
		}
		return json.Marshal(blocks)
	}
	return json.Marshal(c.String)
}

// UnmarshalJSON parses either a JSON string (→ ContentString) or a JSON array of
// content blocks (→ ContentBlocks). A JSON null leaves the zero value (empty
// string content) so a missing/null content field round-trips safely.
func (c *MessageContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*c = MessageContent{}
		return nil
	}
	switch trimmed[0] {
	case '[':
		var blocks []ContentBlock
		if err := json.Unmarshal(trimmed, &blocks); err != nil {
			return err
		}
		*c = MessageContent{Kind: ContentBlocks, Blocks: blocks}
		return nil
	default:
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*c = MessageContent{Kind: ContentString, String: s}
		return nil
	}
}

// ContentBlock represents a typed content block for multimodal messages.
type ContentBlock struct {
	Type         string        `json:"type"` // "text", "image_url", or "file"
	Text         string        `json:"text,omitempty"`
	ImageURL     *ImageURL     `json:"image_url,omitempty"`
	File         *FileData     `json:"file,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON ensures a "text"-type block always carries its "text" field, even
// when the text is empty. The struct tag uses `omitempty`, which would drop an
// empty string and emit an invalid {"type":"text"} part — providers reject that
// with "Expected 'text' field in text type content part to be a string". This
// happens, e.g., when an empty-content message is promoted to a content block to
// attach cache_control. Non-text blocks (image_url/file) keep `text` omitted.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	if b.Type == "text" {
		return json.Marshal(struct {
			Type         string        `json:"type"`
			Text         string        `json:"text"`
			CacheControl *CacheControl `json:"cache_control,omitempty"`
		}{Type: b.Type, Text: b.Text, CacheControl: b.CacheControl})
	}
	type alias ContentBlock
	return json.Marshal(alias(b))
}

// CacheControl marks a content block as a cache breakpoint.
// The provider caches all content up to and including this block.
// Supported by Anthropic, Qwen, and other providers that implement
// the cache_control extension to the OpenAI chat completions format.
type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// ImageURL holds the URL (or data URI) for an image content block.
type ImageURL struct {
	URL string `json:"url"`
}

// FileData holds a URL (or data URI) for a non-image file content block (video, audio, PDF, etc.).
type FileData struct {
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
	// SystemFingerprint is an OpenAI-specific opaque string identifying the
	// backend configuration used for the request. Present on most OpenAI
	// responses; absent on other providers. Propagated into
	// oasis.ChatResponse.ProviderMeta as {"system_fingerprint":"..."}.
	SystemFingerprint string `json:"system_fingerprint,omitempty"`
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
	// Images carries generated images returned by image-capable models. This
	// is the de-facto OpenAI-compatible convention (OpenRouter and others):
	// each entry is {"type":"image_url","image_url":{"url":"data:<mime>;base64,<...>"}}.
	Images []ImageOut `json:"images,omitempty"`
}

// ImageOut is a generated-image entry in a response message's `images` array.
type ImageOut struct {
	Type     string    `json:"type,omitempty"` // "image_url"
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// Usage contains token usage statistics.
// OpenAI populates PromptTokensDetails.CachedTokens for cache hits.
// Anthropic populates CacheReadInputTokens (hits) and CacheCreationInputTokens
// (warming cost) as top-level fields on the same object instead.
type Usage struct {
	PromptTokens        int                  `json:"prompt_tokens"`
	CompletionTokens    int                  `json:"completion_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	PromptTokensDetails *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	// Anthropic-specific top-level cache fields.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// PromptTokensDetails breaks down prompt token usage, including cached tokens.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}
