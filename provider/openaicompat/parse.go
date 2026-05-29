package openaicompat

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	oasis "github.com/nevindra/oasis/core"
)

// mapOpenAIFinishReason converts an OpenAI finish_reason string to an
// oasis.FinishReason constant. Unknown values return an empty string so
// the agent loop can synthesize a reason from context (e.g. tool calls present).
func mapOpenAIFinishReason(reason string) oasis.FinishReason {
	switch reason {
	case "stop":
		return oasis.FinishStop
	case "tool_calls":
		return oasis.FinishToolCalls
	case "length":
		return oasis.FinishLength
	case "content_filter":
		return oasis.FinishContentFilter
	default:
		return ""
	}
}

// ParseResponse converts an OpenAI-format ChatResponse to an oasis ChatResponse.
// It extracts content, tool calls, usage, finish reason, and provider metadata
// from choices[0] and the top-level response fields.
func ParseResponse(resp ChatResponse) (oasis.ChatResponse, error) {
	var out oasis.ChatResponse

	if len(resp.Choices) == 0 {
		return out, nil
	}

	choice := resp.Choices[0]
	if choice.Message != nil {
		out.Content = choice.Message.Content
		out.ToolCalls = ParseToolCalls(choice.Message.ToolCalls)
		out.Attachments = imagesToAttachments(choice.Message.Images)
	}

	out.FinishReason = mapOpenAIFinishReason(choice.FinishReason)

	if resp.Usage != nil {
		out.Usage = oasis.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
		// OpenAI: cache hits arrive nested under prompt_tokens_details.
		if resp.Usage.PromptTokensDetails != nil {
			out.Usage.CachedTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		// Anthropic: cache hits and warming costs arrive as top-level fields.
		// CacheReadInputTokens maps to the same CachedTokens concept (read = hit).
		// We prefer the Anthropic field when the OpenAI nested field is absent.
		if resp.Usage.CacheReadInputTokens > 0 && out.Usage.CachedTokens == 0 {
			out.Usage.CachedTokens = resp.Usage.CacheReadInputTokens
		}
		out.Usage.CacheCreationTokens = resp.Usage.CacheCreationInputTokens
	}

	if resp.SystemFingerprint != "" {
		meta, err := json.Marshal(map[string]string{
			"system_fingerprint": resp.SystemFingerprint,
		})
		if err == nil {
			out.ProviderMeta = meta
		}
	}

	return out, nil
}

// imagesToAttachments converts response `images` entries into oasis Attachments.
// Inline data URIs ("data:<mime>;base64,<...>") are decoded to bytes; remote
// URLs are passed through as URL attachments.
func imagesToAttachments(imgs []ImageOut) []oasis.Attachment {
	if len(imgs) == 0 {
		return nil
	}
	var out []oasis.Attachment
	for _, img := range imgs {
		if img.ImageURL == nil || img.ImageURL.URL == "" {
			continue
		}
		if att, ok := parseImageDataURI(img.ImageURL.URL); ok {
			out = append(out, att)
			continue
		}
		// Remote URL — let the consumer fetch it. MIME is best-effort.
		out = append(out, oasis.Attachment{MimeType: "image/png", URL: img.ImageURL.URL})
	}
	return out
}

// parseImageDataURI decodes a "data:<mime>;base64,<payload>" URI into an
// Attachment. Returns ok=false if the string is not a base64 data URI.
func parseImageDataURI(uri string) (oasis.Attachment, bool) {
	if !strings.HasPrefix(uri, "data:") {
		return oasis.Attachment{}, false
	}
	rest := uri[len("data:"):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return oasis.Attachment{}, false
	}
	meta, payload := rest[:comma], rest[comma+1:]
	mime := meta
	isB64 := false
	if semi := strings.IndexByte(meta, ';'); semi >= 0 {
		mime = meta[:semi]
		isB64 = strings.Contains(meta[semi:], "base64")
	}
	if !isB64 {
		return oasis.Attachment{}, false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return oasis.Attachment{}, false
	}
	return oasis.Attachment{MimeType: mime, Data: data}, true
}

// ParseToolCalls converts OpenAI tool call requests to oasis ToolCalls.
// OpenAI returns function.arguments as a JSON string; we parse it into json.RawMessage.
func ParseToolCalls(tcs []ToolCallRequest) []oasis.ToolCall {
	if len(tcs) == 0 {
		return nil
	}

	out := make([]oasis.ToolCall, 0, len(tcs))
	for _, tc := range tcs {
		args := json.RawMessage(tc.Function.Arguments)
		// Validate that arguments is valid JSON; if not, wrap as a JSON string.
		if !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		out = append(out, oasis.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
		})
	}
	return out
}
