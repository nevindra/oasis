package oasis

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// recordingCompactor wraps a canned CompactResult and counts calls.
// Used to verify agent-level wiring of WithCompaction.
type recordingCompactor struct {
	result CompactResult
	err    error
	calls  atomic.Int32
}

func (c *recordingCompactor) Compact(_ context.Context, _ CompactRequest) (CompactResult, error) {
	c.calls.Add(1)
	if c.err != nil {
		return CompactResult{}, c.err
	}
	return c.result, nil
}

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

// TestWithCompaction_FiresWhenThresholdCrossed verifies the agent-level
// wiring: when loaded history exceeds compactThreshold × MaxTokens, the
// Compactor is invoked and the captured ChatRequest sees the summary
// instead of the raw history.
func TestWithCompaction_FiresWhenThresholdCrossed(t *testing.T) {
	// Seed ~1200 tokens of history (~4800 chars). With MaxTokens=1000 and
	// threshold=0.5, trigger = 500 tokens — comfortably crossed.
	big := strings.Repeat("filler text blob blob blob ", 40) // ~1080 chars each
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: big},
			{Role: "assistant", Content: big},
			{Role: "user", Content: big},
			{Role: "assistant", Content: big},
		},
		threads: map[string]Thread{"t1": {ID: "t1"}},
	}
	compactor := &recordingCompactor{result: CompactResult{
		SummaryText:   "USER wanted X. ASSISTANT delivered Y.",
		SourceTokens:  1200,
		SummaryTokens: 20,
	}}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	agent := NewLLMAgent("test", "test", provider,
		WithConversationMemory(store,
			MaxHistory(50),
			MaxTokens(1000),
			WithCompaction(compactor, 0.5),
		),
	)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"}.WithThreadID("t1"))
	if err != nil {
		t.Fatal(err)
	}

	if got := compactor.calls.Load(); got != 1 {
		t.Fatalf("Compact called %d times, want 1", got)
	}

	msgs := provider.firstCall().Messages
	foundSummary := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "Prior conversation summary") &&
			strings.Contains(m.Content, "USER wanted X") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Errorf("expected summary system message in LLM request, got %d messages; none contained the summary marker", len(msgs))
	}

	// Raw history should NOT be in the request — it was replaced.
	for _, m := range msgs {
		if strings.Contains(m.Content, "filler text blob") {
			t.Error("raw history leaked into LLM request after compaction")
			break
		}
	}
}

// TestWithCompaction_NoopBelowThreshold verifies the Compactor is not
// invoked when history is under the trigger.
func TestWithCompaction_NoopBelowThreshold(t *testing.T) {
	store := &recordingStore{
		history: []Message{{Role: "user", Content: "short"}},
		threads: map[string]Thread{"t1": {ID: "t1"}},
	}
	compactor := &recordingCompactor{result: CompactResult{SummaryText: "unused"}}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	agent := NewLLMAgent("test", "test", provider,
		WithConversationMemory(store,
			MaxHistory(10),
			MaxTokens(10_000),
			WithCompaction(compactor, 0.8),
		),
	)
	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"}.WithThreadID("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := compactor.calls.Load(); got != 0 {
		t.Fatalf("Compact called %d times below threshold, want 0", got)
	}
}

// TestWithCompaction_MergesWithCrossThreadRecall verifies that when
// compaction and cross-thread recall both fire on the same turn, the
// resulting LLM request does NOT contain two adjacent system messages
// (the compaction summary and the recall block). Some providers reject
// consecutive system messages outright; all are cleaner with a single
// merged block.
func TestWithCompaction_MergesWithCrossThreadRecall(t *testing.T) {
	big := strings.Repeat("filler text blob blob blob ", 40)
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: big},
			{Role: "assistant", Content: big},
			{Role: "user", Content: big},
			{Role: "assistant", Content: big},
		},
		related: []ScoredMessage{
			{Message: Message{ThreadID: "other-thread", Role: "user", Content: "earlier note"}, Score: 0.9},
		},
		threads: map[string]Thread{"t1": {ID: "t1"}},
	}
	compactor := &recordingCompactor{result: CompactResult{
		SummaryText:   "USER wanted X. ASSISTANT delivered Y.",
		SourceTokens:  1200,
		SummaryTokens: 20,
	}}

	emb := &stubEmbedding{}
	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	agent := NewLLMAgent("test", "test", provider,
		WithPrompt("base system"),
		WithConversationMemory(store,
			MaxHistory(50),
			MaxTokens(1000),
			WithCompaction(compactor, 0.5),
			CrossThreadSearch(emb),
		),
	)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"}.WithThreadID("t1"))
	if err != nil {
		t.Fatal(err)
	}

	msgs := provider.firstCall().Messages
	for i := 1; i < len(msgs); i++ {
		if msgs[i-1].Role == "system" && msgs[i].Role == "system" {
			t.Fatalf("adjacent system messages at index %d..%d; full role sequence: %s",
				i-1, i, roleSequence(msgs))
		}
	}

	// Sanity: the summary and recall content must still reach the LLM,
	// just inside a single merged block.
	var combined strings.Builder
	for _, m := range msgs {
		if m.Role == "system" {
			combined.WriteString(m.Content)
			combined.WriteString("\n---\n")
		}
	}
	merged := combined.String()
	if !strings.Contains(merged, "Prior conversation summary") {
		t.Error("compaction summary missing from merged system block")
	}
	if !strings.Contains(merged, "recalled from past conversations") {
		t.Error("cross-thread recall missing from merged system block")
	}
}

// TestMemoryLoadHistoryError_SkipsCompactionAndRecall verifies that when
// the conversation store fails to return history, the agent does NOT
// fabricate downstream context: compaction is skipped (no summary), and
// cross-thread recall is skipped too (recall on top of missing history
// is misleading — the LLM has no idea what it lacks). The request still
// goes through with system prompt + user input only.
func TestMemoryLoadHistoryError_SkipsCompactionAndRecall(t *testing.T) {
	store := &errorHistoryStore{
		related: []ScoredMessage{
			{Message: Message{ThreadID: "other", Role: "user", Content: "earlier note"}, Score: 0.9},
		},
	}
	compactor := &recordingCompactor{result: CompactResult{SummaryText: "unused summary"}}
	emb := &stubEmbedding{}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	agent := NewLLMAgent("test", "test", provider,
		WithPrompt("base system"),
		WithConversationMemory(store,
			MaxTokens(1000),
			WithCompaction(compactor, 0.5),
			CrossThreadSearch(emb),
		),
	)

	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"}.WithThreadID("t1"))
	if err != nil {
		t.Fatal(err)
	}

	if got := compactor.calls.Load(); got != 0 {
		t.Errorf("Compact called %d times after history load failed; want 0", got)
	}

	msgs := provider.firstCall().Messages
	for _, m := range msgs {
		if strings.Contains(m.Content, "Prior conversation summary") {
			t.Error("compaction summary present after history load error")
		}
		if strings.Contains(m.Content, "recalled from past conversations") {
			t.Error("cross-thread recall present after history load error")
		}
	}
}

func roleSequence(msgs []ChatMessage) string {
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	return strings.Join(roles, ",")
}

// TestWithCompaction_FallsBackOnError verifies that a Compactor error
// is logged and the agent continues with the uncompacted history.
func TestWithCompaction_FallsBackOnError(t *testing.T) {
	big := strings.Repeat("filler text blob blob blob ", 40)
	store := &recordingStore{
		history: []Message{
			{Role: "user", Content: big},
			{Role: "assistant", Content: big},
			{Role: "user", Content: big},
			{Role: "assistant", Content: big},
		},
		threads: map[string]Thread{"t1": {ID: "t1"}},
	}
	compactor := &recordingCompactor{err: errors.New("summarizer unavailable")}

	provider := &capturingProvider{resp: ChatResponse{Content: "ok"}}
	agent := NewLLMAgent("test", "test", provider,
		WithConversationMemory(store,
			MaxHistory(50),
			MaxTokens(1000),
			WithCompaction(compactor, 0.5),
		),
	)
	_, err := agent.Execute(context.Background(), AgentTask{Input: "hi"}.WithThreadID("t1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := compactor.calls.Load(); got != 1 {
		t.Fatalf("Compact called %d times, want 1", got)
	}
	// Fallback path runs trim instead of insertion, so no summary marker.
	msgs := provider.firstCall().Messages
	for _, m := range msgs {
		if strings.Contains(m.Content, "Prior conversation summary") {
			t.Error("summary system message should not be present when compactor errored")
		}
	}
}
