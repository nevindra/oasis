package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
)

// --- scriptedProvider ---

// scriptedProvider returns canned responses in order (ChatStream-based).
// After the script is exhausted, returns an error.
type scriptedProvider struct {
	capturedRequestProvider
	mu        sync.Mutex
	responses []ChatResponse
	idx       int
}

func (p *scriptedProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	p.capturedRequestProvider.mu.Lock()
	p.capturedRequestProvider.reqs = append(p.capturedRequestProvider.reqs, req)
	p.capturedRequestProvider.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idx >= len(p.responses) {
		return ChatResponse{}, errors.New("scripted provider: out of responses")
	}
	resp := p.responses[p.idx]
	p.idx++
	return resp, nil
}

// --- scriptedRepeatingProvider ---

// scriptedRepeatingProvider always returns the same response (no tool calls →
// loop terminates after first iteration).
type scriptedRepeatingProvider struct {
	capturedRequestProvider
	response ChatResponse
}

func (p *scriptedRepeatingProvider) ChatStream(_ context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	defer close(ch)
	p.capturedRequestProvider.mu.Lock()
	p.capturedRequestProvider.reqs = append(p.capturedRequestProvider.reqs, req)
	p.capturedRequestProvider.mu.Unlock()
	return p.response, nil
}

// --- Test 14 ---

// TestIntegration_ValidationLoop demonstrates OnIterationComplete used as
// a re-prompt loop: the agent loop continues until the response matches
// a validation predicate.
func TestIntegration_ValidationLoop(t *testing.T) {
	responses := []ChatResponse{
		{Content: "42"},
		{Content: "the answer is 42"},
		{Content: "42 is the answer to life, the universe, and everything"},
	}
	provider := &scriptedProvider{responses: responses}

	called := 0
	hook := func(ctx context.Context, iter int, snap *IterationSnapshot) (IterationDecision, error) {
		called++
		// Require the response to contain the word "universe".
		if strings.Contains(snap.Response.Content, "universe") {
			return Stop(AgentResult{Output: snap.Response.Content}), nil
		}
		return InjectFeedback("be more elaborate — explain why 42"), nil
	}

	a := NewLLMAgent("validator", "validates", provider,
		WithOnIterationComplete(hook))

	result, err := a.Execute(context.Background(), AgentTask{Input: "what is the answer?"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result.Output, "universe") {
		t.Fatalf("Output = %q, want containing 'universe'", result.Output)
	}
	if called != 3 {
		t.Fatalf("hook called %d times, want 3", called)
	}
}

// --- Test 15 ---

// TestIntegration_OnError_RetryWithFeedback demonstrates OnError used to
// recover from a transient/feedback-prompted error.
func TestIntegration_OnError_RetryWithFeedback(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	provider := &flakyProvider{errFn: func() error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return errors.New("invalid tool name")
		}
		return nil
	}}

	hook := func(ctx context.Context, iter int, err error) (ErrorDecision, error) {
		if strings.Contains(err.Error(), "invalid tool") {
			return RetryWithFeedback("Tools available: search, summarize. Use one of those."), nil
		}
		return Propagate(), nil
	}

	a := NewLLMAgent("recover", "recovers", provider, WithOnError(hook))

	_, err := a.Execute(context.Background(), AgentTask{Input: "x"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Verify that the retry message reached the LLM via captured request.
	captured := provider.last()
	found := false
	for _, m := range captured.Messages {
		if m.Role == core.RoleUser && strings.Contains(m.Content, "Tools available") {
			found = true
		}
	}
	if !found {
		t.Fatalf("RetryWithFeedback: message not in retry history. Messages: %v", captured.Messages)
	}

	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("provider calls = %d, want 2 (1 fail + 1 retry)", gotCalls)
	}
}

// --- Test 16 ---

// TestIntegration_MultiTenantMemorySwap runs concurrent ExecuteWith calls
// with distinct memory orchestrators per tenant; verifies no
// cross-contamination. Run with -race to confirm thread safety.
func TestIntegration_MultiTenantMemorySwap(t *testing.T) {
	a := NewLLMAgent("multi", "tenant",
		&scriptedRepeatingProvider{response: ChatResponse{Content: "ack"}})

	const N = 20
	stores := make([]*recordingStore, N)
	mems := make([]*memory.AgentMemory, N)
	for i := 0; i < N; i++ {
		stores[i] = &recordingStore{}
		var m memory.AgentMemory
		m.Init(memory.AgentMemoryConfig{Store: stores[i]})
		mems[i] = &m
	}

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			task := AgentTask{
				Input:    fmt.Sprintf("hello from tenant %d", idx),
				ThreadID: fmt.Sprintf("t-%d", idx),
			}
			_, err := a.ExecuteWith(context.Background(), task, &RunOptions{Memory: mems[idx]})
			if err != nil {
				t.Errorf("tenant %d: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	// Flush background persist goroutines before inspecting stores.
	for _, m := range mems {
		if err := m.Close(); err != nil {
			t.Errorf("mem.Close: %v", err)
		}
	}

	// Each store should have received at least one message write.
	for i, s := range stores {
		msgs := s.storedMessages()
		if len(msgs) == 0 {
			t.Errorf("tenant %d store: 0 writes, want > 0", i)
			continue
		}
		// Verify cross-tenant isolation: no other tenant's ThreadID should appear
		// in this store. The ThreadID is "t-N" (e.g. "t-3"), which is short and
		// unambiguous — no substring overlap among {t-0 … t-19}.
		for _, msg := range msgs {
			for j := 0; j < N; j++ {
				if j == i {
					continue
				}
				if msg.ThreadID == fmt.Sprintf("t-%d", j) {
					t.Errorf("tenant %d store contains message with ThreadID of tenant %d",
						i, j)
				}
			}
		}
	}

	// Total writes across all stores should equal N tenants worth.
	total := 0
	for _, s := range stores {
		total += len(s.storedMessages())
	}
	if total == 0 {
		t.Errorf("total writes across all stores = 0, want > 0")
	}
}

// --- Sources regression test ---
//
// sourcedTool implements core.Sourced. The agent loop is supposed to collect
// these sources onto AgentResult.Sources via cfg.lookupTool. Before the
// BaseLoopConfig unification, buildLoopConfigFrom omitted lookupTool, so
// ExecuteWith / ExecuteStreamWith silently dropped sources. This test guards
// against that drift returning.
type sourcedTool struct {
	srcs []core.Source
	out  string
}

func (s *sourcedTool) Name() string { return "search" }
func (s *sourcedTool) Definition() ToolDefinition {
	return ToolDefinition{Name: "search", Description: "search", Parameters: []byte(`{"type":"object"}`)}
}
func (s *sourcedTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: []byte(`"` + s.out + `"`)}, nil
}
func (s *sourcedTool) Sources() []core.Source { return s.srcs }

func TestExecuteWith_PopulatesSources(t *testing.T) {
	tool := &sourcedTool{
		srcs: []core.Source{{URL: "https://example.com", Title: "Example", Origin: "tool:search"}},
		out:  "found",
	}
	provider := &scriptedProvider{
		responses: []ChatResponse{
			{ToolCalls: []ToolCall{{ID: "1", Name: "search", Args: []byte(`{}`)}}},
			{Content: "done"},
		},
	}
	a := NewLLMAgent("a", "d", provider, WithTools(tool))

	prompt := "override"
	result, err := a.ExecuteWith(context.Background(), AgentTask{Input: "q"}, &RunOptions{Prompt: &prompt})
	if err != nil {
		t.Fatalf("ExecuteWith: %v", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("Sources: want 1, got %d (result=%+v)", len(result.Sources), result.Sources)
	}
	if result.Sources[0].URL != "https://example.com" {
		t.Errorf("Sources[0].URL: want https://example.com, got %q", result.Sources[0].URL)
	}
}
