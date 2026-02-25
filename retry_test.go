package oasis

import (
	"context"
	"testing"
	"time"
)

// stubProvider is a test Provider that returns pre-configured results in order.
// All three methods share the same result queue via a shared call counter.
type stubProvider struct {
	calls   int
	results []stubResult
}

type stubResult struct {
	resp   ChatResponse
	tokens []string // tokens written to ch in ChatStream
	err    error
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) next() stubResult {
	i := s.calls
	s.calls++
	if i < len(s.results) {
		return s.results[i]
	}
	return stubResult{}
}

func (s *stubProvider) Chat(_ context.Context, _ ChatRequest) (ChatResponse, error) {
	r := s.next()
	return r.resp, r.err
}

func (s *stubProvider) ChatStream(_ context.Context, _ ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	r := s.next()
	for _, tok := range r.tokens {
		ch <- StreamEvent{Type: EventTextDelta, Content: tok}
	}
	return r.resp, r.err
}

var _ Provider = (*stubProvider)(nil)

// --- Chat tests ---

func TestWithRetry_Chat_SucceedsFirstAttempt(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{resp: ChatResponse{Content: "hello"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	resp, err := p.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("got %q, want %q", resp.Content, "hello")
	}
	if stub.calls != 1 {
		t.Errorf("got %d calls, want 1", stub.calls)
	}
}

func TestWithRetry_Chat_RetriesOn503(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 503, Body: "unavailable"}},
		{resp: ChatResponse{Content: "hello"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	resp, err := p.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("got %q, want %q", resp.Content, "hello")
	}
	if stub.calls != 2 {
		t.Errorf("got %d calls, want 2", stub.calls)
	}
}

func TestWithRetry_Chat_RetriesOn429(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 429, Body: "rate limited"}},
		{resp: ChatResponse{Content: "ok"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	_, err := p.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.calls != 2 {
		t.Errorf("got %d calls, want 2", stub.calls)
	}
}

func TestWithRetry_Chat_DoesNotRetryNonTransient(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 500, Body: "internal error"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	_, err := p.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if stub.calls != 1 {
		t.Errorf("got %d calls, want 1 (no retry for 500)", stub.calls)
	}
}

func TestWithRetry_Chat_ExhaustsMaxAttempts(t *testing.T) {
	transient := stubResult{err: &ErrHTTP{Status: 503, Body: "unavailable"}}
	stub := &stubProvider{results: []stubResult{transient, transient, transient, transient}}
	p := WithRetry(stub, RetryBaseDelay(0), RetryMaxAttempts(3))

	_, err := p.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error after max attempts, got nil")
	}
	if stub.calls != 3 {
		t.Errorf("got %d calls, want 3", stub.calls)
	}
}

// --- Chat with tools on request tests ---

func TestWithRetry_ChatWithToolsOnRequest_RetriesOn429(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 429}},
		{resp: ChatResponse{Content: "done"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	_, err := p.Chat(context.Background(), ChatRequest{
		Tools: []ToolDefinition{{Name: "test"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.calls != 2 {
		t.Errorf("got %d calls, want 2", stub.calls)
	}
}

// --- ChatStream tests ---

func TestWithRetry_ChatStream_RetriesOn503(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 503}},
		{tokens: []string{"hel", "lo"}, resp: ChatResponse{Content: "hello"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	ch := make(chan StreamEvent, 8)
	resp, err := p.ChatStream(context.Background(), ChatRequest{}, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("got content %q, want %q", resp.Content, "hello")
	}
	var got string
	for ev := range ch {
		got += ev.Content
	}
	if got != "hello" {
		t.Errorf("got tokens %q, want %q", got, "hello")
	}
	if stub.calls != 2 {
		t.Errorf("got %d calls, want 2", stub.calls)
	}
}

func TestWithRetry_ChatStream_NoRetryAfterTokensSent(t *testing.T) {
	// Tokens sent before 503 — must not retry (can't unsend tokens).
	stub := &stubProvider{results: []stubResult{
		{tokens: []string{"partial"}, err: &ErrHTTP{Status: 503}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	ch := make(chan StreamEvent, 8)
	_, err := p.ChatStream(context.Background(), ChatRequest{}, ch)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if stub.calls != 1 {
		t.Errorf("got %d calls, want 1 (no retry after tokens sent)", stub.calls)
	}
}

// --- RetryAfter tests ---

func TestWithRetry_Chat_RespectsRetryAfter(t *testing.T) {
	// Server says wait 100ms via Retry-After. Verify the retry waits at least that long
	// even when base delay is 0.
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 429, Body: "rate limited", RetryAfter: 100 * time.Millisecond}},
		{resp: ChatResponse{Content: "ok"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	start := time.Now()
	resp, err := p.Chat(context.Background(), ChatRequest{})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("got %q, want %q", resp.Content, "ok")
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("retry was too fast: %v, expected at least ~100ms from Retry-After", elapsed)
	}
	if stub.calls != 2 {
		t.Errorf("got %d calls, want 2", stub.calls)
	}
}

func TestWithRetry_ChatStream_RespectsRetryAfter(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 429, RetryAfter: 100 * time.Millisecond}},
		{tokens: []string{"ok"}, resp: ChatResponse{Content: "ok"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0))

	start := time.Now()
	ch := make(chan StreamEvent, 8)
	_, err := p.ChatStream(context.Background(), ChatRequest{}, ch)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("retry was too fast: %v, expected at least ~100ms from Retry-After", elapsed)
	}
}

// --- RetryTimeout tests ---

func TestWithRetry_Chat_TimeoutExceeded(t *testing.T) {
	// Two transient errors with 100ms Retry-After each. Timeout of 50ms should
	// cause the retry loop to give up after the first attempt's wait.
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 429, RetryAfter: 100 * time.Millisecond}},
		{err: &ErrHTTP{Status: 429, RetryAfter: 100 * time.Millisecond}},
		{resp: ChatResponse{Content: "ok"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0), RetryTimeout(50*time.Millisecond))

	_, err := p.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("expected error due to timeout, got nil")
	}
	// Should have made 1 call, then the timeout fires during the wait.
	if stub.calls > 2 {
		t.Errorf("got %d calls, expected at most 2 with 50ms timeout", stub.calls)
	}
}

func TestWithRetry_Chat_TimeoutAllowsSuccess(t *testing.T) {
	// One transient error with no Retry-After, generous timeout — should succeed.
	stub := &stubProvider{results: []stubResult{
		{err: &ErrHTTP{Status: 503}},
		{resp: ChatResponse{Content: "ok"}},
	}}
	p := WithRetry(stub, RetryBaseDelay(0), RetryTimeout(5*time.Second))

	resp, err := p.Chat(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("got %q, want %q", resp.Content, "ok")
	}
	if stub.calls != 2 {
		t.Errorf("got %d calls, want 2", stub.calls)
	}
}
