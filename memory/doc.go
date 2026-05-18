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
//	import "github.com/nevindra/oasis/memory"
//
//	agent := oasis.NewLLMAgent(
//		oasis.WithConversationMemory(store, 10),  // last 10 messages
//		oasis.WithUserMemory(memStore, provider), // facts + extraction
//		oasis.WithCrossThreadSearch(),            // semantic recall
//	)
//
// # Architecture
//
// The memory package is responsible for:
//
//  - Loading and assembling conversation context (buildMessages)
//  - Token-based and semantic trimming for prompt optimization
//  - Semantic search over cross-thread messages
//  - Automatic fact extraction and persistence
//  - Thread lifecycle (creation, title generation)
//
// All memory features are optional. The agent works without any Store
// configured, and individual features (user memory, cross-thread search, etc.)
// can be mixed and matched.
package memory
