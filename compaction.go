package oasis

import (
	"context"
	"errors"
)

// Sentinel errors for compaction primitives.
var (
	ErrEmptyMessages      = errors.New("compaction: messages list is empty")
	ErrNoProvider         = errors.New("compaction: no summarizer provider")
	ErrSummaryParseFailed = errors.New("compaction: failed to parse <summary> block from response")
)

// Compactor turns a message list into a structured summary via an LLM call.
// Implementations MUST be safe to call concurrently.
type Compactor interface {
	Compact(ctx context.Context, req CompactRequest) (CompactResult, error)
}

// CompactRequest is the input to a single compaction call.
type CompactRequest struct {
	// Messages to summarize. Caller decides partitioning.
	Messages []ChatMessage

	// SummarizerProvider is the LLM used for the summarization call.
	// If nil, the Compactor's configured default is used.
	SummarizerProvider Provider

	// FocusHint is an optional user directive (e.g., "focus on layout").
	// Injected into the prompt to bias preservation.
	FocusHint string

	// IsRecompact signals the summarizer that the input already contains
	// a prior compact. Changes prompt tone to preserve prior summary by
	// reference instead of re-summarizing it.
	IsRecompact bool

	// OutputBudget caps the summary's max_tokens. Zero = implementation
	// default (20_000).
	OutputBudget int

	// ExtraSections are appended to the default 9 sections.
	// Use for domain-specific additions (e.g., "Active Skills").
	ExtraSections []CompactSection
}

// CompactSection describes a domain-specific summary section.
type CompactSection struct {
	Title        string
	Instructions string
}

// CompactResult is the output of a compaction call.
type CompactResult struct {
	// SummaryText is the full structured summary (analysis scratchpad
	// already stripped).
	SummaryText string

	// Sections is the parsed per-section map. Keys are section titles.
	Sections map[string]string

	// Token accounting.
	SourceTokens     int
	SummaryTokens    int
	CompressionRatio float64

	// SourceMessageIDs are IDs of messages that were summarized. Populated
	// from input Messages[].ID when set.
	SourceMessageIDs []string

	// PersistsTable and LostTable are UI transparency data.
	PersistsTable []string
	LostTable     []string

	// Warnings is non-empty when the result is usable but imperfect
	// (partial parse, truncated, etc). UI should surface these.
	Warnings []string
}
