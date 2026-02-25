package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nevindra/oasis"
)

func TestProvider_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request.
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected path /chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content-type: %s", r.Header.Get("Content-Type"))
		}

		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "gpt-4o" {
			t.Errorf("expected model gpt-4o, got %s", req.Model)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{
			ID: "chatcmpl-1",
			Choices: []Choice{{
				Index:   0,
				Message: &ChoiceMessage{Role: "assistant", Content: "Hello!"},
			}},
			Usage: &Usage{PromptTokens: 5, CompletionTokens: 2},
		})
	}))
	defer srv.Close()

	p := NewProvider("test-key", "gpt-4o", srv.URL)

	resp, err := p.Chat(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}

	if resp.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %q", resp.Content)
	}
	if resp.Usage.InputTokens != 5 {
		t.Errorf("expected 5 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 2 {
		t.Errorf("expected 2 output tokens, got %d", resp.Usage.OutputTokens)
	}
}

func TestProvider_ChatWithToolsOnRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if len(req.Tools) != 1 {
			t.Fatalf("expected 1 tool, got %d", len(req.Tools))
		}
		if req.Tools[0].Function.Name != "get_weather" {
			t.Errorf("expected tool name 'get_weather', got %q", req.Tools[0].Function.Name)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{
			ID: "chatcmpl-2",
			Choices: []Choice{{
				Index: 0,
				Message: &ChoiceMessage{
					Role: "assistant",
					ToolCalls: []ToolCallRequest{{
						ID:   "call_abc",
						Type: "function",
						Function: FunctionCall{
							Name:      "get_weather",
							Arguments: `{"city":"London"}`,
						},
					}},
				},
			}},
			Usage: &Usage{PromptTokens: 10, CompletionTokens: 8},
		})
	}))
	defer srv.Close()

	p := NewProvider("test-key", "gpt-4o", srv.URL)

	tools := []oasis.ToolDefinition{{
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
	}}

	resp, err := p.Chat(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{{Role: "user", Content: "Weather in London?"}},
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("Chat with tools returned error: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "get_weather" {
		t.Errorf("expected tool call name 'get_weather', got %q", resp.ToolCalls[0].Name)
	}

	var args map[string]any
	if err := json.Unmarshal(resp.ToolCalls[0].Args, &args); err != nil {
		t.Fatalf("failed to parse args: %v", err)
	}
	if args["city"] != "London" {
		t.Errorf("expected city 'London', got %v", args["city"])
	}
}

func TestProvider_ChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if !req.Stream {
			t.Error("expected stream=true")
		}
		if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Error("expected stream_options.include_usage=true")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)

		chunks := []string{
			`data: {"id":"chatcmpl-3","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
			`data: {"id":"chatcmpl-3","choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
			`data: {"id":"chatcmpl-3","choices":[{"index":0,"delta":{"content":" world"}}]}`,
			`data: {"id":"chatcmpl-3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
			`data: [DONE]`,
		}

		for _, chunk := range chunks {
			w.Write([]byte(chunk + "\n\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	p := NewProvider("test-key", "gpt-4o", srv.URL)

	ch := make(chan oasis.StreamEvent, 10)
	resp, err := p.ChatStream(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{{Role: "user", Content: "Hi"}},
	}, ch)
	if err != nil {
		t.Fatalf("ChatStream returned error: %v", err)
	}

	// Drain events.
	var deltas []string
	for ev := range ch {
		if ev.Type == oasis.EventTextDelta {
			deltas = append(deltas, ev.Content)
		}
	}

	if resp.Content != "Hello world" {
		t.Errorf("expected content 'Hello world', got %q", resp.Content)
	}
	if len(deltas) != 2 {
		t.Errorf("expected 2 text deltas, got %d", len(deltas))
	}
	if resp.Usage.InputTokens != 5 {
		t.Errorf("expected 5 input tokens, got %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 2 {
		t.Errorf("expected 2 output tokens, got %d", resp.Usage.OutputTokens)
	}
}

func TestProvider_ChatStream_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	p := NewProvider("test-key", "gpt-4o", srv.URL)

	ch := make(chan oasis.StreamEvent, 10)
	_, err := p.ChatStream(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{{Role: "user", Content: "Hi"}},
	}, ch)

	if err == nil {
		t.Fatal("expected error for 429 response")
	}

	httpErr, ok := err.(*oasis.ErrHTTP)
	if !ok {
		t.Fatalf("expected *oasis.ErrHTTP, got %T", err)
	}
	if httpErr.Status != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", httpErr.Status)
	}

	// Channel must be closed even on error.
	_, open := <-ch
	if open {
		t.Error("expected channel to be closed on error")
	}
}

func TestProvider_Chat_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer srv.Close()

	p := NewProvider("test-key", "gpt-4o", srv.URL)

	_, err := p.Chat(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{{Role: "user", Content: "Hi"}},
	})

	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	httpErr, ok := err.(*oasis.ErrHTTP)
	if !ok {
		t.Fatalf("expected *oasis.ErrHTTP, got %T", err)
	}
	if httpErr.Status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", httpErr.Status)
	}
}

func TestProvider_Name(t *testing.T) {
	p := NewProvider("key", "model", "http://localhost")
	if p.Name() != "openai" {
		t.Errorf("expected default name 'openai', got %q", p.Name())
	}

	p = NewProvider("key", "model", "http://localhost", WithName("groq"))
	if p.Name() != "groq" {
		t.Errorf("expected name 'groq', got %q", p.Name())
	}
}

func TestProvider_NoAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("expected no auth header for empty API key")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{
			ID: "chatcmpl-4",
			Choices: []Choice{{
				Index:   0,
				Message: &ChoiceMessage{Role: "assistant", Content: "OK"},
			}},
		})
	}))
	defer srv.Close()

	// Ollama and other local providers don't need API keys.
	p := NewProvider("", "llama3", srv.URL)

	resp, err := p.Chat(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Content != "OK" {
		t.Errorf("expected content 'OK', got %q", resp.Content)
	}
}

func TestProvider_WithOptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		if req.Temperature == nil || *req.Temperature != 0.7 {
			t.Errorf("expected temperature 0.7, got %v", req.Temperature)
		}
		if req.MaxTokens != 2048 {
			t.Errorf("expected max_tokens 2048, got %d", req.MaxTokens)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ChatResponse{
			ID: "chatcmpl-5",
			Choices: []Choice{{
				Index:   0,
				Message: &ChoiceMessage{Role: "assistant", Content: "OK"},
			}},
		})
	}))
	defer srv.Close()

	p := NewProvider("key", "gpt-4o", srv.URL,
		WithOptions(WithTemperature(0.7), WithMaxTokens(2048)),
	)

	_, err := p.Chat(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
}
