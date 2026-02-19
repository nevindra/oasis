package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubStreamingAgent implements StreamingAgent for testing.
type stubStreamingAgent struct {
	name   string
	desc   string
	events []StreamEvent
	result AgentResult
	err    error
}

func (s *stubStreamingAgent) Name() string        { return s.name }
func (s *stubStreamingAgent) Description() string { return s.desc }
func (s *stubStreamingAgent) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return s.result, s.err
}
func (s *stubStreamingAgent) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	defer close(ch)
	for _, ev := range s.events {
		select {
		case ch <- ev:
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}
	return s.result, s.err
}

func TestServeSSE(t *testing.T) {
	agent := &stubStreamingAgent{
		name: "test",
		desc: "test agent",
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "Hello"},
			{Type: EventTextDelta, Content: " world"},
			{Type: EventToolCallStart, Name: "search", Args: json.RawMessage(`{"q":"test"}`)},
			{Type: EventToolCallResult, Name: "search", Content: "found it"},
		},
		result: AgentResult{Output: "Hello world"},
	}

	rec := httptest.NewRecorder()
	task := AgentTask{Input: "say hello"}

	result, err := ServeSSE(context.Background(), rec, agent, task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "Hello world" {
		t.Errorf("result.Output = %q, want %q", result.Output, "Hello world")
	}

	// Check headers.
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
	}

	body := rec.Body.String()

	// Verify all 4 events are present.
	if strings.Count(body, "event: ") != 5 { // 4 stream events + 1 done
		t.Errorf("expected 5 event lines, got %d in:\n%s", strings.Count(body, "event: "), body)
	}

	// Verify event types appear in order.
	events := []string{"event: text-delta", "event: tool-call-start", "event: tool-call-result", "event: done"}
	pos := 0
	for _, ev := range events {
		idx := strings.Index(body[pos:], ev)
		if idx < 0 {
			t.Errorf("missing %q after position %d in body:\n%s", ev, pos, body)
			break
		}
		pos += idx + len(ev)
	}

	// Verify done event.
	if !strings.Contains(body, "event: done\ndata: [DONE]") {
		t.Errorf("missing done event in body:\n%s", body)
	}
}

func TestServeSSE_AgentError(t *testing.T) {
	agent := &stubStreamingAgent{
		name: "fail",
		desc: "fails",
		events: []StreamEvent{
			{Type: EventTextDelta, Content: "partial"},
		},
		err: errors.New("provider timeout"),
	}

	rec := httptest.NewRecorder()
	task := AgentTask{Input: "fail"}

	_, err := ServeSSE(context.Background(), rec, agent, task)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "provider timeout" {
		t.Errorf("err = %q, want %q", err.Error(), "provider timeout")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Errorf("missing error event in body:\n%s", body)
	}
	if !strings.Contains(body, "provider timeout") {
		t.Errorf("missing error message in body:\n%s", body)
	}
}

// nonFlusher is a ResponseWriter that does not implement http.Flusher.
type nonFlusher struct {
	header http.Header
}

func (n *nonFlusher) Header() http.Header        { return n.header }
func (n *nonFlusher) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusher) WriteHeader(int)             {}

func TestServeSSE_NoFlusher(t *testing.T) {
	agent := &stubStreamingAgent{name: "test", desc: "test"}
	w := &nonFlusher{header: http.Header{}}

	_, err := ServeSSE(context.Background(), w, agent, AgentTask{})
	if err == nil {
		t.Fatal("expected error for non-flusher ResponseWriter")
	}
	if !strings.Contains(err.Error(), "Flusher") {
		t.Errorf("err = %q, want mention of Flusher", err.Error())
	}
}
