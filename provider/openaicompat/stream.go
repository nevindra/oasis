package openaicompat

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/nevindra/oasis"
)

// StreamSSE reads an SSE stream from body, sends text-delta events to ch, and
// returns the fully accumulated response (content + tool calls + usage).
//
// The channel is closed when streaming completes. Callers should read from ch
// in a separate goroutine. The context is used to cancel channel sends if the
// consumer is no longer interested.
//
// SSE format expected:
//
//	data: {"id":"...","choices":[...]}\n
//	data: [DONE]\n
func StreamSSE(ctx context.Context, body io.Reader, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	defer close(ch)

	scanner := bufio.NewScanner(body)
	// Increase buffer for large SSE payloads.
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var fullContent strings.Builder
	var usage oasis.Usage

	// Accumulate tool calls across chunks. OpenAI streams tool calls
	// incrementally: each chunk has an index, and arguments arrive as string fragments.
	type partialToolCall struct {
		ID   string
		Name string
		Args strings.Builder
	}
	var toolCalls []partialToolCall

	for scanner.Scan() {
		line := scanner.Text()

		// SSE lines that carry data start with "data: ".
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// End-of-stream sentinel.
		if data == "[DONE]" {
			break
		}

		var chunk ChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Skip malformed chunks.
			continue
		}

		if len(chunk.Choices) == 0 {
			// Usage-only chunk (some providers send this).
			if chunk.Usage != nil {
				usage.InputTokens = chunk.Usage.PromptTokens
				usage.OutputTokens = chunk.Usage.CompletionTokens
				if chunk.Usage.PromptTokensDetails != nil {
					usage.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
				}
			}
			continue
		}

		delta := chunk.Choices[0].Delta
		if delta == nil {
			continue
		}

		// Accumulate text content.
		if delta.Content != "" {
			fullContent.WriteString(delta.Content)
			select {
			case ch <- oasis.StreamEvent{Type: oasis.EventTextDelta, Content: delta.Content}:
			case <-ctx.Done():
				return oasis.ChatResponse{}, ctx.Err()
			}
		}

		// Accumulate tool calls.
		for _, tc := range delta.ToolCalls {
			// Ensure we have a slot for this tool call index.
			idx := tc.Index
			for len(toolCalls) <= idx {
				toolCalls = append(toolCalls, partialToolCall{})
			}

			if tc.ID != "" {
				toolCalls[idx].ID = tc.ID
			}
			if tc.Function.Name != "" {
				toolCalls[idx].Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				toolCalls[idx].Args.WriteString(tc.Function.Arguments)
			}
		}

		// Extract usage from chunks that include it.
		if chunk.Usage != nil {
			usage.InputTokens = chunk.Usage.PromptTokens
			usage.OutputTokens = chunk.Usage.CompletionTokens
			if chunk.Usage.PromptTokensDetails != nil {
				usage.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return oasis.ChatResponse{}, err
	}

	// Build final tool calls.
	var oasisToolCalls []oasis.ToolCall
	for _, tc := range toolCalls {
		args := json.RawMessage(tc.Args.String())
		if !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		oasisToolCalls = append(oasisToolCalls, oasis.ToolCall{
			ID:   tc.ID,
			Name: tc.Name,
			Args: args,
		})
	}

	return oasis.ChatResponse{
		Content:   fullContent.String(),
		ToolCalls: oasisToolCalls,
		Usage:     usage,
	}, nil
}
