package openaicompat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis"
)

// BuildBody converts oasis ChatMessages and a model name into an OpenAI-format ChatRequest.
// System messages are kept in the messages array as role:"system".
// Options configure generation parameters (temperature, top_p, etc.).
func BuildBody(messages []oasis.ChatMessage, tools []oasis.ToolDefinition, model string, schema *oasis.ResponseSchema, opts ...Option) ChatRequest {
	var msgs []Message

	for _, m := range messages {
		switch {
		case m.Role == "system":
			msgs = append(msgs, Message{
				Role:    "system",
				Content: m.Content,
			})

		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			// Assistant message with tool calls.
			var tcs []ToolCallRequest
			for _, tc := range m.ToolCalls {
				tcs = append(tcs, ToolCallRequest{
					ID:   tc.ID,
					Type: "function",
					Function: FunctionCall{
						Name:      tc.Name,
						Arguments: string(tc.Args),
					},
				})
			}
			msg := Message{
				Role:      "assistant",
				ToolCalls: tcs,
			}
			// Include text content if present alongside tool calls.
			if m.Content != "" {
				msg.Content = m.Content
			}
			msgs = append(msgs, msg)

		case m.Role == "tool":
			// Tool result message.
			msgs = append(msgs, Message{
				Role:       "tool",
				Content:    m.Content,
				ToolCallID: m.ToolCallID,
			})

		default:
			// Regular user or assistant message.
			if len(m.Attachments) > 0 {
				// Multimodal: build content blocks.
				var blocks []ContentBlock
				if m.Content != "" {
					blocks = append(blocks, ContentBlock{
						Type: "text",
						Text: m.Content,
					})
				}
				for _, att := range m.Attachments {
					url := att.URL
					if url == "" {
						url = fmt.Sprintf("data:%s;base64,%s",
							att.MimeType, base64.StdEncoding.EncodeToString(att.InlineData()))
					}
					if strings.HasPrefix(att.MimeType, "image/") {
						blocks = append(blocks, ContentBlock{
							Type:     "image_url",
							ImageURL: &ImageURL{URL: url},
						})
					} else {
						blocks = append(blocks, ContentBlock{
							Type: "file",
							File: &FileData{URL: url},
						})
					}
				}
				msgs = append(msgs, Message{
					Role:    m.Role,
					Content: blocks,
				})
			} else {
				msgs = append(msgs, Message{
					Role:    m.Role,
					Content: m.Content,
				})
			}
		}
	}

	req := ChatRequest{
		Model:    model,
		Messages: msgs,
	}

	if len(tools) > 0 {
		req.Tools = BuildToolDefs(tools)
	}

	// Structured output: enforce JSON response matching the schema.
	if schema != nil && len(schema.Schema) > 0 {
		req.ResponseFormat = &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &JSONSchema{
				Name:   schema.Name,
				Schema: schema.Schema,
				Strict: true,
			},
		}
	}

	for _, opt := range opts {
		opt(&req)
	}

	return req
}

// BuildToolDefs converts oasis ToolDefinitions to OpenAI tool format.
func BuildToolDefs(tools []oasis.ToolDefinition) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out = append(out, Tool{
			Type: "function",
			Function: Function{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return out
}
