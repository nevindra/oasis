package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis"
)

// KnowledgeTool searches the knowledge base and past conversations.
//
// By default, New creates a HybridRetriever internally with default settings.
// To configure retrieval behavior (score threshold, filters, keyword weight,
// re-ranking), construct a Retriever with the options you need and inject it:
//
//	retriever := oasis.NewHybridRetriever(store, embedding,
//	    oasis.WithMinRetrievalScore(0.05),
//	    oasis.WithKeywordWeight(0.4),
//	    oasis.WithFilters(oasis.ByDocument("doc-123")),
//	    oasis.WithReranker(oasis.NewScoreReranker(0.1)),
//	)
//	tool := knowledge.New(store, embedding,
//	    knowledge.WithRetriever(retriever),
//	    knowledge.WithTopK(10),
//	)
type KnowledgeTool struct {
	retriever oasis.Retriever
	store     oasis.Store
	embedding oasis.EmbeddingProvider
	topK      int
}

// Option configures a KnowledgeTool.
type Option func(*KnowledgeTool)

// WithRetriever injects a custom Retriever. When not set, New creates a
// default HybridRetriever from the provided store and embedding provider.
func WithRetriever(r oasis.Retriever) Option {
	return func(k *KnowledgeTool) { k.retriever = r }
}

// WithTopK sets the number of results to retrieve. Default is 5.
func WithTopK(n int) Option {
	return func(k *KnowledgeTool) { k.topK = n }
}

// New creates a KnowledgeTool. If no Retriever is provided via WithRetriever,
// a default HybridRetriever is created from store and embedding.
func New(store oasis.Store, emb oasis.EmbeddingProvider, opts ...Option) *KnowledgeTool {
	k := &KnowledgeTool{store: store, embedding: emb, topK: 5}
	for _, o := range opts {
		o(k)
	}
	if k.retriever == nil {
		k.retriever = oasis.NewHybridRetriever(store, emb)
	}
	return k
}

func (k *KnowledgeTool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{{
		Name:        "knowledge_search",
		Description: "Search the user's personal knowledge base for previously saved information, documents, and past conversations.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query"}},"required":["query"]}`),
	}}
}

func (k *KnowledgeTool) Execute(ctx context.Context, _ string, args json.RawMessage) (oasis.ToolResult, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}

	// Retrieve from knowledge base via Retriever
	chunks, err := k.retriever.Retrieve(ctx, params.Query, k.topK)
	if err != nil {
		return oasis.ToolResult{Error: "retrieval error: " + err.Error()}, nil
	}

	// Search past conversations
	embs, err := k.embedding.Embed(ctx, []string{params.Query})
	if err != nil {
		return oasis.ToolResult{Error: "embedding error: " + err.Error()}, nil
	}
	var messages []oasis.ScoredMessage
	if len(embs) > 0 {
		messages, err = k.store.SearchMessages(ctx, embs[0], 5)
		if err != nil {
			return oasis.ToolResult{Error: "message search error: " + err.Error()}, nil
		}
	}

	var out strings.Builder
	if len(chunks) > 0 {
		out.WriteString("From knowledge base:\n")
		for i, r := range chunks {
			fmt.Fprintf(&out, "%d. %s\n", i+1, r.Content)
			for _, ec := range r.GraphContext {
				fmt.Fprintf(&out, "   ↳ Related: %q (%s)\n", ec.Description, ec.Relation)
			}
			out.WriteString("\n")
		}
	}
	if len(messages) > 0 {
		out.WriteString("From past conversations:\n")
		for _, sm := range messages {
			fmt.Fprintf(&out, "[%s]: %s\n", sm.Role, sm.Content)
		}
	}
	if out.Len() == 0 {
		fmt.Fprintf(&out, "No relevant information found for %q.", params.Query)
	}

	return oasis.ToolResult{Content: out.String()}, nil
}
