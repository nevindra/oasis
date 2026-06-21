package guardrail

import (
	"context"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestTokenBudgetTrimsOldest(t *testing.T) {
	g := NewTokenBudgetGuard(50) // 50-token budget
	long := strings.Repeat("x", 300)
	req := core.ChatRequest{Messages: []core.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: long}, // oldest user turn — should be trimmed
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "newest"}, // preserved (most recent)
	}}
	if err := g.PreLLM(context.Background(), &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Messages[0].Role != "system" {
		t.Errorf("system message must survive, got role %q", req.Messages[0].Role)
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Content != "newest" {
		t.Errorf("most recent message must survive, got %q", last.Content)
	}
	for _, m := range req.Messages {
		if m.Content == long {
			t.Error("oldest oversized message should have been trimmed")
		}
	}
}

func TestTokenBudgetNoOrphanToolResult(t *testing.T) {
	g := NewTokenBudgetGuard(1) // force aggressive trim
	req := core.ChatRequest{Messages: []core.ChatMessage{
		{Role: "assistant", Content: "", ToolCalls: []core.ToolCall{{ID: "c1", Name: "search"}}},
		{Role: "tool", Content: strings.Repeat("y", 200), ToolCallID: "c1"},
		{Role: "user", Content: "hi"},
	}}
	if err := g.PreLLM(context.Background(), &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A tool-result message (ToolCallID != "") must never lead the slice.
	if len(req.Messages) > 0 && req.Messages[0].ToolCallID != "" {
		t.Errorf("leading orphan tool result: %+v", req.Messages[0])
	}
}

func TestTokenBudgetNoOrphanAfterSystem(t *testing.T) {
	// Regression: the old leading-only orphan cleanup stripped tool-results only
	// at index 0. When a system message occupies index 0, an orphaned tool-result
	// anywhere else in the slice survives undetected. Providers reject such
	// malformed requests.
	//
	// Layout before trim (5 messages):
	//   [system, assistant(tc=c1), user(large), tool-result(c1), user(newest)]
	// PreserveLast(3) protects the last 3: [user(large), tool-result(c1), user(newest)].
	// Trimmable window is indices 0–1 (cutoff=2), skipping system at 0.
	// Budget loop trims assistant at index 1 → only one pass possible before
	// cutoff shrinks to 1 and firstTrimmable returns -1.
	// Result after budget loop:
	//   [system, user(large), tool-result(c1), user(newest)]
	// The old cleanup checks only index 0 (system, ToolCallID=="") → no-op.
	// The orphaned tool-result at index 2 survives. The fix must remove it.
	g := NewTokenBudgetGuard(1, PreserveLast(3)) // protect last 3; force trim
	req := core.ChatRequest{Messages: []core.ChatMessage{
		{Role: "system", Content: "s"},
		{Role: "assistant", Content: "", ToolCalls: []core.ToolCall{{ID: "c1", Name: "search"}}},
		{Role: "user", Content: strings.Repeat("z", 200)}, // in protect window (index 2 of 5, last-3)
		{Role: "tool", Content: "t", ToolCallID: "c1"},    // in protect window
		{Role: "user", Content: "hi"},                     // in protect window (newest)
	}}
	if err := g.PreLLM(context.Background(), &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Build set of tool-call IDs still present in remaining messages.
	present := make(map[string]struct{})
	for _, m := range req.Messages {
		for _, tc := range m.ToolCalls {
			present[tc.ID] = struct{}{}
		}
	}
	// Every tool-result must have its originating tool-call still present.
	for _, m := range req.Messages {
		if m.ToolCallID == "" {
			continue
		}
		if _, ok := present[m.ToolCallID]; !ok {
			t.Errorf("orphaned tool-result with ToolCallID=%q; no matching tool call in remaining messages: %+v", m.ToolCallID, req.Messages)
		}
	}
}

func TestTokenBudgetUnderBudgetNoOp(t *testing.T) {
	g := NewTokenBudgetGuard(10000)
	orig := []core.ChatMessage{{Role: "user", Content: "short"}}
	req := core.ChatRequest{Messages: append([]core.ChatMessage(nil), orig...)}
	if err := g.PreLLM(context.Background(), &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(req.Messages) != 1 {
		t.Errorf("under-budget input must be untouched, got %d messages", len(req.Messages))
	}
}
