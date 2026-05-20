package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/nevindra/oasis/core"
)

// mergeAttachments combines accumulated sub-agent attachments with the final
// response attachments. Accumulated attachments come first (from tool calls
// during the loop), followed by any attachments from the final LLM response.
func mergeAttachments(accumulated, resp []Attachment) []Attachment {
	if len(accumulated) == 0 {
		return resp
	}
	if len(resp) == 0 {
		return accumulated
	}
	merged := make([]Attachment, 0, len(accumulated)+len(resp))
	merged = append(merged, accumulated...)
	merged = append(merged, resp...)
	return merged
}

// runeCount returns the total rune count of all message content.
func runeCount(messages []ChatMessage) int {
	var n int
	for _, m := range messages {
		n += utf8.RuneCountInString(m.Content)
	}
	return n
}

// compressMessages summarizes old tool-result messages via an LLM call.
// Keeps the last preserveIters iterations of tool results intact.
// currentRuneCount is the caller's tracked rune count (avoids redundant recomputation).
// Returns the compressed message slice and new rune count, or the
// original slice on error (degrade, don't die).
func compressMessages(ctx context.Context, cfg LoopConfig, task AgentTask, messages []ChatMessage, preserveIters, currentRuneCount int) ([]ChatMessage, int) {
	// Pick compression provider (used as fallback for inlineCompactor and for
	// resolving a per-request summarizer override via compressModel).
	provider := cfg.provider
	if cfg.compressModel != nil {
		if p := cfg.compressModel(ctx, task); p != nil {
			provider = p
		}
	}

	// Identify tool-result messages to compress.
	// Walk backwards to find the boundary of the last preserveIters iterations.
	// An "iteration" is one assistant message (with tool calls) followed by
	// its tool-result messages.
	iterCount := 0
	preserveFrom := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			iterCount++
			if iterCount >= preserveIters {
				preserveFrom = i
				break
			}
		}
	}

	// Collect old tool-result messages and prior summaries (before preserveFrom).
	// Prior summaries are re-compressed so successive passes fold together.
	const summaryPrefix = "[Summary of earlier tool results]\n"
	var oldMsgs []ChatMessage
	var toRemove []int
	for i := 0; i < preserveFrom; i++ {
		m := messages[i]
		switch {
		case m.ToolCallID != "" && m.Content != "":
			// Tool result message.
			oldMsgs = append(oldMsgs, m)
			toRemove = append(toRemove, i)
		case m.Role == "user" && strings.HasPrefix(m.Content, summaryPrefix) && i > 0:
			// Prior summary from an earlier compression pass (skip the initial user message at i=0).
			oldMsgs = append(oldMsgs, m)
			toRemove = append(toRemove, i)
		}
	}
	if len(toRemove) == 0 {
		return messages, currentRuneCount
	}

	// Start compression span if tracing.
	compressCtx := ctx
	if cfg.tracer != nil {
		var span Span
		compressCtx, span = cfg.tracer.Start(ctx, "agent.loop.compress",
			IntAttr("original_runes", currentRuneCount),
			IntAttr("messages_compressed", len(toRemove)))
		defer span.End()
	}

	// Pick the compactor: use the configured one, or fall back to a default
	// inlineCompactor backed by the (possibly override-resolved) provider.
	compactor := cfg.compressor
	if compactor == nil {
		compactor = NewInlineCompactor(provider)
	}

	// Delegate to the compactor with ScopeToolResultsOnly so the Compactor
	// implementation decides the prompt and summarization strategy.
	result, err := compactor.Compact(compressCtx, CompactRequest{
		Messages:           oldMsgs,
		Scope:              core.ScopeToolResultsOnly,
		SummarizerProvider: provider,
	})
	if err != nil {
		cfg.logger.Warn("context compression failed, continuing uncompressed", "error", err)
		return messages, currentRuneCount
	}

	// Build new message slice: keep non-removed messages, insert summary.
	// Pre-alloc per §5.6: len(messages)-len(toRemove)+1 (the +1 is for the inserted summary).
	removeSet := make([]bool, len(messages))
	for _, idx := range toRemove {
		removeSet[idx] = true
	}
	compressed := make([]ChatMessage, 0, len(messages)-len(toRemove)+1)
	summaryInserted := false
	for i, m := range messages {
		if removeSet[i] {
			if !summaryInserted {
				compressed = append(compressed, UserMessage(summaryPrefix+result.SummaryText))
				summaryInserted = true
			}
			continue
		}
		compressed = append(compressed, m)
	}

	newRuneCount := runeCount(compressed)
	cfg.logger.Info("context compressed",
		"agent", cfg.name,
		"before_runes", currentRuneCount,
		"after_runes", newRuneCount,
		"messages_removed", len(toRemove))

	return compressed, newRuneCount
}
