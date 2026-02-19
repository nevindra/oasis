// Package oasis is an AI assistant framework for building conversational agents in Go.
//
// It provides modular, interface-driven building blocks: LLM providers,
// embedding providers, vector storage, long-term memory, a tool execution system,
// a document ingestion pipeline, and messaging frontend abstractions.
//
// # Quick Start
//
// Create an agent using the LLMAgent primitive:
//
//	provider := gemini.New(apiKey, model)
//	embedding := gemini.NewEmbedding(apiKey)
//	store := sqlite.New("oasis.db")
//	memoryStore := sqlitemem.New("memory.db")
//
//	agent := oasis.NewLLMAgent(
//		"assistant",
//		"You are a helpful assistant.",
//		provider,
//		oasis.WithTools(
//			knowledge.New(store, embedding),
//			search.New(),
//		),
//		oasis.WithConversationMemory(store, oasis.CrossThreadSearch(embedding)),
//		oasis.WithUserMemory(memoryStore, embedding),
//	)
//
//	result, err := agent.Execute(ctx, "What's the weather like?")
//
// # Core Interfaces
//
// The root package defines the contracts that all components implement:
//
//   - [Agent] — composable work unit (LLMAgent, Network, Workflow, or custom)
//   - [Provider] — LLM backend (chat, tool calling, streaming)
//   - [EmbeddingProvider] — text-to-vector embedding
//   - [Store] — persistence with vector search
//   - [MemoryStore] — long-term semantic memory
//   - [Tool] — pluggable capability for LLM function calling
//   - [PreProcessor], [PostProcessor], [PostToolProcessor] — message/response/tool result transformers
//
// # Included Implementations
//
// Providers: provider/gemini (Google Gemini), provider/openaicompat (OpenAI-compatible APIs).
// Storage: store/sqlite (local), store/libsql (Turso/remote).
// Tools: tools/knowledge, tools/remember, tools/search, tools/schedule, tools/shell, tools/file, tools/http.
//
// See the cmd/bot_example directory for a complete reference application.
package oasis
