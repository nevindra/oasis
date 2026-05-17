// Package compaction provides the default implementation of the
// oasis.Compactor contract — turning long conversation histories into
// structured 9-section summaries via an LLM call.
//
// The kernel oasis package defines the Compactor interface and the
// CompactRequest/CompactResult/CompactSection value types. This package
// provides:
//
//   - StructuredCompactor: the default Compactor implementation. Wraps
//     an oasis.Provider and issues one summarization call per Compact()
//     invocation.
//   - BuildCompactPrompt: composes the structured prompt independently
//     of any Compactor; useful for callers that want full control.
//   - EstimateContextTokens: model-family-aware token estimator. Used
//     internally to compute compression ratio; exported for upstream
//     trigger logic (e.g., "compact when input > 80% of window").
//   - StripMediaBlocks: defensive helper that strips image/document
//     attachments before a compaction call so the summarizer never
//     sees megabytes of media bytes.
//   - CompactableToolNames: the default whitelist of tool names whose
//     results may be summarized away during compaction.
//
// Sentinel errors (ErrEmptyMessages, ErrNoProvider, ErrSummaryParseFailed)
// are returned by StructuredCompactor.Compact and can be matched with
// errors.Is.
//
// Basic usage:
//
//     c := compaction.NewStructuredCompactor(provider)
//     out, err := c.Compact(ctx, oasis.CompactRequest{
//         Messages:  history,
//         FocusHint: "focus on user decisions",
//     })
//
// Pair with oasis.WithCompaction to wire automatic compaction into the
// agent's conversation memory:
//
//     agent := oasis.NewLLMAgent("agent", "...", provider,
//         oasis.WithConversationMemory(store,
//             oasis.MaxTokens(100_000),
//             oasis.WithCompaction(
//                 compaction.NewStructuredCompactor(summarizer),
//                 0.80,
//             ),
//         ),
//     )
//
// All exported types and functions are safe for concurrent use except
// where noted on individual symbols.
package compaction
