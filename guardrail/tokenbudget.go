package guardrail

import (
	"context"
	"log/slog"
	"unicode/utf8"

	"github.com/nevindra/oasis/core"
)

// TokenBudgetGuard trims the oldest non-system messages from a request until
// its estimated token count fits maxTokens. It is the cheap, lossless
// complement to compaction (which summarizes). Stateless and safe for
// concurrent use.
//
// Why a heuristic estimate: Oasis ships no BPE tokenizer (own-your-deps). The
// default estimate runs hot (~1 token / 3 runes) so it trims early rather than
// overflowing a provider context window. Supply WithEstimator to plug in a
// real tokenizer.
type TokenBudgetGuard struct {
	maxTokens    int
	preserveLast int
	estimate     func([]core.ChatMessage) int
	logger       *slog.Logger
}

// TokenBudgetOption configures a TokenBudgetGuard.
type TokenBudgetOption func(*TokenBudgetGuard)

// NewTokenBudgetGuard returns a guard that keeps the request under maxTokens.
// A non-positive maxTokens disables trimming.
func NewTokenBudgetGuard(maxTokens int, opts ...TokenBudgetOption) *TokenBudgetGuard {
	g := &TokenBudgetGuard{
		maxTokens:    maxTokens,
		preserveLast: 1,
		estimate:     defaultTokenEstimate,
	}
	for _, opt := range opts {
		opt(g)
	}
	if g.logger == nil {
		g.logger = nopLogger
	}
	if g.estimate == nil {
		g.estimate = defaultTokenEstimate
	}
	return g
}

// WithEstimator overrides the token estimator (e.g. a real tokenizer).
func WithEstimator(fn func([]core.ChatMessage) int) TokenBudgetOption {
	return func(g *TokenBudgetGuard) { g.estimate = fn }
}

// PreserveLast guarantees the n most recent messages are never trimmed.
func PreserveLast(n int) TokenBudgetOption {
	return func(g *TokenBudgetGuard) { g.preserveLast = n }
}

// TokenBudgetLogger sets the guard's logger.
func TokenBudgetLogger(l *slog.Logger) TokenBudgetOption {
	return func(g *TokenBudgetGuard) { g.logger = l }
}

// PreLLM trims oldest non-system messages until the estimate fits.
func (g *TokenBudgetGuard) PreLLM(_ context.Context, req *core.ChatRequest) error {
	if g.maxTokens <= 0 || g.estimate(req.Messages) <= g.maxTokens {
		return nil
	}
	msgs := req.Messages
	// Trim oldest non-system, non-preserved messages first.
	for g.estimate(msgs) > g.maxTokens {
		idx := firstTrimmable(msgs, g.preserveLast)
		if idx < 0 {
			break // nothing left to trim but the protected set
		}
		msgs = append(msgs[:idx], msgs[idx+1:]...)
	}
	// Cleanup: remove any tool-result whose originating tool call is no longer
	// present in the remaining messages. A leading-only check is insufficient
	// because a system message at index 0 causes orphaned tool-results at
	// later positions to survive undetected.
	presentIDs := make(map[string]struct{})
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			presentIDs[tc.ID] = struct{}{}
		}
	}
	filtered := msgs[:0]
	for _, m := range msgs {
		if m.ToolCallID != "" {
			if _, ok := presentIDs[m.ToolCallID]; !ok {
				continue // orphaned tool-result — drop it
			}
		}
		filtered = append(filtered, m)
	}
	msgs = filtered
	if len(msgs) != len(req.Messages) {
		g.logger.Warn("token budget trimmed messages",
			"from", len(req.Messages), "to", len(msgs), "max_tokens", g.maxTokens)
	}
	req.Messages = msgs
	return nil
}

// firstTrimmable returns the index of the oldest message that is neither a
// system message nor inside the preserveLast window, or -1 if none.
func firstTrimmable(msgs []core.ChatMessage, preserveLast int) int {
	cutoff := len(msgs) - preserveLast
	for i := 0; i < cutoff; i++ {
		if msgs[i].Role == "system" {
			continue
		}
		return i
	}
	return -1
}

// defaultTokenEstimate mirrors the compaction heuristic: ~1 token per 3 runes,
// padded hot, plus a fixed cost per image/PDF attachment.
func defaultTokenEstimate(msgs []core.ChatMessage) int {
	const attachmentTokens = 2000
	var runes, media int
	for _, m := range msgs {
		runes += utf8.RuneCountInString(m.Content)
		media += len(m.Attachments)
	}
	return (runes*4)/3/4 + media*attachmentTokens
}

// compile-time check
var _ core.PreProcessor = (*TokenBudgetGuard)(nil)
