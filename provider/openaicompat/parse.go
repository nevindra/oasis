package openaicompat

import (
	"encoding/json"

	"github.com/nevindra/oasis"
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
	}

	out.FinishReason = mapOpenAIFinishReason(choice.FinishReason)

	if resp.Usage != nil {
		out.Usage = oasis.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
		if resp.Usage.PromptTokensDetails != nil {
			out.Usage.CachedTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
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
