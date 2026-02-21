# Memory and Recall

This guide covers practical patterns for wiring conversation memory, cross-thread recall, and user memory into your agents.

## Basic Conversation Memory

Load/persist message history per thread:

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithConversationMemory(store),
)

// Must pass thread_id for history to work
result, _ := agent.Execute(ctx, oasis.AgentTask{
    Input: "What did we just talk about?",
    Context: map[string]any{
        oasis.ContextThreadID: "thread-123",
    },
})
```

Without `thread_id`, the agent runs stateless — no history loaded or persisted.

## History Limits

Control how much conversation history is loaded before each LLM call. Two options compose — whichever triggers first wins:

```go
// By message count (default: 10 most recent messages)
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithConversationMemory(store, oasis.MaxHistory(30)),
)

// By estimated token budget — trim oldest-first to fit
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithConversationMemory(store, oasis.MaxTokens(4000)),
)

// Both compose — whichever triggers first wins
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithConversationMemory(store,
        oasis.MaxHistory(50),
        oasis.MaxTokens(4000),
    ),
)
```

Token estimation uses a ~4 characters per token heuristic with a small overhead for message framing. This is a rough estimate suitable for budget control — not exact tokenizer output.

## Cross-Thread Recall

Search past conversations for relevant context:

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithConversationMemory(store,
        oasis.CrossThreadSearch(embedding),
    ),
)
```

When the agent receives "What do you know about Go?", it embeds the query and searches all stored messages. Relevant results from other threads are injected into the system prompt.

### Tuning the Threshold

```go
// Higher threshold = more relevant but fewer results
oasis.CrossThreadSearch(embedding, oasis.MinScore(0.75))

// Lower threshold = more results but noisier (default: 0.60)
oasis.CrossThreadSearch(embedding, oasis.MinScore(0.50))
```

## User Memory (Long-term Facts)

Learn and remember things about the user:

```go
// SQLite
store := sqlite.New("oasis.db")
store.Init(ctx)
memoryStore := sqlite.NewMemoryStore(store.DB())
memoryStore.Init(ctx)

// PostgreSQL alternative:
//   store := postgres.New(pool)
//   memoryStore := postgres.NewMemoryStore(pool)

// libSQL/Turso alternative:
//   store := libsql.New(dbURL, authToken)
//   memoryStore := libsql.NewMemoryStore(store.DB())

agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithConversationMemory(store),  // required for write path
    oasis.WithUserMemory(memoryStore, embedding),
)
```

After each conversation turn, the agent automatically:
1. Extracts durable facts ("User's favorite language is Go")
2. Handles contradictions ("User now prefers Rust" supersedes the Go fact)
3. Upserts with semantic deduplication

## Full Setup

All three memory layers together:

```go
agent := oasis.NewLLMAgent("assistant", "Personal assistant", llm,
    oasis.WithTools(searchTool, scheduleTool),
    oasis.WithConversationMemory(store,
        oasis.CrossThreadSearch(embedding, oasis.MinScore(0.7)),
    ),
    oasis.WithUserMemory(memoryStore, embedding),
    oasis.WithPrompt("You are a personal assistant. Use your memory of the user to give personalized responses."),
)
```

## What Happens During Execute

1. Load recent messages from Store (conversation memory)
2. Embed the input, search all threads for similar messages (cross-thread)
3. Embed the input, retrieve relevant user facts from MemoryStore
4. Build the system prompt: base prompt + user facts + cross-thread context
5. Run the tool-calling loop
6. Persist the user message and assistant response
7. (Background) Extract user facts and upsert to MemoryStore

## Without Memory

Agents are fully functional without memory. Skip the options and they run stateless:

```go
agent := oasis.NewLLMAgent("worker", "Task executor", llm,
    oasis.WithTools(searchTool),
)
// No history, no recall, no fact extraction. Just tools.
```

## See Also

- [Memory Concept](../concepts/memory.md) — confidence system, extraction pipeline
- [Store Concept](../concepts/store.md) — persistence layer
