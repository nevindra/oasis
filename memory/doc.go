// Package memory provides conversational memory wiring for LLM agents.
//
// It implements a composable memory system with three tiers:
//
//  1. Conversation History (Store) — persists all messages in a thread
//  2. Cross-Thread Search — semantic recall from past conversations
//  3. Semantic Memory — extracted user facts with deduplication
//
// # Usage
//
// Configure memory when setting up an agent:
//
//	import (
//	    "github.com/nevindra/oasis"
//	    "github.com/nevindra/oasis/memory"
//	)
//
//	agent := oasis.NewAgent(name, desc, provider,
//		oasis.WithMemory(
//			memory.WithStore(store),
//			memory.WithEmbedding(embedding),
//			memory.WithHistory(memory.HistoryConfig{MaxMessages: 10}),
//			memory.WithSemanticRecall(), // cross-thread recall
//		),
//	)
//
// # Architecture
//
// The memory package is responsible for:
//
//   - Loading and assembling conversation context (buildMessages)
//   - Token-based and semantic trimming for prompt optimization
//   - Semantic search over cross-thread messages
//   - Automatic fact extraction and persistence via the ingest pipeline
//   - Thread lifecycle (creation, title generation)
//
// All memory features are optional. The agent works without any Store
// configured, and individual features (cross-thread search, item memory, etc.)
// can be mixed and matched.
package memory
