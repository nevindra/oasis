package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/nevindra/oasis/core"
)

// NewInlineCompactor returns a Compactor that delegates to the given LLM
// provider using a default prompt. Handles both ScopeFull (full-thread
// synopsis) and ScopeToolResultsOnly (tool-result compression). This is the
// default Compactor when none is configured via WithCompactor.
func NewInlineCompactor(provider core.Provider) core.Compactor {
	return &inlineCompactor{provider: provider}
}

type inlineCompactor struct {
	provider core.Provider
}

func (c *inlineCompactor) Compact(ctx context.Context, req core.CompactRequest) (core.CompactResult, error) {
	if c.provider == nil {
		return core.CompactResult{}, fmt.Errorf("inlineCompactor: no provider configured")
	}
	summarizer := req.SummarizerProvider
	if summarizer == nil {
		summarizer = c.provider
	}

	var prompt string
	switch req.Scope {
	case core.ScopeToolResultsOnly:
		prompt = "Summarize the following tool execution results concisely. " +
			"Preserve key facts, data values, decisions, and errors. Omit redundant details."
	default: // ScopeFull
		prompt = "Summarize the following conversation, preserving key decisions, " +
			"facts, and intermediate results. Be concise but complete."
	}

	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}

	resp, err := core.Chat(ctx, summarizer, core.ChatRequest{
		Messages: []core.ChatMessage{
			core.SystemMessage(prompt),
			core.UserMessage(b.String()),
		},
	})
	if err != nil {
		return core.CompactResult{}, err
	}

	return core.CompactResult{
		SummaryText:   resp.Content,
		Sections:      map[string]string{"summary": resp.Content},
		SourceTokens:  resp.Usage.InputTokens,
		SummaryTokens: resp.Usage.OutputTokens,
	}, nil
}
