package guardrail

import (
	"context"
	"strings"
	"testing"

	"github.com/nevindra/oasis/core"
)

// TestRedactionGuardImplementsInterfaces verifies at test-compile time that
// RedactionGuard satisfies all three processor interfaces. The var-nil-pointer
// pattern is the idiomatic Go compile-time assertion; this test function makes
// the intent visible in the test suite alongside the assertions in redaction.go.
func TestRedactionGuardImplementsInterfaces(t *testing.T) {
	var _ core.PreProcessor = (*RedactionGuard)(nil)
	var _ core.PostProcessor = (*RedactionGuard)(nil)
	var _ core.StreamProcessor = (*RedactionGuard)(nil)
}

func TestRedactionInputRedactsPII(t *testing.T) {
	g := NewRedactionGuard(RedactPresets("pii"))
	req := core.ChatRequest{Messages: []core.ChatMessage{
		core.UserMessage("email me at jane.doe@example.com please"),
	}}
	if err := g.PreLLM(context.Background(), &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := req.Messages[0].Content
	if strings.Contains(got, "jane.doe@example.com") {
		t.Errorf("email not redacted: %q", got)
	}
	if !strings.Contains(got, "[REDACTED:email]") {
		t.Errorf("missing placeholder: %q", got)
	}
}

func TestRedactionOutputRedactsSecret(t *testing.T) {
	g := NewRedactionGuard(RedactPresets("secrets"), RedactPhases(PhaseOutput))
	resp := core.ChatResponse{Content: "here is your key AKIAIOSFODNN7EXAMPLE done"}
	if err := g.PostLLM(context.Background(), &resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(resp.Content, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("aws key not redacted: %q", resp.Content)
	}
}

func TestRedactionBlockStrategyHalts(t *testing.T) {
	g := NewRedactionGuard(RedactPresets("pii"), RedactStrategy(StrategyBlock))
	req := core.ChatRequest{Messages: []core.ChatMessage{
		core.UserMessage("my ssn is 123-45-6789"),
	}}
	err := g.PreLLM(context.Background(), &req)
	if _, ok := err.(*core.ErrHalt); !ok {
		t.Errorf("expected *core.ErrHalt, got %v", err)
	}
}

func TestRedactionCleanTextUntouched(t *testing.T) {
	g := NewRedactionGuard(RedactPresets("pii", "secrets"))
	req := core.ChatRequest{Messages: []core.ChatMessage{core.UserMessage("just a normal sentence")}}
	if err := g.PreLLM(context.Background(), &req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Messages[0].Content != "just a normal sentence" {
		t.Errorf("clean text changed: %q", req.Messages[0].Content)
	}
}

func TestRedactionBlockHaltsOnThinking(t *testing.T) {
	g := NewRedactionGuard(RedactPresets("secrets"), RedactStrategy(StrategyBlock), RedactPhases(PhaseOutput))
	resp := core.ChatResponse{
		Content:  "all clear",
		Thinking: "internal note: AKIAIOSFODNN7EXAMPLE",
	}
	err := g.PostLLM(context.Background(), &resp)
	if _, ok := err.(*core.ErrHalt); !ok {
		t.Errorf("expected *core.ErrHalt when secret appears in Thinking, got %v", err)
	}
}

func TestRedactionPostChunkRedactsInChunk(t *testing.T) {
	g := NewRedactionGuard(RedactPresets("pii"))
	ev := &core.StreamEvent{Type: core.EventTextDelta, Content: "ping jane.doe@example.com now"}
	out, err := g.PostChunk(context.Background(), ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil || strings.Contains(out.Content, "jane.doe@example.com") {
		t.Errorf("email not redacted in chunk: %+v", out)
	}
}

func TestRedactionPostChunkBlockHalts(t *testing.T) {
	g := NewRedactionGuard(RedactPresets("pii"), RedactStrategy(StrategyBlock))
	ev := &core.StreamEvent{Type: core.EventTextDelta, Content: "ssn 123-45-6789"}
	_, err := g.PostChunk(context.Background(), ev)
	if _, ok := err.(*core.ErrHalt); !ok {
		t.Errorf("expected *core.ErrHalt, got %v", err)
	}
}
