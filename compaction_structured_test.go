package oasis

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// compactMockProvider is a canned-response provider for compaction tests.
// Named distinctly from agent_test.go's mockProvider to avoid collision.
type compactMockProvider struct {
	response string
	err      error
	lastReq  ChatRequest
}

func (m *compactMockProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	m.lastReq = req
	if m.err != nil {
		return ChatResponse{}, m.err
	}
	return ChatResponse{
		Content: m.response,
		Usage:   Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func (m *compactMockProvider) ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	close(ch)
	return m.Chat(ctx, req)
}

// REQUIRED — Provider interface has 3 methods, not 2.
func (m *compactMockProvider) Name() string { return "mock" }

func canonicalSummary() string {
	return `<analysis>
chronological walkthrough
</analysis>

<summary>
1. Primary Request and Intent:
   User wants to build a presentation.

2. Key Technical Concepts:
   - PowerPoint generation
   - Slide layout

3. Files and Artifacts:
   - deck.pptx
     - Main output file

4. Errors and Fixes:
   - None

5. Problem Solving:
   Straightforward path.

6. All User Messages:
   - "make me a deck"
   - "use blue"

7. Pending Tasks:
   - Add final slide

8. Current Work:
   Adding chart to slide 3.

9. Optional Next Step:
   Finalize slide 3 chart per user's "use blue" directive.
</summary>`
}

func TestStructuredCompactor_Happy(t *testing.T) {
	mock := &compactMockProvider{response: canonicalSummary()}
	c := NewStructuredCompactor(mock)
	res, err := c.Compact(context.Background(), CompactRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "make me a deck"},
			{Role: "assistant", Content: "sure"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SummaryText == "" {
		t.Error("empty SummaryText")
	}
	if strings.Contains(res.SummaryText, "<analysis>") {
		t.Error("SummaryText must have <analysis> block stripped")
	}
	if !strings.Contains(res.SummaryText, "Primary Request and Intent") {
		t.Error("SummaryText missing core section")
	}
	if len(res.Sections) < 9 {
		t.Errorf("expected 9+ parsed sections, got %d", len(res.Sections))
	}
	if _, ok := res.Sections["Primary Request and Intent"]; !ok {
		t.Error("missing Primary Request section in map")
	}
	if res.SummaryTokens == 0 {
		t.Error("SummaryTokens should be populated")
	}
	if res.SourceTokens == 0 {
		t.Error("SourceTokens should be populated")
	}
	if res.CompressionRatio <= 0 {
		t.Error("CompressionRatio must be positive")
	}
}

func TestStructuredCompactor_EmptyMessages_ReturnsErr(t *testing.T) {
	c := NewStructuredCompactor(&compactMockProvider{})
	_, err := c.Compact(context.Background(), CompactRequest{})
	if !errors.Is(err, ErrEmptyMessages) {
		t.Errorf("err = %v, want ErrEmptyMessages", err)
	}
}

func TestStructuredCompactor_NilProvider_ReturnsErr(t *testing.T) {
	c := NewStructuredCompactor(nil)
	_, err := c.Compact(context.Background(), CompactRequest{
		Messages: []ChatMessage{{Role: "user", Content: "x"}},
	})
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
}

func TestStructuredCompactor_ProviderError_Wrapped(t *testing.T) {
	boom := errors.New("provider boom")
	c := NewStructuredCompactor(&compactMockProvider{err: boom})
	_, err := c.Compact(context.Background(), CompactRequest{
		Messages: []ChatMessage{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("err should wrap boom; got %v", err)
	}
}

func TestStructuredCompactor_MissingSummaryTag_ReturnsParseErr(t *testing.T) {
	mock := &compactMockProvider{response: "just some prose with no tags"}
	c := NewStructuredCompactor(mock)
	_, err := c.Compact(context.Background(), CompactRequest{
		Messages: []ChatMessage{{Role: "user", Content: "x"}},
	})
	if !errors.Is(err, ErrSummaryParseFailed) {
		t.Errorf("err = %v, want ErrSummaryParseFailed", err)
	}
}

func TestStructuredCompactor_FocusHint_InPrompt(t *testing.T) {
	mock := &compactMockProvider{response: canonicalSummary()}
	c := NewStructuredCompactor(mock)
	_, err := c.Compact(context.Background(), CompactRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "x"}},
		FocusHint: "focus pada layout",
	})
	if err != nil {
		t.Fatal(err)
	}
	var promptBody string
	for _, m := range mock.lastReq.Messages {
		promptBody += m.Content
	}
	if !strings.Contains(promptBody, "focus pada layout") {
		t.Error("focus hint not in prompt sent to provider")
	}
}

func TestStructuredCompactor_StripsMediaBeforeCall(t *testing.T) {
	mock := &compactMockProvider{response: canonicalSummary()}
	c := NewStructuredCompactor(mock)
	msgs := []ChatMessage{
		{Role: "user", Content: "see", Attachments: []Attachment{
			{MimeType: "image/png", Data: make([]byte, 10000)},
		}},
	}
	_, err := c.Compact(context.Background(), CompactRequest{Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mock.lastReq.Messages {
		for _, att := range m.Attachments {
			if strings.HasPrefix(att.MimeType, "image/") {
				t.Fatal("image attachment leaked into compaction call")
			}
		}
	}
}

func TestStructuredCompactor_RequestProviderOverridesDefault(t *testing.T) {
	def := &compactMockProvider{response: "DEFAULT"}
	override := &compactMockProvider{response: canonicalSummary()}
	c := NewStructuredCompactor(def)
	_, err := c.Compact(context.Background(), CompactRequest{
		Messages:           []ChatMessage{{Role: "user", Content: "x"}},
		SummarizerProvider: override,
	})
	if err != nil {
		t.Fatal(err)
	}
	if def.lastReq.Messages != nil {
		t.Error("default provider should not have been called")
	}
	if override.lastReq.Messages == nil {
		t.Error("override provider should have been called")
	}
}
