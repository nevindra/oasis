package memory

import (
	"context"
	"log/slog"

	"github.com/nevindra/oasis/core"
)

// ExampleAgentMemory shows how to wire memory into an agent using the Init method.
func ExampleAgentMemory() {
	// This example demonstrates the memory wiring pattern.
	// In production, you would use a real Store and EmbeddingProvider.

	var store core.Store                 // typically: sqlite.NewStore() or postgres.NewStore()
	var embedding core.EmbeddingProvider // typically: gemini.NewEmbedding() or openai.Embedding()
	var memoryStore MemoryStore          // typically: sqlite.NewMemoryStore()
	var provider core.Provider           // typically: gemini.NewProvider() or other

	// Configure memory for an agent with all features enabled
	var am AgentMemory
	am.Init(AgentMemoryConfig{
		Store:             store,
		Embedding:         embedding,
		Memory:            memoryStore,
		CrossThreadSearch: true,   // enable semantic recall
		SemanticMinScore:  0.6,    // similarity threshold
		MaxHistory:        10,     // keep last 10 messages
		MaxTokens:         4000,   // trim to 4k tokens
		AutoTitle:         true,   // generate thread titles
		Provider:          provider, // for fact extraction
		SemanticTrimming:  true,   // drop low-relevance messages
		KeepRecent:        3,      // always preserve last 3
		Logger:            slog.New(slog.NewTextHandler(nil, nil)),
	})

	// Build messages for a task (normally called by the agent)
	task := core.AgentTask{Input: "What's my timezone?"}
	ctx := context.Background()

	messages := am.BuildMessages(ctx, "example_agent", "You are a helpful assistant.", task)
	_ = messages // typically passed to LLM provider
}
