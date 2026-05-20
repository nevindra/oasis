package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// stubResult is a single scripted response from stubProvider.
// Copied from the root package's test helpers (retry_test.go); kept
// private to this module so ratelimit's tests are self-contained.
type stubResult struct {
	resp   core.ChatResponse
	tokens []string // tokens written to ch in ChatStream
	err    error
}

// stubProvider returns canned responses in order.
// All three methods share the same result queue via a shared call counter.
type stubProvider struct {
	calls   int
	results []stubResult
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

func (s *stubProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	r := s.next()
	for _, tok := range r.tokens {
		ch <- core.StreamEvent{Type: core.EventTextDelta, Content: tok}
	}
	return r.resp, r.err
}

var _ core.Provider = (*stubProvider)(nil)

// --- RPM tests (Task 4) ---

func TestWithRateLimit_RPM_AllowsWithinLimit(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{resp: core.ChatResponse{Content: "a"}},
		{resp: core.ChatResponse{Content: "b"}},
	}}
	p := WithRateLimit(stub, RPM(60))

	resp, err := core.Chat(context.Background(), p, core.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "a" {
		t.Errorf("got %q, want %q", resp.Content, "a")
	}
}

func TestWithRateLimit_RPM_BlocksWhenExceeded(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{resp: core.ChatResponse{Content: "a"}},
		{resp: core.ChatResponse{Content: "b"}},
	}}
	// RPM(1) = 1 request per minute. Second call should block.
	p := WithRateLimit(stub, RPM(1))

	_, err := core.Chat(context.Background(), p, core.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// Second call with a short-lived context should timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = core.Chat(ctx, p, core.ChatRequest{})
	if err == nil {
		t.Fatal("expected context deadline exceeded, got nil")
	}
}

func TestWithRateLimit_Name(t *testing.T) {
	stub := &stubProvider{}
	p := WithRateLimit(stub, RPM(10))
	if p.Name() != "stub" {
		t.Errorf("Name() = %q, want %q", p.Name(), "stub")
	}
}

// --- TPM tests (Task 5) ---

func TestWithRateLimit_TPM_AllowsWithinLimit(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{resp: core.ChatResponse{Content: "a", Usage: core.Usage{InputTokens: 100, OutputTokens: 50}}},
		{resp: core.ChatResponse{Content: "b", Usage: core.Usage{InputTokens: 100, OutputTokens: 50}}},
	}}
	p := WithRateLimit(stub, TPM(1000))

	// First call: 150 tokens, well within 1000 TPM.
	_, err := core.Chat(context.Background(), p, core.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Second call: 300 total, still within 1000.
	_, err = core.Chat(context.Background(), p, core.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if stub.calls != 2 {
		t.Errorf("got %d calls, want 2", stub.calls)
	}
}

func TestWithRateLimit_TPM_BlocksWhenExceeded(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{resp: core.ChatResponse{Content: "a", Usage: core.Usage{InputTokens: 500, OutputTokens: 500}}},
		{resp: core.ChatResponse{Content: "b", Usage: core.Usage{InputTokens: 100, OutputTokens: 100}}},
	}}
	// TPM(1000). First call uses 1000 tokens = at limit.
	p := WithRateLimit(stub, TPM(1000))

	_, err := core.Chat(context.Background(), p, core.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// Second call should block (1000 tokens already used in this minute).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = core.Chat(ctx, p, core.ChatRequest{})
	if err == nil {
		t.Fatal("expected context deadline exceeded, got nil")
	}
}

func TestWithRateLimit_RPMAndTPM(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{resp: core.ChatResponse{Content: "a", Usage: core.Usage{InputTokens: 10, OutputTokens: 10}}},
		{resp: core.ChatResponse{Content: "b", Usage: core.Usage{InputTokens: 10, OutputTokens: 10}}},
	}}
	// RPM high, TPM low — TPM should be the bottleneck after first call fills budget.
	p := WithRateLimit(stub, RPM(100), TPM(20))

	_, err := core.Chat(context.Background(), p, core.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// First call used 20 tokens = at TPM limit. Second should block.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = core.Chat(ctx, p, core.ChatRequest{})
	if err == nil {
		t.Fatal("expected timeout due to TPM limit")
	}
}

// --- Chat with tools and ChatStream tests ---

func TestWithRateLimit_ChatWithToolsOnRequest(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{resp: core.ChatResponse{Content: "ok", Usage: core.Usage{InputTokens: 50, OutputTokens: 50}}},
	}}
	p := WithRateLimit(stub, RPM(60))

	resp, err := core.Chat(context.Background(), p, core.ChatRequest{
		Tools: []core.ToolDefinition{{Name: "test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Errorf("got %q, want %q", resp.Content, "ok")
	}
}

func TestWithRateLimit_ChatStream(t *testing.T) {
	stub := &stubProvider{results: []stubResult{
		{tokens: []string{"hel", "lo"}, resp: core.ChatResponse{Content: "hello", Usage: core.Usage{InputTokens: 30, OutputTokens: 20}}},
	}}
	p := WithRateLimit(stub, RPM(60), TPM(1000))

	ch := make(chan core.StreamEvent, 8)
	resp, err := p.ChatStream(context.Background(), core.ChatRequest{}, ch)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "hello" {
		t.Errorf("got %q, want %q", resp.Content, "hello")
	}
	var got string
	for ev := range ch {
		got += ev.Content
	}
	if got != "hello" {
		t.Errorf("streamed %q, want %q", got, "hello")
	}
}
