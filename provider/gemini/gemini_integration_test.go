package gemini

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nevindra/oasis"
)

const rateLimitDelay = 5 * time.Second

func skipIfNoAPIKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		key = os.Getenv("OASIS_LLM_API_KEY")
	}
	if key == "" {
		t.Skip("GEMINI_API_KEY or OASIS_LLM_API_KEY not set, skipping integration test")
	}
	return key
}

func TestIntegration(t *testing.T) {
	key := skipIfNoAPIKey(t)

	t.Run("Chat", func(t *testing.T) {
		g := New(key, "gemini-2.0-flash")

		resp, err := g.Chat(context.Background(), oasis.ChatRequest{
			Messages: []oasis.ChatMessage{
				{Role: "user", Content: "Reply with exactly: hello"},
			},
		})
		if err != nil {
			t.Fatalf("Chat failed: %v", err)
		}
		if resp.Content == "" {
			t.Fatal("expected non-empty response content")
		}
		t.Logf("response: %q", resp.Content)
		t.Logf("usage: input=%d output=%d", resp.Usage.InputTokens, resp.Usage.OutputTokens)
	})

	time.Sleep(rateLimitDelay)

	t.Run("ChatWithOptions", func(t *testing.T) {
		g := New(key, "gemini-2.0-flash",
			WithTemperature(0.5),
			WithTopP(0.8),
		)

		resp, err := g.Chat(context.Background(), oasis.ChatRequest{
			Messages: []oasis.ChatMessage{
				{Role: "user", Content: "Reply with exactly: configured"},
			},
		})
		if err != nil {
			t.Fatalf("Chat with options failed: %v", err)
		}
		if resp.Content == "" {
			t.Fatal("expected non-empty response content")
		}
		t.Logf("response: %q", resp.Content)
	})

	time.Sleep(rateLimitDelay)

	t.Run("StructuredOutput", func(t *testing.T) {
		g := New(key, "gemini-2.0-flash")

		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"name": {"type": "string"},
				"age": {"type": "integer"}
			},
			"required": ["name", "age"]
		}`)

		resp, err := g.Chat(context.Background(), oasis.ChatRequest{
			Messages: []oasis.ChatMessage{
				{Role: "user", Content: "Generate a fictional person with a name and age."},
			},
			ResponseSchema: &oasis.ResponseSchema{Schema: schema},
		})
		if err != nil {
			t.Fatalf("structured output failed: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal([]byte(resp.Content), &result); err != nil {
			t.Fatalf("response is not valid JSON: %v\nraw: %q", err, resp.Content)
		}
		if _, ok := result["name"]; !ok {
			t.Error("expected 'name' field in structured response")
		}
		if _, ok := result["age"]; !ok {
			t.Error("expected 'age' field in structured response")
		}
		t.Logf("structured response: %s", resp.Content)
	})

	time.Sleep(rateLimitDelay)

	t.Run("StructuredOutputDisabled", func(t *testing.T) {
		g := New(key, "gemini-2.0-flash", WithStructuredOutput(false))

		schema := json.RawMessage(`{"type": "object", "properties": {"name": {"type": "string"}}}`)

		resp, err := g.Chat(context.Background(), oasis.ChatRequest{
			Messages: []oasis.ChatMessage{
				{Role: "user", Content: "Reply with exactly: free text"},
			},
			ResponseSchema: &oasis.ResponseSchema{Schema: schema},
		})
		if err != nil {
			t.Fatalf("chat with disabled structured output failed: %v", err)
		}
		if resp.Content == "" {
			t.Fatal("expected non-empty response")
		}
		t.Logf("response (structured output disabled): %q", resp.Content)
	})

	time.Sleep(rateLimitDelay)

	t.Run("ChatStream", func(t *testing.T) {
		g := New(key, "gemini-2.0-flash")

		ch := make(chan oasis.StreamEvent, 100)
		var chunks []oasis.StreamEvent

		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range ch {
				chunks = append(chunks, ev)
			}
		}()

		resp, err := g.ChatStream(context.Background(), oasis.ChatRequest{
			Messages: []oasis.ChatMessage{
				{Role: "user", Content: "Count from 1 to 5."},
			},
		}, ch)
		<-done

		if err != nil {
			t.Fatalf("ChatStream failed: %v", err)
		}
		if resp.Content == "" {
			t.Fatal("expected non-empty streamed content")
		}
		if len(chunks) == 0 {
			t.Fatal("expected at least 1 streamed chunk")
		}
		t.Logf("streamed %d chunks, final content: %q", len(chunks), resp.Content)
	})

	time.Sleep(rateLimitDelay)

	t.Run("ChatWithTools", func(t *testing.T) {
		g := New(key, "gemini-2.0-flash")

		tools := []oasis.ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get current weather for a city",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string","description":"City name"}},"required":["city"]}`),
			},
		}

		resp, err := g.ChatWithTools(context.Background(), oasis.ChatRequest{
			Messages: []oasis.ChatMessage{
				{Role: "user", Content: "What's the weather in Tokyo?"},
			},
		}, tools)
		if err != nil {
			t.Fatalf("ChatWithTools failed: %v", err)
		}

		if len(resp.ToolCalls) == 0 {
			t.Fatalf("expected tool calls, got content: %q", resp.Content)
		}
		tc := resp.ToolCalls[0]
		if tc.Name != "get_weather" {
			t.Errorf("expected tool call 'get_weather', got %q", tc.Name)
		}
		t.Logf("tool call: %s(%s)", tc.Name, string(tc.Args))
	})

	time.Sleep(rateLimitDelay)

	t.Run("BatchChat", func(t *testing.T) {
		g := New(key, "gemini-2.0-flash")

		requests := []oasis.ChatRequest{
			{Messages: []oasis.ChatMessage{{Role: "user", Content: "Reply with exactly: batch1"}}},
			{Messages: []oasis.ChatMessage{{Role: "user", Content: "Reply with exactly: batch2"}}},
		}

		job, err := g.BatchChat(context.Background(), requests)
		if err != nil {
			t.Fatalf("BatchChat failed: %v", err)
		}
		t.Logf("batch job created: id=%s state=%s", job.ID, job.State)

		if job.ID == "" {
			t.Fatal("expected non-empty job ID")
		}

		// Verify we can poll status (don't wait for completion — batch jobs take minutes to hours).
		status, err := g.BatchStatus(context.Background(), job.ID)
		if err != nil {
			t.Fatalf("BatchStatus failed: %v", err)
		}
		t.Logf("status: state=%s stats=%+v", status.State, status.Stats)

		// Cancel the job so it doesn't run to completion.
		if err := g.BatchCancel(context.Background(), job.ID); err != nil {
			t.Logf("BatchCancel (best-effort): %v", err)
		}
	})
}
