package observer

import (
	"encoding/json"

	oasis "github.com/nevindra/oasis/core"

	"go.opentelemetry.io/otel/attribute"
)

// GenAI semantic-convention and Langfuse-mapped attribute keys. Langfuse's
// OTLP endpoint maps gen_ai.* per the OTel GenAI semconv and gives
// langfuse.observation.* precedence (see
// https://langfuse.com/integrations/native/opentelemetry#property-mapping);
// other OTel backends pick up the gen_ai.* keys.
var (
	AttrGenAIRequestModel = attribute.Key("gen_ai.request.model")
	AttrGenAISystem       = attribute.Key("gen_ai.system")
	AttrGenAIInputTokens  = attribute.Key("gen_ai.usage.input_tokens")
	AttrGenAIOutputTokens = attribute.Key("gen_ai.usage.output_tokens")
	AttrGenAICachedTokens = attribute.Key("gen_ai.usage.cached_tokens")
	AttrGenAICost         = attribute.Key("gen_ai.usage.cost")

	AttrObservationType      = attribute.Key("langfuse.observation.type")
	AttrObservationInput     = attribute.Key("langfuse.observation.input")
	AttrObservationOutput    = attribute.Key("langfuse.observation.output")
	AttrCompletionStartTime  = attribute.Key("langfuse.observation.completion_start_time")
	AttrObservationLevelAttr = attribute.Key("langfuse.observation.level")
)

// Payload caps keep span attributes bounded: one runaway message cannot blow
// up the OTLP export. Generous because chat histories with tool results are
// legitimately large.
const (
	maxMessageContent = 20_000  // runes per message content
	maxPayloadJSON    = 200_000 // bytes per input/output attribute
)

// wireMessage is the OpenAI chat-completions message shape. Langfuse renders
// input/output as a role-labeled conversation when it receives this format,
// including tool-call cards.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func toWireMessage(m oasis.ChatMessage) wireMessage {
	w := wireMessage{
		Role:       string(m.Role),
		Content:    truncateRunes(m.Content, maxMessageContent),
		ToolCallID: m.ToolCallID,
	}
	for _, tc := range m.ToolCalls {
		w.ToolCalls = append(w.ToolCalls, wireToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: wireFunction{
				Name:      tc.Name,
				Arguments: truncateRunes(string(tc.Args), maxMessageContent),
			},
		})
	}
	return w
}

// ChatInputJSON renders the request messages as an OpenAI-format JSON array.
func ChatInputJSON(req oasis.ChatRequest) string {
	msgs := make([]wireMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, toWireMessage(m))
	}
	return marshalCapped(msgs)
}

// ChatOutputJSON renders the response as a single OpenAI-format assistant message.
func ChatOutputJSON(resp oasis.ChatResponse) string {
	return marshalCapped(toWireMessage(oasis.ChatMessage{
		Role:      oasis.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	}))
}

func marshalCapped(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if len(b) > maxPayloadJSON {
		// Invalid JSON after the cut is fine — Langfuse stores it as a string.
		return string(b[:maxPayloadJSON]) + "…(truncated)"
	}
	return string(b)
}

// toolNamesList renders the advertised tool set as a comma-separated list,
// capped so a huge registry cannot bloat the span.
func toolNamesList(tools []oasis.ToolDefinition) string {
	const cap = 4_000
	var b []byte
	for i, t := range tools {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = append(b, t.Name...)
		if len(b) > cap {
			return string(b[:cap]) + "…(truncated)"
		}
	}
	return string(b)
}

func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…(truncated)"
}
