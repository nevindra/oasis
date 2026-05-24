package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nevindra/oasis/core"
)

// fakeCompressorCompactor is a test-only Compactor that records all Compact calls.
type fakeCompressorCompactor struct {
	mu      sync.Mutex
	scopes  []core.CompactScope
	summary string
}

func (f *fakeCompressorCompactor) Compact(_ context.Context, req core.CompactRequest) (core.CompactResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scopes = append(f.scopes, req.Scope)
	return core.CompactResult{SummaryText: f.summary}, nil
}

func (f *fakeCompressorCompactor) recordedScopes() []core.CompactScope {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]core.CompactScope, len(f.scopes))
	copy(out, f.scopes)
	return out
}

// TestCompressMessagesRoutesThroughCompactor verifies that compressMessages
// delegates to the configured Compactor with ScopeToolResultsOnly rather than
// making a direct LLM call.
func TestCompressMessagesRoutesThroughCompactor(t *testing.T) {
	fake := &fakeCompressorCompactor{summary: "compacted by fake"}

	cfg := LoopConfig{
		Name:       "test",
		Compressor: fake,
		Config:     Config{Logger: nopLogger},
	}

	// Build a message slice that has compressible content:
	//   [0] user initial message
	//   [1] assistant with tool call (iteration 1)
	//   [2] tool result (iteration 1)
	//   [3] assistant with tool call (iteration 2)
	//   [4] tool result (iteration 2)
	//
	// With preserveIters=1, iteration 1 (indices 1-2) will be compressed;
	// iteration 2 (indices 3-4) is preserved.
	msgs := []core.ChatMessage{
		core.UserMessage("initial"),
		{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "tc1", Name: "tool"}}},
		{Role: "user", ToolCallID: "tc1", Content: "result of tc1"},
		{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "tc2", Name: "tool"}}},
		{Role: "user", ToolCallID: "tc2", Content: "result of tc2"},
	}

	compressed, _ := compressMessages(context.Background(), cfg, AgentTask{}, msgs, 1, 100)

	// The fake compactor should have been called exactly once.
	scopes := fake.recordedScopes()
	if len(scopes) != 1 {
		t.Fatalf("expected 1 Compact call, got %d", len(scopes))
	}

	// It must be called with ScopeToolResultsOnly.
	if scopes[0] != core.ScopeToolResultsOnly {
		t.Errorf("Compact called with scope %v, want ScopeToolResultsOnly", scopes[0])
	}

	// The summary text must appear in the compressed messages.
	var foundSummary bool
	for _, m := range compressed {
		if strings.Contains(m.Content, "compacted by fake") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Errorf("compressed messages do not contain the summary text; messages: %+v", compressed)
	}
}

// TestCompressMessagesFallbackToInlineCompactor verifies that when no Compactor
// is configured, compressMessages falls back to NewInlineCompactor using the
// loop provider.
func TestCompressMessagesFallbackToInlineCompactor(t *testing.T) {
	// We use a provider that returns a known string when called.
	fakeProvider := &mockProvider{
		name:      "fallback-compress",
		responses: makeChatResponses(5, "inline-compacted"),
	}

	cfg := LoopConfig{
		Name:       "test-fallback",
		Provider:   fakeProvider,
		Compressor: nil, // no compressor configured — should fall back
		Config:     Config{Logger: nopLogger},
	}

	msgs := []core.ChatMessage{
		core.UserMessage("initial"),
		{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "tc1", Name: "tool"}}},
		{Role: "user", ToolCallID: "tc1", Content: "big tool result"},
		{Role: "assistant", ToolCalls: []core.ToolCall{{ID: "tc2", Name: "tool"}}},
		{Role: "user", ToolCallID: "tc2", Content: "another result"},
	}

	compressed, _ := compressMessages(context.Background(), cfg, AgentTask{}, msgs, 1, 100)

	// The inline compactor's LLM call should produce a summary message.
	var foundSummary bool
	for _, m := range compressed {
		if strings.Contains(m.Content, "inline-compacted") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Errorf("fallback inline compactor did not produce expected summary; messages: %+v", compressed)
	}
}

// TestCompressMessagesNoOpWhenNothingToCompress ensures the function returns
// the original slice unchanged when no compressible messages exist.
func TestCompressMessagesNoOpWhenNothingToCompress(t *testing.T) {
	fake := &fakeCompressorCompactor{summary: "should not be called"}

	cfg := LoopConfig{
		Name:       "test-noop",
		Compressor: fake,
		Config:     Config{Logger: nopLogger},
	}

	// Only user messages — no tool results to compress.
	msgs := []core.ChatMessage{
		core.UserMessage("hello"),
		{Role: "assistant", Content: "world"},
	}

	compressed, count := compressMessages(context.Background(), cfg, AgentTask{}, msgs, 1, 42)

	if len(fake.recordedScopes()) != 0 {
		t.Error("Compact should not be called when there is nothing to compress")
	}
	if len(compressed) != len(msgs) {
		t.Errorf("message count changed: got %d, want %d", len(compressed), len(msgs))
	}
	if count != 42 {
		t.Errorf("rune count changed: got %d, want 42", count)
	}
}

// makeChatResponses returns n ChatResponse values all with the given content.
func makeChatResponses(n int, content string) []core.ChatResponse {
	out := make([]core.ChatResponse, n)
	for i := range out {
		out[i] = core.ChatResponse{Content: content}
	}
	return out
}
