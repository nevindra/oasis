package oasis

import (
	"errors"
	"strings"
	"testing"
)

func TestCompactionErrors_Distinct(t *testing.T) {
	if errors.Is(ErrEmptyMessages, ErrNoProvider) {
		t.Fatal("ErrEmptyMessages must not equal ErrNoProvider")
	}
	if errors.Is(ErrSummaryParseFailed, ErrEmptyMessages) {
		t.Fatal("ErrSummaryParseFailed must not equal ErrEmptyMessages")
	}
}

func TestCompactResult_Zero(t *testing.T) {
	var r CompactResult
	if r.SummaryText != "" {
		t.Errorf("zero SummaryText = %q, want empty", r.SummaryText)
	}
	if r.SourceTokens != 0 {
		t.Errorf("zero SourceTokens = %d, want 0", r.SourceTokens)
	}
	if len(r.Sections) != 0 {
		t.Errorf("zero Sections len = %d, want 0", len(r.Sections))
	}
}

func TestCompactRequest_ExtraSectionsAppendable(t *testing.T) {
	req := CompactRequest{}
	req.ExtraSections = append(req.ExtraSections, CompactSection{
		Title:        "Active Skills",
		Instructions: "List all skills activated",
	})
	if len(req.ExtraSections) != 1 {
		t.Errorf("ExtraSections len = %d, want 1", len(req.ExtraSections))
	}
	if req.ExtraSections[0].Title != "Active Skills" {
		t.Errorf("Title = %q, want %q", req.ExtraSections[0].Title, "Active Skills")
	}
}

func TestEstimateContextTokens_EmptyMessages(t *testing.T) {
	got := EstimateContextTokens(nil, ModelInfo{})
	if got != 0 {
		t.Errorf("empty messages: got %d, want 0", got)
	}
}

func TestEstimateContextTokens_TextMessages(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "Hello, world!"},
		{Role: "assistant", Content: "Hi there."},
	}
	got := EstimateContextTokens(msgs, ModelInfo{Provider: "openaicompat"})
	if got < 5 || got > 15 {
		t.Errorf("got %d, want in [5,15]", got)
	}
}

func TestEstimateContextTokens_KnownProvider_Gemini(t *testing.T) {
	msgs := []ChatMessage{{Role: "user", Content: "The quick brown fox jumps over the lazy dog."}}
	got := EstimateContextTokens(msgs, ModelInfo{Provider: "gemini"})
	if got <= 0 || got > 50 {
		t.Errorf("got %d, want in (0,50]", got)
	}
}

func TestEstimateContextTokens_ImageBlock_CountsFixed(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "", Attachments: []Attachment{
			{MimeType: "image/png", Data: make([]byte, 1024)},
		}},
	}
	got := EstimateContextTokens(msgs, ModelInfo{Provider: "gemini"})
	if got < 1500 || got > 2500 {
		t.Errorf("image-only msg: got %d, want in [1500,2500]", got)
	}
}

func TestStripMediaBlocks_NoMedia_Unchanged(t *testing.T) {
	msgs := []ChatMessage{{Role: "user", Content: "hello"}}
	stripped := StripMediaBlocks(msgs)
	if len(stripped) != 1 {
		t.Fatalf("len = %d, want 1", len(stripped))
	}
	if stripped[0].Content != "hello" {
		t.Errorf("content = %q, want 'hello'", stripped[0].Content)
	}
}

func TestStripMediaBlocks_ImageReplacedWithMarker(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "see this", Attachments: []Attachment{
			{MimeType: "image/png", Data: []byte{0xff, 0xd8}},
		}},
	}
	stripped := StripMediaBlocks(msgs)
	if len(stripped[0].Attachments) != 0 {
		t.Errorf("attachments len = %d, want 0", len(stripped[0].Attachments))
	}
	if !strings.Contains(stripped[0].Content, "[image]") {
		t.Errorf("content = %q, want to contain '[image]'", stripped[0].Content)
	}
}

func TestStripMediaBlocks_DoesNotMutateOriginal(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "x", Attachments: []Attachment{
			{MimeType: "image/jpeg", Data: []byte{1, 2, 3}},
		}},
	}
	_ = StripMediaBlocks(msgs)
	if len(msgs[0].Attachments) != 1 {
		t.Errorf("original attachments mutated: len = %d, want 1", len(msgs[0].Attachments))
	}
}

func TestStripMediaBlocks_MultipleMediaTypes(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "user", Content: "mixed", Attachments: []Attachment{
			{MimeType: "image/png"},
			{MimeType: "application/pdf"},
			{MimeType: "text/plain"}, // not media, kept
		}},
	}
	stripped := StripMediaBlocks(msgs)
	if len(stripped[0].Attachments) != 1 {
		t.Errorf("remaining attachments = %d, want 1 (text/plain kept)", len(stripped[0].Attachments))
	}
	if !strings.Contains(stripped[0].Content, "[image]") {
		t.Errorf("missing [image] marker")
	}
	if !strings.Contains(stripped[0].Content, "[document]") {
		t.Errorf("missing [document] marker")
	}
}

func TestCompactableToolNames_NonEmpty(t *testing.T) {
	names := CompactableToolNames()
	if len(names) == 0 {
		t.Fatal("CompactableToolNames returned empty slice")
	}
}

func TestCompactableToolNames_IncludesShellAndFileRead(t *testing.T) {
	names := CompactableToolNames()
	has := func(s string) bool {
		for _, n := range names {
			if n == s {
				return true
			}
		}
		return false
	}
	if !has("shell_exec") {
		t.Error("missing shell_exec")
	}
	if !has("file_read") {
		t.Error("missing file_read")
	}
	if !has("web_search") {
		t.Error("missing web_search")
	}
}

func TestCompactableToolNames_ExcludesSkillActivate(t *testing.T) {
	names := CompactableToolNames()
	for _, n := range names {
		if n == "skill_activate" {
			t.Fatal("skill_activate must NOT be in the compactable whitelist — its instruction content must be preserved")
		}
		if n == "ask_user" {
			t.Fatal("ask_user must NOT be in the compactable whitelist")
		}
	}
}

func TestCompactableToolNames_ReturnsCopy(t *testing.T) {
	a := CompactableToolNames()
	a[0] = "MUTATED"
	b := CompactableToolNames()
	if b[0] == "MUTATED" {
		t.Fatal("CompactableToolNames must return a fresh slice, not a shared reference")
	}
}

func TestCompactPrompt_ContainsNoToolsPreamble(t *testing.T) {
	got := BuildCompactPrompt(nil, "", false)
	if !strings.Contains(got, "TEXT ONLY") {
		t.Error("missing NO_TOOLS_PREAMBLE marker")
	}
	if !strings.Contains(got, "Do NOT call any tools") {
		t.Error("prompt should forbid tool calls")
	}
}

func TestCompactPrompt_Contains9CoreSections(t *testing.T) {
	got := BuildCompactPrompt(nil, "", false)
	needed := []string{
		"Primary Request and Intent",
		"Key Technical Concepts",
		"Files and Artifacts",
		"Errors and Fixes",
		"Problem Solving",
		"All User Messages",
		"Pending Tasks",
		"Current Work",
		"Optional Next Step",
	}
	for _, n := range needed {
		if !strings.Contains(got, n) {
			t.Errorf("missing section %q in prompt", n)
		}
	}
}

func TestCompactPrompt_FocusHintInjected(t *testing.T) {
	got := BuildCompactPrompt(nil, "fokus pada layout slide", false)
	if !strings.Contains(got, "fokus pada layout slide") {
		t.Errorf("focus hint not injected into prompt; got:\n%s", got)
	}
}

func TestCompactPrompt_FocusHintEmpty_NoInjection(t *testing.T) {
	got := BuildCompactPrompt(nil, "", false)
	if strings.Contains(got, "Additional focus from user:") {
		t.Error("empty focus hint must not add 'Additional focus' marker")
	}
}

func TestCompactPrompt_RecompactNote(t *testing.T) {
	withNote := BuildCompactPrompt(nil, "", true)
	withoutNote := BuildCompactPrompt(nil, "", false)
	if !strings.Contains(withNote, "prior summary") {
		t.Error("IsRecompact=true must add prior-summary note")
	}
	if strings.Contains(withoutNote, "prior summary") {
		t.Error("IsRecompact=false must NOT add prior-summary note")
	}
}

func TestCompactPrompt_ExtraSectionsAppended(t *testing.T) {
	extras := []CompactSection{
		{Title: "Active Skills", Instructions: "List skills loaded"},
	}
	got := BuildCompactPrompt(extras, "", false)
	if !strings.Contains(got, "Active Skills") {
		t.Error("extra section title not in prompt")
	}
	if !strings.Contains(got, "List skills loaded") {
		t.Error("extra section instructions not in prompt")
	}
	if !strings.Contains(got, "Primary Request and Intent") {
		t.Error("extras should not replace core sections")
	}
}

func TestWithCompaction_StoresOnConfig(t *testing.T) {
	cfg := buildConfig([]AgentOption{
		WithConversationMemory(nil, WithCompaction(
			NewStructuredCompactor(nil), 0.75)),
	})
	if cfg.compactor == nil {
		t.Fatal("compactor not stored on config")
	}
	if cfg.compactThreshold != 0.75 {
		t.Errorf("threshold = %f, want 0.75", cfg.compactThreshold)
	}
}

func TestWithCompaction_OmittedLeavesNilCompactor(t *testing.T) {
	cfg := buildConfig([]AgentOption{
		WithConversationMemory(nil, MaxHistory(10)),
	})
	if cfg.compactor != nil {
		t.Error("compactor should be nil when WithCompaction not used")
	}
}
