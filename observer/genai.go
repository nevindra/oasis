package observer

import (
	"encoding/json"
	"fmt"

	oasis "github.com/nevindra/oasis/core"

	"go.opentelemetry.io/otel/attribute"
)

// GenAI semantic-convention and OpenInference attribute keys. Arize (and
// Phoenix) read the openinference.* / input.value / output.value /
// llm.token_count.* keys per the OpenInference spec
// (https://arize.com/docs/ax/observe/tracing/semantic-conventions);
// other OTel backends pick up the gen_ai.* keys.
var (
	AttrGenAIRequestModel = attribute.Key("gen_ai.request.model")
	AttrGenAISystem       = attribute.Key("gen_ai.system")
	AttrGenAIInputTokens  = attribute.Key("gen_ai.usage.input_tokens")
	AttrGenAIOutputTokens = attribute.Key("gen_ai.usage.output_tokens")
	AttrGenAICachedTokens = attribute.Key("gen_ai.usage.cached_tokens")
	AttrGenAICost         = attribute.Key("gen_ai.usage.cost")

	AttrSpanKind    = attribute.Key("openinference.span.kind")
	AttrInputValue  = attribute.Key("input.value")
	AttrInputMime   = attribute.Key("input.mime_type")
	AttrOutputValue = attribute.Key("output.value")
	AttrOutputMime  = attribute.Key("output.mime_type")

	AttrTokenCountPrompt     = attribute.Key("llm.token_count.prompt")
	AttrTokenCountCompletion = attribute.Key("llm.token_count.completion")
	AttrTokenCountTotal      = attribute.Key("llm.token_count.total")
	AttrTokenCountCacheRead  = attribute.Key("llm.token_count.prompt_details.cache_read")
	AttrLLMCostTotal         = attribute.Key("llm.cost.total")
	AttrInvocationParams     = attribute.Key("llm.invocation_parameters")

	// Not part of OpenInference — kept as a custom attribute so
	// time-to-first-token stays visible on streaming generations.
	AttrCompletionStartTime = attribute.Key("llm.completion_start_time")
)

// OpenInference span kind values.
const (
	SpanKindLLM       = "LLM"
	SpanKindTool      = "TOOL"
	SpanKindAgent     = "AGENT"
	SpanKindChain     = "CHAIN"
	SpanKindEmbedding = "EMBEDDING"

	MimeJSON = "application/json"
	MimeText = "text/plain"
)

// Payload caps keep span attributes bounded: one runaway message cannot blow
// up the OTLP export. Generous because chat histories with tool results are
// legitimately large — in particular the system prompt regularly exceeds
// 20k runes and must land untruncated for prompt debugging.
const (
	maxMessageContent = 200_000   // runes per message content
	maxPayloadJSON    = 1_000_000 // bytes per input/output attribute
	maxFlatMessages   = 100       // flattened llm.*_messages.{i} entries per span
)

// wireMessage is the OpenAI chat-completions message shape. Trace backends
// render input/output as a role-labeled conversation when they receive this
// format, including tool-call cards.
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

// InputMessageAttrs flattens the request messages into OpenInference
// llm.input_messages.{i}.message.* attributes — the shape Arize renders as a
// conversation. Only the newest maxFlatMessages entries are flattened so a
// long history cannot bloat the span; input.value still carries the capped
// full JSON.
func InputMessageAttrs(req oasis.ChatRequest) []attribute.KeyValue {
	msgs := req.Messages
	if len(msgs) > maxFlatMessages {
		msgs = msgs[len(msgs)-maxFlatMessages:]
	}
	attrs := make([]attribute.KeyValue, 0, len(msgs)*2)
	for i, m := range msgs {
		attrs = append(attrs, messageAttrs(fmt.Sprintf("llm.input_messages.%d.", i), toWireMessage(m))...)
	}
	return attrs
}

// OutputMessageAttrs flattens the response into OpenInference
// llm.output_messages.0.message.* attributes.
func OutputMessageAttrs(resp oasis.ChatResponse) []attribute.KeyValue {
	return messageAttrs("llm.output_messages.0.", toWireMessage(oasis.ChatMessage{
		Role:      oasis.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	}))
}

func messageAttrs(prefix string, w wireMessage) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String(prefix+"message.role", w.Role),
	}
	if w.Content != "" {
		attrs = append(attrs, attribute.String(prefix+"message.content", w.Content))
	}
	for j, tc := range w.ToolCalls {
		tcPrefix := fmt.Sprintf("%smessage.tool_calls.%d.tool_call.", prefix, j)
		attrs = append(attrs,
			attribute.String(tcPrefix+"id", tc.ID),
			attribute.String(tcPrefix+"function.name", tc.Function.Name),
			attribute.String(tcPrefix+"function.arguments", tc.Function.Arguments),
		)
	}
	return attrs
}

func marshalCapped(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	if len(b) > maxPayloadJSON {
		// Invalid JSON after the cut is fine — backends store it as a string.
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
