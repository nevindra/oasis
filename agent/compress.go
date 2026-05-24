package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/nevindra/oasis/core"
)

// mergeAttachments combines accumulated sub-agent attachments with the final
// response attachments.
func mergeAttachments(accumulated, resp []core.Attachment) []core.Attachment {
	if len(accumulated) == 0 {
		return resp
	}
	if len(resp) == 0 {
		return accumulated
	}
	merged := make([]core.Attachment, 0, len(accumulated)+len(resp))
	merged = append(merged, accumulated...)
	merged = append(merged, resp...)
	return merged
}

// runeCount returns the total rune count of all message content.
func runeCount(messages []core.ChatMessage) int {
	var n int
	for _, m := range messages {
		n += utf8.RuneCountInString(m.Content)
	}
	return n
}

// compressMessages summarizes old tool-result messages via an LLM call.
func compressMessages(ctx context.Context, cfg LoopConfig, task AgentTask, messages []core.ChatMessage, preserveIters, currentRuneCount int) ([]core.ChatMessage, int) {
	// Pick compression provider.
	provider := cfg.Provider
	if cfg.CompressModel != nil {
		if p := cfg.CompressModel(ctx, task); p != nil {
			provider = p
		}
	}

	// Identify tool-result messages to compress.
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

	const summaryPrefix = "[Summary of earlier tool results]\n"
	var oldMsgs []core.ChatMessage
	var toRemove []int
	for i := 0; i < preserveFrom; i++ {
		m := messages[i]
		switch {
		case m.ToolCallID != "" && m.Content != "":
			oldMsgs = append(oldMsgs, m)
			toRemove = append(toRemove, i)
		case m.Role == "user" && strings.HasPrefix(m.Content, summaryPrefix) && i > 0:
			oldMsgs = append(oldMsgs, m)
			toRemove = append(toRemove, i)
		}
	}
	if len(toRemove) == 0 {
		return messages, currentRuneCount
	}

	// Start compression span if tracing.
	compressCtx := ctx
	if cfg.Tracer != nil {
		var span core.Span
		compressCtx, span = cfg.Tracer.Start(ctx, "agent.loop.compress",
			core.IntAttr("original_runes", currentRuneCount),
			core.IntAttr("messages_compressed", len(toRemove)))
		defer span.End()
	}

	compactor := cfg.Compressor
	if compactor == nil {
		compactor = NewInlineCompactor(provider)
	}

	result, err := compactor.Compact(compressCtx, core.CompactRequest{
		Messages:           oldMsgs,
		Scope:              core.ScopeToolResultsOnly,
		SummarizerProvider: provider,
	})
	if err != nil {
		cfg.Logger.Warn("context compression failed, continuing uncompressed", "error", err)
		return messages, currentRuneCount
	}

	removeSet := make([]bool, len(messages))
	for _, idx := range toRemove {
		removeSet[idx] = true
	}
	compressed := make([]core.ChatMessage, 0, len(messages)-len(toRemove)+1)
	summaryInserted := false
	for i, m := range messages {
		if removeSet[i] {
			if !summaryInserted {
				compressed = append(compressed, core.UserMessage(summaryPrefix+result.SummaryText))
				summaryInserted = true
			}
			continue
		}
		compressed = append(compressed, m)
	}

	newRuneCount := runeCount(compressed)
	cfg.Logger.Info("context compressed",
		"agent", cfg.Name,
		"before_runes", currentRuneCount,
		"after_runes", newRuneCount,
		"messages_removed", len(toRemove))

	return compressed, newRuneCount
}
