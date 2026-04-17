package oasis

import (
	"strings"
	"unicode/utf8"
)

// imageTokenEstimate is the per-image-attachment token cost heuristic.
// Matches Claude Code's IMAGE_MAX_TOKEN_SIZE — images cost ~2000 tokens
// regardless of resolution/format.
const imageTokenEstimate = 2000

// EstimateContextTokens returns an approximate token count for a message
// list against the given model. Uses a model-family-aware heuristic. Does
// NOT make network calls. Accurate to ~10-15%. Prefer ChatResponse.Usage.
// InputTokens for exact counts after a real response.
//
// Image/document attachments count imageTokenEstimate (2000) each,
// regardless of data size.
//
// Unknown providers use a conservative fallback: runeCount * 4/3 / 4.
// Gemini is ~5% tighter than other families.
func EstimateContextTokens(messages []ChatMessage, model ModelInfo) int {
	if len(messages) == 0 {
		return 0
	}
	var runes int
	var mediaCount int
	for _, m := range messages {
		runes += utf8.RuneCountInString(m.Content)
		for _, att := range m.Attachments {
			if strings.HasPrefix(att.MimeType, "image/") ||
				strings.HasPrefix(att.MimeType, "application/pdf") ||
				att.MimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
				mediaCount++
			}
		}
	}

	// Why: the naive heuristic is ~4 chars per token. We pad by a 4/3 factor
	// so the estimate runs hot rather than cold — trimming prematurely is
	// recoverable (more recent context survives), but overflowing a real
	// context window is a hard provider error. Effective rate: ~1 token per
	// 3 runes.
	base := (runes * 4) / 3 / 4

	if strings.EqualFold(model.Provider, "gemini") {
		base = (base * 95) / 100
	}

	return base + mediaCount*imageTokenEstimate
}

// StripMediaBlocks returns a copy of messages with image and document
// attachments removed and replaced by text markers ("[image]", "[document]")
// appended to the message Content. Used before a compaction LLM call to:
//   (a) prevent the compaction request itself from overflowing on media bytes
//   (b) save tokens — visual content doesn't help summary generation
//
// Does NOT modify the original messages. Non-media attachments are preserved.
func StripMediaBlocks(messages []ChatMessage) []ChatMessage {
	out := make([]ChatMessage, len(messages))
	for i, m := range messages {
		if len(m.Attachments) == 0 {
			out[i] = m
			continue
		}
		var kept []Attachment
		var markers []string
		for _, att := range m.Attachments {
			switch {
			case strings.HasPrefix(att.MimeType, "image/"):
				markers = append(markers, "[image]")
			case strings.HasPrefix(att.MimeType, "application/pdf"),
				att.MimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				att.MimeType == "application/vnd.openxmlformats-officedocument.presentationml.presentation":
				markers = append(markers, "[document]")
			default:
				kept = append(kept, att)
			}
		}
		newContent := m.Content
		if len(markers) > 0 {
			newContent = strings.TrimSpace(newContent + "\n" + strings.Join(markers, " "))
		}
		nm := m
		nm.Content = newContent
		nm.Attachments = kept
		out[i] = nm
	}
	return out
}

// compactableToolNamesDefault is the package-level whitelist of tool
// names whose results are safe to compact/drop during summarization.
// Tools NOT in this list are preserved verbatim by default — including
// skill_activate, ask_user, and any other instructional-result tool.
//
// Callers extend this list for their own tool registry (e.g., Athena
// appends pptx_read, kb_search).
var compactableToolNamesDefault = []string{
	"shell_exec",
	"file_read",
	"file_write",
	"grep",
	"glob",
	"web_search",
	"web_fetch",
}

// CompactableToolNames returns a fresh copy of the default whitelist of
// tool names whose results are safe to compact. Modifying the returned
// slice does not affect future calls.
func CompactableToolNames() []string {
	out := make([]string, len(compactableToolNamesDefault))
	copy(out, compactableToolNamesDefault)
	return out
}
