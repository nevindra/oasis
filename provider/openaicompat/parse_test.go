package openaicompat

import (
	"encoding/json"
	"testing"
)

func TestParseResponse_TextResponse(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-123",
		Choices: []Choice{
			{
				Index: 0,
				Message: &ChoiceMessage{
					Role:    "assistant",
					Content: "Hello! How can I help you?",
				},
				FinishReason: "stop",
			},
		},
		Usage: &Usage{
			PromptTokens:     10,
			CompletionTokens: 8,
			TotalTokens:      18,
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Content != "Hello! How can I help you?" {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
	if result.Usage.InputTokens != 10 {
		t.Errorf("expected 10 input tokens, got %d", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 8 {
		t.Errorf("expected 8 output tokens, got %d", result.Usage.OutputTokens)
	}
}

func TestParseResponse_ToolCallResponse(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-456",
		Choices: []Choice{
			{
				Index: 0,
				Message: &ChoiceMessage{
					Role: "assistant",
					ToolCalls: []ToolCallRequest{
						{
							ID:   "call_abc",
							Type: "function",
							Function: FunctionCall{
								Name:      "get_weather",
								Arguments: `{"city":"London","units":"celsius"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: &Usage{
			PromptTokens:     15,
			CompletionTokens: 20,
			TotalTokens:      35,
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}

	tc := result.ToolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("expected ID 'call_abc', got %q", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("expected name 'get_weather', got %q", tc.Name)
	}

	// Args should be valid JSON.
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("failed to parse tool call args: %v", err)
	}
	if args["city"] != "London" {
		t.Errorf("expected city 'London', got %v", args["city"])
	}
}

func TestParseResponse_EmptyChoices(t *testing.T) {
	resp := ChatResponse{
		ID:      "chatcmpl-789",
		Choices: []Choice{},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
}

func TestParseResponse_NoUsage(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-nousage",
		Choices: []Choice{
			{
				Message: &ChoiceMessage{
					Content: "Hello",
				},
			},
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Usage.InputTokens != 0 {
		t.Errorf("expected 0 input tokens, got %d", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 0 {
		t.Errorf("expected 0 output tokens, got %d", result.Usage.OutputTokens)
	}
}

func TestParseToolCalls(t *testing.T) {
	tcs := []ToolCallRequest{
		{
			ID:   "call_1",
			Type: "function",
			Function: FunctionCall{
				Name:      "search",
				Arguments: `{"query":"cats"}`,
			},
		},
		{
			ID:   "call_2",
			Type: "function",
			Function: FunctionCall{
				Name:      "calc",
				Arguments: `{"expr":"2+2"}`,
			},
		},
	}

	result := ParseToolCalls(tcs)
	if len(result) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result))
	}

	if result[0].ID != "call_1" {
		t.Errorf("expected ID 'call_1', got %q", result[0].ID)
	}
	if result[0].Name != "search" {
		t.Errorf("expected name 'search', got %q", result[0].Name)
	}

	var args map[string]any
	if err := json.Unmarshal(result[0].Args, &args); err != nil {
		t.Fatalf("failed to parse args: %v", err)
	}
	if args["query"] != "cats" {
		t.Errorf("expected query 'cats', got %v", args["query"])
	}

	if result[1].ID != "call_2" {
		t.Errorf("expected ID 'call_2', got %q", result[1].ID)
	}
	if result[1].Name != "calc" {
		t.Errorf("expected name 'calc', got %q", result[1].Name)
	}
}

func TestParseToolCalls_Empty(t *testing.T) {
	result := ParseToolCalls(nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestParseToolCalls_InvalidJSON(t *testing.T) {
	tcs := []ToolCallRequest{
		{
			ID:   "call_bad",
			Type: "function",
			Function: FunctionCall{
				Name:      "search",
				Arguments: `not valid json`,
			},
		},
	}

	result := ParseToolCalls(tcs)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result))
	}

	// Invalid JSON should be replaced with {}.
	if string(result[0].Args) != `{}` {
		t.Errorf("expected empty JSON object for invalid args, got %q", string(result[0].Args))
	}
}

func TestParseResponse_MultipleToolCalls(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-multi",
		Choices: []Choice{
			{
				Message: &ChoiceMessage{
					Role:    "assistant",
					Content: "I'll search and calculate.",
					ToolCalls: []ToolCallRequest{
						{
							ID:   "call_a",
							Type: "function",
							Function: FunctionCall{
								Name:      "search",
								Arguments: `{"q":"test"}`,
							},
						},
						{
							ID:   "call_b",
							Type: "function",
							Function: FunctionCall{
								Name:      "calc",
								Arguments: `{"expr":"1+1"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: &Usage{
			PromptTokens:     20,
			CompletionTokens: 30,
			TotalTokens:      50,
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Content != "I'll search and calculate." {
		t.Errorf("unexpected content: %q", result.Content)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "search" {
		t.Errorf("expected first tool 'search', got %q", result.ToolCalls[0].Name)
	}
	if result.ToolCalls[1].Name != "calc" {
		t.Errorf("expected second tool 'calc', got %q", result.ToolCalls[1].Name)
	}
}
