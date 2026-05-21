package memory

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/nevindra/oasis/core"
)

// countingEmbed counts the total number of texts passed across all Embed calls.
type countingEmbed struct {
	calls    atomic.Int64 // total Embed invocations
	textsSum atomic.Int64 // sum of len(texts) across all invocations
}

func (c *countingEmbed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.calls.Add(1)
	c.textsSum.Add(int64(len(texts)))
	out := make([][]float32, len(texts))
	for i, t := range texts {
		// Deterministic non-zero vector keyed by length so different texts produce
		// different embeddings — enough for cosine similarity to distinguish them.
		out[i] = []float32{float32(len(t)), 0, 0}
	}
	return out, nil
}
func (c *countingEmbed) Dimensions() int { return 3 }
func (c *countingEmbed) Name() string    { return "counting" }

func TestSemanticTrimReusesCachedEmbeddings(t *testing.T) {
	emb := &countingEmbed{}
	m := &AgentMemory{
		embedding:        emb,
		semanticTrimming: true,
		maxTokens:        5, // tiny budget forces trimming
		keepRecent:       1,
		logger:           slog.Default(),
	}

	// 5 older messages + 1 recent → trimHistory embeds 4 older texts (keepRecent=1).
	messages := []core.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "msg one is a moderately long message"},
		{Role: "assistant", Content: "msg two is a moderately long message"},
		{Role: "user", Content: "msg three is a moderately long message"},
		{Role: "assistant", Content: "msg four is a moderately long message"},
		{Role: "user", Content: "msg five recent"},
	}
	inputEmb := []float32{1, 0, 0}
	totalTokens := 0
	for i := 1; i < len(messages); i++ {
		totalTokens += estimateTokens(messages[i])
	}

	// First trim — all 4 older messages are embedded.
	_ = m.trimHistory(context.Background(), messages, 1, len(messages), totalTokens, inputEmb)
	if got := emb.textsSum.Load(); got != 4 {
		t.Fatalf("first trim: expected 4 texts embedded, got %d", got)
	}

	// Second trim with the same older messages — all hits, zero new texts embedded.
	before := emb.textsSum.Load()
	_ = m.trimHistory(context.Background(), messages, 1, len(messages), totalTokens, inputEmb)
	delta := emb.textsSum.Load() - before
	if delta != 0 {
		t.Fatalf("second trim: expected 0 texts embedded via cache, got %d", delta)
	}

	// Replace one of the older messages with a brand-new text.
	messages[2] = core.ChatMessage{Role: "assistant", Content: "msg two replacement — different content"}
	before = emb.textsSum.Load()
	_ = m.trimHistory(context.Background(), messages, 1, len(messages), totalTokens, inputEmb)
	delta = emb.textsSum.Load() - before
	if delta != 1 {
		t.Fatalf("partial-overlap trim: expected exactly 1 new text embedded, got %d", delta)
	}
}
