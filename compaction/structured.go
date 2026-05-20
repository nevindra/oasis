package compaction

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/nevindra/oasis/core"
)

const defaultCompactOutputBudget = 20_000

// StructuredCompactor is the default Compactor implementation.
// It calls an LLM provider once with a structured 9-section prompt
// (plus extras) and parses the <summary> block out of the response.
type StructuredCompactor struct {
	defaultProvider core.Provider
	logger          *slog.Logger
}

// Compile-time interface check.
var _ core.Compactor = (*StructuredCompactor)(nil)

// NewStructuredCompactor creates a StructuredCompactor with a default provider.
// The provider can be overridden per-call via CompactRequest.SummarizerProvider.
// Pass nil to require that every CompactRequest specifies its own provider.
func NewStructuredCompactor(defaultProvider core.Provider) *StructuredCompactor {
	return &StructuredCompactor{
		defaultProvider: defaultProvider,
		logger:          slog.Default().With("component", "core.structured-compactor"),
	}
}

// Compact runs the compaction call.
func (s *StructuredCompactor) Compact(ctx context.Context, req core.CompactRequest) (core.CompactResult, error) {
	if len(req.Messages) == 0 {
		return core.CompactResult{}, ErrEmptyMessages
	}

	provider := req.SummarizerProvider
	if provider == nil {
		provider = s.defaultProvider
	}
	if provider == nil {
		return core.CompactResult{}, ErrNoProvider
	}

	// Defense-in-depth: strip media blocks even if caller didn't.
	stripped := StripMediaBlocks(req.Messages)

	// Build prompt and prepend as a system message to the stripped history.
	prompt := BuildCompactPrompt(req.ExtraSections, req.FocusHint, req.IsRecompact)

	chatMsgs := make([]core.ChatMessage, 0, len(stripped)+1)
	chatMsgs = append(chatMsgs, core.ChatMessage{Role: "system", Content: prompt})
	chatMsgs = append(chatMsgs, stripped...)

	budget := req.OutputBudget
	if budget <= 0 {
		budget = defaultCompactOutputBudget
	}

	chatReq := core.ChatRequest{
		Messages:         chatMsgs,
		GenerationParams: &core.GenerationParams{MaxTokens: &budget},
	}

	resp, err := core.Chat(ctx, provider, chatReq)
	if err != nil {
		return core.CompactResult{}, fmt.Errorf("compact: provider chat: %w", err)
	}

	summary, err := extractSummaryBlock(resp.Content)
	if err != nil {
		return core.CompactResult{}, fmt.Errorf("compact: parse summary: %w (raw response: %q)",
			err, truncateForError(resp.Content, 500))
	}

	sections := parseNumberedSections(summary)

	// Token accounting.
	sourceTokens := EstimateContextTokens(stripped, core.ModelInfo{})
	summaryTokens := resp.Usage.OutputTokens
	if summaryTokens == 0 {
		summaryTokens = EstimateContextTokens(
			[]core.ChatMessage{{Content: summary}}, core.ModelInfo{})
	}
	ratio := 0.0
	if sourceTokens > 0 {
		ratio = float64(summaryTokens) / float64(sourceTokens)
	}

	// SourceMessageIDs intentionally left nil: ChatMessage has no ID field
	// in the current Oasis schema. Future work may add per-message IDs.

	// Populate warnings.
	var warnings []string
	if budget > 0 && resp.Usage.OutputTokens == budget {
		warnings = append(warnings, "summary_truncated_at_budget")
	}
	if len(sections) < 9 {
		warnings = append(warnings, "partial_sections")
	}

	return core.CompactResult{
		SummaryText:      summary,
		Sections:         sections,
		SourceTokens:     sourceTokens,
		SummaryTokens:    summaryTokens,
		CompressionRatio: ratio,
		PersistsTable: []string{
			"user intent",
			"key decisions",
			"artifact IDs",
			"active skills",
			"user preferences",
			"pending tasks",
		},
		LostTable: []string{
			"intermediate reasoning journey",
			"tool result noise",
			"exploration detours",
			"verbose log output",
		},
		Warnings: warnings,
	}, nil
}

// extractSummaryBlock returns the content between <summary>...</summary> tags.
var summaryBlockRe = regexp.MustCompile(`(?si)<summary>\s*(.+?)\s*</summary>`)

func extractSummaryBlock(raw string) (string, error) {
	m := summaryBlockRe.FindStringSubmatch(raw)
	if len(m) != 2 {
		return "", ErrSummaryParseFailed
	}
	summary := strings.TrimSpace(m[1])
	if summary == "" {
		return "", ErrSummaryParseFailed
	}
	return summary, nil
}

// parseNumberedSections splits a summary text into a section map.
// Matches headers like "1. Title:" or "10. Title:" at line start.
var sectionHeaderRe = regexp.MustCompile(`(?m)^\s*(\d+)\.\s+([^:]+):\s*$`)

func parseNumberedSections(summary string) map[string]string {
	out := make(map[string]string)
	idxs := sectionHeaderRe.FindAllStringSubmatchIndex(summary, -1)
	for i, idx := range idxs {
		titleStart, titleEnd := idx[4], idx[5]
		title := strings.TrimSpace(summary[titleStart:titleEnd])
		contentStart := idx[1]
		contentEnd := len(summary)
		if i+1 < len(idxs) {
			contentEnd = idxs[i+1][0]
		}
		body := strings.TrimSpace(summary[contentStart:contentEnd])
		out[title] = body
	}
	return out
}

func truncateForError(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "... [truncated]"
}
