# Memory Examples

---

## Recipe 1: Conversation history with token budgeting

**Goal:** Persist the last 20 messages and trim to a 100 000-token budget, dropping the least-relevant messages first.

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/memory"
)

agent := oasis.NewLLMAgent("assistant", "helpful assistant", provider,
    oasis.WithMemory(
        memory.WithStore(store),
        memory.WithEmbedding(embedder),
        memory.WithMaxHistory(20),
        memory.WithMaxTokens(100_000),
        memory.WithSemanticTrimming(),
        memory.WithKeepRecent(5),
    ),
)

result, err := agent.Execute(ctx, core.AgentTask{
    ThreadID: "user-abc-chat-1",
    ChatID:   "user-abc",
    Input:    "What did we decide about the API?",
})
```

**Plain-English walkthrough:**
- `WithMaxHistory(20)` loads the last 20 messages into the prompt.
- `WithMaxTokens(100_000)` sets the ceiling. When the prompt exceeds it, trimming fires.
- `WithSemanticTrimming()` scores each older message by cosine similarity to the current input, dropping the least-relevant ones first. Without this option, the oldest messages are dropped instead.
- `WithKeepRecent(5)` anchors the five most-recent messages — they are always kept, no matter how irrelevant the scorer thinks they are.

**Variations:**
- Drop `WithSemanticTrimming()` and `WithEmbedding` if you don't have an embedder — oldest-first trimming works without embeddings.
- Set `WithMaxTokens(0)` to disable the cap entirely; all 20 messages always go in.
- Override the trimming embedder with a faster/cheaper model: `memory.WithSemanticTrimEmbedding(fastEmbedder)`.

---

## Recipe 2: Cross-thread semantic recall

**Goal:** When the user asks about a topic, surface relevant messages from their *other* conversations and inject them into the system prompt.

```go
agent := oasis.NewLLMAgent("assistant", "helpful assistant", provider,
    oasis.WithMemory(
        memory.WithStore(store),
        memory.WithEmbedding(embedder),
        memory.WithSemanticRecall(),
        memory.WithSemanticRecallMinScore(0.65),
    ),
)

result, err := agent.Execute(ctx, core.AgentTask{
    ThreadID: "user-abc-chat-42",
    ChatID:   "user-abc",
    Input:    "Remind me what we decided about the database schema.",
})
```

**Plain-English walkthrough:**
- `WithSemanticRecall()` embeds the current user input and searches across all threads for the same `ChatID`.
- `WithSemanticRecallMinScore(0.65)` sets the cosine similarity floor. Lower = more results, noisier. Higher = fewer results, more precise.
- The recalled messages appear in the system prompt before the conversation history. The LLM sees them and can reference them in its answer.

**Variations:**
- Leave out `WithSemanticRecallMinScore`; the default is `0.60`.
- Combine with `WithRecallKinds(memory.KindFact)` to also surface extracted facts — not just raw messages.
- Combine with `WithRecallTopK(12)` to broaden the result count (default is 8).

---

## Recipe 3: Saving and retrieving structured memory items

**Goal:** Explicitly save a fact about a user and later retrieve it in a new session.

```go
// After building the agent and running at least one turn:
mem := agent.Memory()

// Save a fact
err := mem.Remember(ctx, memory.MemoryItem{
    Kind:    memory.KindFact,
    Content: "User prefers dark mode and short answers.",
    Scope:   memory.Scoped(memory.ScopeResource, "user-abc"),
    Tags:    []string{"preference", "ui"},
})

// Retrieve it later (from any session for user-abc)
results, err := mem.Recall(ctx, "display preferences",
    memory.RecallKind(memory.KindFact),
    memory.RecallScope(memory.Scoped(memory.ScopeResource, "user-abc")),
    memory.RecallLimit(3),
)
for _, r := range results {
    fmt.Printf("[%.2f] %s\n", r.Score, r.Item.Content)
}
```

**Plain-English walkthrough:**
- `Remember` writes the item to the store. If `ID` is empty, a fresh one is generated. If `Embedding` is empty and an embedder is configured, the embedding is backfilled before writing.
- `Recall` embeds the query string, then runs a vector search. The `ScopeResource` scope means it only returns items for `user-abc`, not other users.
- `RecallLimit(3)` caps results; default is 5.

**Variations:**
- Use `mem.Pin(ctx, id, true)` to mark a fact as always-loaded — it will appear in every prompt for that agent + scope, regardless of relevance.
- Use `mem.Forget(ctx, memory.ForgetByKind(memory.KindFact))` to clear all facts for a user when they ask to be forgotten.
- Use `mem.List(ctx, memory.Filter{Kinds: []memory.Kind{memory.KindFact}})` to enumerate all facts without an embedding query.

---

## Recipe 4: Agent-callable memory tools

**Goal:** Let the LLM save and look up memory items during a turn — the model decides what to remember.

```go
var m memory.AgentMemory
m.Init(memory.BuildConfig(
    memory.WithStore(store),
    memory.WithEmbedding(embedder),
))

agent := oasis.NewLLMAgent("assistant", "helpful assistant", provider,
    oasis.WithMemory(
        memory.WithStore(store),
        memory.WithEmbedding(embedder),
        memory.WithTools(m.AllTools()...),
    ),
)
```

**Plain-English walkthrough:**
- `m.AllTools()` returns four tools: `memory.remember`, `memory.recall`, `memory.forget`, `memory.pin`.
- `WithTools(...)` registers them with the agent so the LLM can call them during any turn.
- The LLM can now write `{"tool": "memory.remember", "args": {"content": "user is in Berlin"}}` and the item is persisted.

**Variations:**
- Register only the tools you want: `memory.WithTools(m.RecallTool(), m.RememberTool())` — omit `ForgetTool` if you don't want the model to delete things.
- The `memory.remember` tool defaults `kind` to `"fact"`. The LLM can override: `{"content": "...", "kind": "event"}`.

---

## Recipe 5: Working memory (per-session scratchpad)

**Goal:** Give the agent a single writable slot it can use as a markdown scratchpad that persists across turns but can be overwritten.

```go
agent := oasis.NewLLMAgent("planner", "task planner", provider,
    oasis.WithMemory(
        memory.WithStore(store),
        memory.WithWorkingMemory(),
        memory.WithWorkingMemoryScope(memory.ScopeThread),
    ),
)
```

**Plain-English walkthrough:**
- `WithWorkingMemory()` enables a `KindNote` item whose ID is derived deterministically from `(agentName, scope)` using SHA-256. Upserting it always overwrites the same row.
- `WithWorkingMemoryScope(ScopeThread)` means the scratchpad is private to a single thread. Use `ScopeResource` (the default) if you want it shared across all threads for the same user.
- Your code (or a custom processor) can write to it with `mem.Remember(ctx, memory.MemoryItem{ID: memory.WorkingMemoryID(agentName, scope), Kind: memory.KindNote, Content: "..."})`.

**Variations:**
- Use `ScopeAgent` scope if you want the scratchpad shared across all users for an agent (e.g., a global task queue).
- Read the working memory with `mem.Get(ctx, memory.WorkingMemoryID(agentName, scope))`.

---

## Recipe 6: Compaction — keep long conversations under the context limit

**Goal:** When a thread's stored history exceeds 80% of the model's context window, replace the oldest portion with a structured summary.

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/memory"
    "github.com/nevindra/oasis/compaction"
)

compactor := compaction.NewStructuredCompactor(provider)

agent := oasis.NewLLMAgent("agent", "long-running assistant", provider,
    oasis.WithMemory(
        memory.WithStore(store),
        memory.WithMaxTokens(120_000),
        memory.WithCompaction(compactor, 0.80),
    ),
)
```

**Plain-English walkthrough:**
- `NewStructuredCompactor(provider)` wraps any `core.Provider` and produces a 9-section structured summary on each compaction call.
- `WithCompaction(compactor, 0.80)` fires the compactor when stored history exceeds 80% of the effective context window. The oldest messages are summarized and replaced with a `KindSummary` item.
- The agent loop triggers compaction automatically — you don't call the compactor directly.

**Variations:**
- Use `WithCompress(modelFunc, 200_000)` instead of (or alongside) `WithCompaction` for pure in-memory compression that doesn't touch the store — useful for long single-session tasks.
- Add `FocusHint` via a custom `CompactRequest` if you call the compactor directly: `compactor.Compact(ctx, core.CompactRequest{Messages: msgs, FocusHint: "preserve all code snippets"})`.
- Check `result.Warnings` after a direct `Compact` call — `"partial_sections"` means fewer than 9 sections were parsed, which is still usable.

---

## Recipe 7: Custom ingest processor

**Goal:** After every turn, tag new items with a project label based on the thread's chat ID.

```go
type ProjectTagger struct {
    ProjectMap map[string]string // chatID -> project name
}

func (p ProjectTagger) Process(ctx context.Context, in *memory.IngestContext) error {
    proj, ok := p.ProjectMap[in.Task.ChatID]
    if !ok {
        return nil
    }
    for i := range in.Candidates {
        in.Candidates[i].Tags = append(in.Candidates[i].Tags, "project:"+proj)
    }
    return nil
}

agent := oasis.NewLLMAgent("agent", "project assistant", provider,
    oasis.WithMemory(
        memory.WithStore(store),
        memory.WithIngestProcessors(ProjectTagger{
            ProjectMap: map[string]string{"chat-alpha": "moonshot"},
        }),
    ),
)
```

**Plain-English walkthrough:**
- `IngestProcessor.Process` is called after the default chain (fact extraction, dedup, embedding). `in.Candidates` holds all items about to be written to the store.
- Mutating `in.Candidates` here adds the tag before the terminal `Upserter` writes them.
- Returning a non-nil error aborts the rest of the pipeline but does not surface to the user — the agent turn already completed.

**Variations:**
- Add a `RetrieveProcessor` with `WithRetrieveProcessors` to inject custom text into `in.PromptParts` before the messages are assembled.
- Use the `in.Store` handle inside a processor to query or write to the store directly.
