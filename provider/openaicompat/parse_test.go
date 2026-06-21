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

func TestMapOpenAIFinishReason(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"stop", "stop"},
		{"tool_calls", "tool-calls"},
		{"length", "length"},
		{"content_filter", "content-filter"},
		{"", ""},
		{"unknown_value", ""},
	}
	for _, c := range cases {
		got := string(mapOpenAIFinishReason(c.in))
		if got != c.want {
			t.Errorf("mapOpenAIFinishReason(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseResponse_FinishReasonAndMeta(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-fp",
		Choices: []Choice{
			{
				Message: &ChoiceMessage{
					Content: "Done",
				},
				FinishReason: "stop",
			},
		},
		SystemFingerprint: "fp_abc123",
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.FinishReason != "stop" {
		t.Errorf("expected FinishReason 'stop', got %q", result.FinishReason)
	}
	if result.ProviderMeta == nil {
		t.Fatal("expected ProviderMeta to be set")
	}

	var meta map[string]string
	if err := json.Unmarshal(result.ProviderMeta, &meta); err != nil {
		t.Fatalf("failed to parse ProviderMeta: %v", err)
	}
	if meta["system_fingerprint"] != "fp_abc123" {
		t.Errorf("expected system_fingerprint 'fp_abc123', got %q", meta["system_fingerprint"])
	}
}

func TestParseResponse_NoSystemFingerprint(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-nofp",
		Choices: []Choice{
			{
				Message:      &ChoiceMessage{Content: "Hi"},
				FinishReason: "stop",
			},
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.ProviderMeta != nil {
		t.Errorf("expected nil ProviderMeta when system_fingerprint absent, got %s", result.ProviderMeta)
	}
}

func TestParseResponse_FinishReasonToolCalls(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-tc",
		Choices: []Choice{
			{
				Message: &ChoiceMessage{
					ToolCalls: []ToolCallRequest{
						{ID: "call_1", Function: FunctionCall{Name: "search", Arguments: `{}`}},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}
	if result.FinishReason != "tool-calls" {
		t.Errorf("expected FinishReason 'tool-calls', got %q", result.FinishReason)
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

// TestParseResponse_AnthropicCacheUsage verifies that Anthropic's top-level
// cache_read_input_tokens and cache_creation_input_tokens map to
// core.Usage.CachedTokens and core.Usage.CacheCreationTokens respectively.
func TestParseResponse_AnthropicCacheUsage(t *testing.T) {
	resp := ChatResponse{
		ID: "msg_01abc",
		Choices: []Choice{
			{
				Message:      &ChoiceMessage{Content: "Hello from Anthropic."},
				FinishReason: "stop",
			},
		},
		Usage: &Usage{
			PromptTokens:             14,
			CompletionTokens:         89,
			TotalTokens:              103,
			CacheCreationInputTokens: 1024,
			CacheReadInputTokens:     4096,
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Usage.InputTokens != 14 {
		t.Errorf("expected 14 input tokens, got %d", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 89 {
		t.Errorf("expected 89 output tokens, got %d", result.Usage.OutputTokens)
	}
	if result.Usage.CachedTokens != 4096 {
		t.Errorf("expected CachedTokens 4096 (cache_read_input_tokens), got %d", result.Usage.CachedTokens)
	}
	if result.Usage.CacheCreationTokens != 1024 {
		t.Errorf("expected CacheCreationTokens 1024 (cache_creation_input_tokens), got %d", result.Usage.CacheCreationTokens)
	}
}

// TestParseResponse_OpenAICacheUsage verifies that OpenAI's nested
// prompt_tokens_details.cached_tokens still maps to core.Usage.CachedTokens
// and that CacheCreationTokens stays zero (OpenAI doesn't expose it).
func TestParseResponse_OpenAICacheUsage(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-openai",
		Choices: []Choice{
			{
				Message:      &ChoiceMessage{Content: "Hello from OpenAI."},
				FinishReason: "stop",
			},
		},
		Usage: &Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			PromptTokensDetails: &PromptTokensDetails{
				CachedTokens: 80,
			},
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Usage.CachedTokens != 80 {
		t.Errorf("expected CachedTokens 80 (prompt_tokens_details.cached_tokens), got %d", result.Usage.CachedTokens)
	}
	if result.Usage.CacheCreationTokens != 0 {
		t.Errorf("expected CacheCreationTokens 0 for OpenAI shape, got %d", result.Usage.CacheCreationTokens)
	}
}

// TestParseResponse_BothFormats exercises a defensive scenario where both the
// OpenAI nested field and Anthropic top-level fields appear in the same payload.
// The OpenAI nested field takes precedence for CachedTokens (it's checked first),
// and CacheCreationTokens still comes from the Anthropic field.
// TestParseResponse_ReasoningContent verifies that reasoning_content in the
// non-stream response maps to ChatResponse.Thinking.
func TestParseResponse_ReasoningContent(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-rc",
		Choices: []Choice{
			{
				Message: &ChoiceMessage{
					Role:             "assistant",
					Content:          "42",
					ReasoningContent: "I must think carefully.",
				},
				FinishReason: "stop",
			},
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Content != "42" {
		t.Errorf("expected Content %q, got %q", "42", result.Content)
	}
	if result.Thinking != "I must think carefully." {
		t.Errorf("expected Thinking %q, got %q", "I must think carefully.", result.Thinking)
	}
}

// TestParseResponse_NoReasoningContent verifies that Thinking is empty when
// reasoning_content is absent (no regression).
func TestParseResponse_NoReasoningContent(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-norc",
		Choices: []Choice{
			{
				Message:      &ChoiceMessage{Content: "Hello"},
				FinishReason: "stop",
			},
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	if result.Thinking != "" {
		t.Errorf("expected empty Thinking when reasoning_content absent, got %q", result.Thinking)
	}
}

func TestParseResponse_BothFormats(t *testing.T) {
	resp := ChatResponse{
		ID: "chatcmpl-hybrid",
		Choices: []Choice{
			{
				Message:      &ChoiceMessage{Content: "Hybrid payload."},
				FinishReason: "stop",
			},
		},
		Usage: &Usage{
			PromptTokens:     50,
			CompletionTokens: 20,
			TotalTokens:      70,
			PromptTokensDetails: &PromptTokensDetails{
				CachedTokens: 30, // OpenAI nested — takes precedence
			},
			CacheReadInputTokens:     999, // would be ignored because OpenAI field is non-zero
			CacheCreationInputTokens: 512, // always mapped
		},
	}

	result, err := ParseResponse(resp)
	if err != nil {
		t.Fatalf("ParseResponse returned error: %v", err)
	}

	// OpenAI nested value wins (non-zero, checked first).
	if result.Usage.CachedTokens != 30 {
		t.Errorf("expected CachedTokens 30 (OpenAI nested wins), got %d", result.Usage.CachedTokens)
	}
	if result.Usage.CacheCreationTokens != 512 {
		t.Errorf("expected CacheCreationTokens 512, got %d", result.Usage.CacheCreationTokens)
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
