package compaction

import "errors"

// Sentinel errors returned by the StructuredCompactor and other compaction
// primitives in this module. Use errors.Is for matching.
var (
	ErrEmptyMessages      = errors.New("compaction: messages list is empty")
	ErrNoProvider         = errors.New("compaction: no summarizer provider")
	ErrSummaryParseFailed = errors.New("compaction: failed to parse <summary> block from response")
)
