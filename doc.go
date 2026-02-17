// Package oasis is an AI assistant framework for building conversational agents in Go.
//
// It provides modular, interface-driven building blocks: LLM providers,
// embedding providers, vector storage, long-term memory, a tool execution system,
// a document ingestion pipeline, and messaging frontend abstractions.
//
// # Quick Start
//
// Create an agent by composing implementations of the core interfaces:
//
//	agent := oasis.New(
//		oasis.WithProvider(gemini.New(apiKey, model)),
//		oasis.WithEmbedding(gemini.NewEmbedding(apiKey)),
//		oasis.WithStore(sqlite.New("oasis.db")),
//		oasis.WithFrontend(telegram.New(token)),
//		oasis.WithSystemPrompt("You are a helpful assistant."),
//	)
//	agent.AddTool(knowledge.New(agent.Store(), agent.Embedding()))
//	agent.Run(ctx)
//
// # Core Interfaces
//
// The root package defines the contracts that all components implement:
//
//   - [Provider] — LLM backend (chat, tool calling, streaming)
//   - [EmbeddingProvider] — text-to-vector embedding
//   - [Frontend] — messaging platform (Telegram, Discord, CLI, etc.)
//   - [VectorStore] — persistence with vector search
//   - [MemoryStore] — long-term semantic memory
//   - [Tool] — pluggable capability for LLM function calling
//
// # Included Implementations
//
// Providers: provider/gemini (Google Gemini), provider/openaicompat (OpenAI-compatible APIs).
// Storage: store/sqlite (local), store/libsql (Turso/remote).
// Frontends: frontend/telegram.
// Tools: tools/knowledge, tools/remember, tools/search, tools/schedule, tools/shell, tools/file, tools/http.
//
// See the cmd/bot_example directory for a complete reference application.
package oasis
