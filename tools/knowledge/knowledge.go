package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis"
)

// KnowledgeTool searches the knowledge base and past conversations.
type KnowledgeTool struct {
	store     oasis.Store
	embedding oasis.EmbeddingProvider
	topK      int
}

// New creates a KnowledgeTool with default topK of 5.
func New(store oasis.Store, emb oasis.EmbeddingProvider) *KnowledgeTool {
	return &KnowledgeTool{store: store, embedding: emb, topK: 5}
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

	embs, err := k.embedding.Embed(ctx, []string{params.Query})
	if err != nil {
		return oasis.ToolResult{Error: "embedding error: " + err.Error()}, nil
	}
	if len(embs) == 0 {
		return oasis.ToolResult{Error: "no embedding returned"}, nil
	}

	embedding := embs[0]

	chunks, err := k.store.SearchChunks(ctx, embedding, k.topK)
	if err != nil {
		return oasis.ToolResult{Error: "chunk search error: " + err.Error()}, nil
	}

	messages, err := k.store.SearchMessages(ctx, embedding, 5)
	if err != nil {
		return oasis.ToolResult{Error: "message search error: " + err.Error()}, nil
	}

	var out strings.Builder
	if len(chunks) > 0 {
		out.WriteString("From knowledge base:\n")
		for i, sc := range chunks {
			fmt.Fprintf(&out, "%d. %s\n\n", i+1, sc.Content)
		}
	}
	if len(messages) > 0 {
		out.WriteString("From past conversations:\n")
		for _, sm := range messages {
			fmt.Fprintf(&out, "[%s]: %s\n", sm.Role, sm.Content)
		}
	}
	if out.Len() == 0 {
		out.WriteString(fmt.Sprintf("No relevant information found for %q.", params.Query))
	}

	return oasis.ToolResult{Content: out.String()}, nil
}
