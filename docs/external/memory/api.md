# Memory API

Import paths used in this section:

```go
import (
    "github.com/nevindra/oasis/memory"
    "github.com/nevindra/oasis/compaction"
    "github.com/nevindra/oasis/core"
)
```

Types and constructors are also re-exported from `github.com/nevindra/oasis` (the root package) as type aliases, so you can use `oasis.MemoryItem`, `oasis.Kind`, etc. if you prefer a single import.

---

## Types

### `MemoryItem`

The universal record type for all memory layers. One struct covers facts, notes, events, playbooks, reflections, summaries, and any user-defined kinds.

| Field | Type | Notes |
|---|---|---|
| `ID` | `string` | Unique item ID. `Remember` generates one if empty. |
| `Kind` | `Kind` | Discriminates the item type. Required. |
| `Content` | `string` | Canonical text shown to the LLM. Required. Structured data should be JSON-encoded here. |
| `Scope` | `Scope` | Visibility partition. Defaults to `ScopeResource` when zero. |
| `Source` | `Source` | Provenance record. Defaults to `{Kind: "user"}` when zero. |
| `Pinned` | `bool` | Pinned items are always loaded into context, regardless of relevance scoring. |
| `Tags` | `[]string` | Arbitrary labels for `Filter` queries. |
| `Embedding` | `[]float32` | Optional. Backfilled by the `Embedder` ingest processor when an `EmbeddingProvider` is configured. |
| `CreatedAt` | `int64` | Unix seconds. Set to `core.NowUnix()` by `Remember` if zero. |
| `UpdatedAt` | `int64` | Unix seconds. Updated by `Upsert`. |
| `ExpiresAt` | `int64` | Unix seconds. `0` = never expires. Expired items are excluded by default; pass `Filter{IncludeExp: true}` to include them. |

### `Kind`

Open string type. Six canonical constants; callers may define their own.

| Constant | Value | Typical use |
|---|---|---|
| `KindFact` | `"fact"` | Semantic fact about the user or world |
| `KindNote` | `"note"` | Working memory scratchpad |
| `KindEvent` | `"event"` | Episodic event tied to a time |
| `KindPlaybook` | `"playbook"` | Procedural memory ("when X, do Y") |
| `KindReflection` | `"reflection"` | Agent self-critique |
| `KindSummary` | `"summary"` | Hierarchical compaction output |

User-defined kinds work in all pipelines, tools, and filters without additional registration.

### `Scope` and `ScopeKind`

`Scope` anchors an item to a specific instance of a `ScopeKind`.

```go
type Scope struct {
    Kind ScopeKind
    Ref  string   // e.g. user ID, agent ID, or empty for "all"
}
```

Use `memory.Scoped(k, ref)` as shorthand for `Scope{Kind: k, Ref: ref}`.

| `ScopeKind` | Value | Visible to |
|---|---|---|
| `ScopeThread` | `"thread"` | Only messages in the same thread |
| `ScopeResource` | `"resource"` | All threads belonging to one user or chat |
| `ScopeAgent` | `"agent"` | All users for this agent |
| `ScopeGlobal` | `"global"` | Every agent |

### `ScoredItem`

Returned by `Recall` and semantic search. Pairs an item with a cosine similarity score.

```go
type ScoredItem struct {
    Item  MemoryItem
    Score float32  // 0.0–1.0; higher = more similar
}
```

### `Source`

Provenance record embedded in `MemoryItem`.

| Field | Type | Notes |
|---|---|---|
| `Kind` | `string` | `"message"`, `"tool"`, `"user"`, `"agent"`, `"extraction"` |
| `Ref` | `string` | Foreign key (message ID, tool call ID, etc.). May be empty. |
| `AgentID` | `string` | Which agent created or extracted this item. |

### `Filter`

Selects `MemoryItem`s for read or delete queries.

| Field | Type | Notes |
|---|---|---|
| `Kinds` | `[]Kind` | OR filter. Empty = any kind. |
| `Scope` | `*Scope` | `nil` = any scope. Non-nil = exact match on `Kind+Ref`. |
| `Tags` | `[]string` | AND filter — all tags must be present. |
| `Pinned` | `*bool` | `nil` = any; `true` = only pinned; `false` = only unpinned. |
| `Since` | `int64` | `CreatedAt >= Since` (unix seconds). `0` = no lower bound. |
| `Until` | `int64` | `CreatedAt <= Until` (unix seconds). `0` = no upper bound. |
| `Limit` | `int` | Max results. `0` = store default (50). |
| `IncludeExp` | `bool` | Include expired items. Default `false`. |

`Filter.IsEmpty()` returns `true` when no fields are set; `DeleteWhere` rejects an empty filter to prevent accidental full deletes.

### `Store`

The interface your persistence layer must implement. It is the union of `core.Store` (conversation history) and `ItemStore` (memory items). Satellite packages `store/sqlite` and `store/postgres` implement both.

```go
type Store interface {
    core.Store   // conversation messages, threads
    ItemStore    // MemoryItem CRUD + semantic search
}
```

You never implement `Store` yourself in typical usage — use a satellite and pass it to `WithStore`.

### `AgentMemoryConfig`

All-fields struct that holds assembled configuration. You do not construct this directly; use `BuildConfig(opts...)` or let `oasis.WithMemory(opts...)` do it for you.

---

## Constructors

### `memory.BuildConfig(opts ...Option) AgentMemoryConfig`

Applies functional options and returns the config. Useful when you need to inspect or pass the config separately before attaching it to an agent.

Zero-value config is valid — all features are disabled but nothing panics.

---

## Methods on `AgentMemory`

`AgentMemory` is the orchestrator returned by `agent.Memory()`. Obtain it after calling `agent.Execute` once, or build it directly with `var m memory.AgentMemory; m.Init(cfg)`.

### `Remember(ctx, item MemoryItem) error`

Persists a single item. Defaults applied when fields are zero:
- `ID`: `core.NewID()`
- `Scope`: `ScopeResource` with empty ref
- `Source.Kind`: `"user"`
- `CreatedAt`: `core.NowUnix()`
- `Embedding`: backfilled if an `EmbeddingProvider` is configured

Returns an error only if no `Store` is configured or the store write fails.

### `Recall(ctx, query string, opts ...RecallOption) ([]ScoredItem, error)`

Returns items semantically similar to `query`, ranked by cosine similarity. Requires both a `Store` and an `EmbeddingProvider`.

| `RecallOption` | Effect |
|---|---|
| `RecallKind(k)` | Filter to items of kind `k` |
| `RecallScope(s)` | Filter to scope `s` |
| `RecallLimit(n)` | Return at most `n` items (default 5) |

### `Forget(ctx, spec ForgetSpec) (int, error)`

Deletes items matching the spec. Returns the count deleted.

Build a `ForgetSpec` with one of the helpers:

| Helper | Matches |
|---|---|
| `ForgetByID(id)` | Exact item ID |
| `ForgetByMatch(s)` | Substring in `Content` (case-insensitive) |
| `ForgetByKind(k)` | All items of kind `k` |
| `ForgetOlderThan(d)` | Items created more than `d` ago |

Multiple fields on `ForgetSpec` can be combined (except `ID`, which short-circuits to a single delete).

### `List(ctx, filter Filter) ([]MemoryItem, error)`

Returns items matching the filter in `CreatedAt` descending order. Never nil on success.

### `Get(ctx, id string) (MemoryItem, error)`

Returns one item by ID, or `core.ErrNotFound` if it doesn't exist.

### `Pin(ctx, id string, pinned bool) error`

Sets or clears the `Pinned` flag. Pinned items are always loaded into the prompt regardless of relevance.

### `BuildMessages(ctx, agentName, systemPrompt string, task AgentTask) []core.ChatMessage`

Runs the full retrieve pipeline and returns the assembled LLM-ready message list. Called internally by the agent loop; exposed for custom agent implementations.

### `PersistTurn(ctx, agentName string, task AgentTask, userText, asstText string, steps []StepTrace)`

Runs the full ingest pipeline in the background (bounded to 16 concurrent goroutines). Falls back to lightweight message-only persist when all slots are busy. Called internally by the agent loop.

### `Close() error`

Waits for all in-flight background ingest goroutines to finish. Call when shutting down an agent that has been executing turns. Always returns `nil` in the current implementation; the error return is reserved for future remote-store flush.

### `AllTools() []core.AnyTool`

Returns all four agent-callable memory tools: `memory.remember`, `memory.recall`, `memory.forget`, `memory.pin`. See the Tools section below.

---

## Options

Pass these to `oasis.WithMemory(...)` or `memory.BuildConfig(...)`.

| Option | Default | Description |
|---|---|---|
| `WithStore(s)` | `nil` | Wires the Store. Disables all persistence and retrieval when unset. |
| `WithEmbedding(p)` | `nil` | Embedding provider for recall, dedup, and semantic trimming. Required for any semantic feature. |
| `WithProvider(p)` | `nil` | LLM provider used by the fact extractor and title generator during ingest. |
| `WithHistory(cfg)` | see below | Configures history loading and trimming. `HistoryConfig` fields: `MaxMessages` (default 10), `MaxTokens` (0=off), `Semantic` (false), `TrimEmbedder` (nil=use main embedder), `KeepRecent` (3 when Semantic=true). |
| `WithSemanticRecall()` | `false` | Inject semantically relevant messages from other threads into the prompt. Requires `WithEmbedding`. |
| `WithSemanticRecallMinScore(s)` | `0.60` | Cosine similarity threshold for cross-thread recall. |
| `WithRecallKinds(kinds...)` | `[KindFact]` | Which `Kind` values are searched during batched recall. |
| `WithRecallTopK(k)` | `8` | Max items returned by batched recall per turn. |
| `WithWorkingMemory()` | `false` | Enable a single writable markdown slot (a `KindNote` item) at `ScopeResource`. When set, the canonical working-memory note is loaded via a `LoadWorkingMemory` retrieve processor on every turn, so it always appears in context regardless of embedding similarity. |
| `WithWorkingMemoryScope(s)` | `ScopeResource` | Override the scope for the working memory slot. |
| `WithAutoTitle()` | `false` | On the first turn of a thread, ask the LLM to generate a thread title. Requires `WithProvider`. |
| `WithCompaction(c, threshold)` | `nil, 0` | Wire a `Compactor`. Fires when stored history exceeds `threshold × contextWindow`. `threshold` is `0.0–1.0`; recommended `0.80`. Requires `WithStore`. |
| `WithCompress(fn, threshold)` | `nil, 0` | In-memory per-turn compression when the message slice exceeds `threshold` runes. Does not require a `Store`. |
| `WithTools(tools...)` | `nil` | Register agent-callable memory tools (see `AllTools()`). |
| `WithIngestProcessors(ps...)` | `nil` | Append custom processors to the ingest pipeline (runs after defaults). |
| `WithRetrieveProcessors(ps...)` | `nil` | Append custom processors to the retrieve pipeline (runs after defaults). |
| `WithLogger(l)` | `slog.DiscardHandler` | Structured logger for memory-internal events. |
| `WithTracer(t)` | `nil` | OpenTelemetry tracer. Instruments ingest and retrieve spans. |

---

## Retrieve Processors

Retrieve processors implement `RetrieveProcessor` and run in the retrieve pipeline (before each LLM call) to load memory items into context. The framework registers built-in processors automatically; use `WithRetrieveProcessors` to append custom ones.

### `LoadWorkingMemory`

```go
type LoadWorkingMemory struct {
    Scope core.MemoryScopeKind // default: ScopeResource
}
```

A retrieve processor that loads the single canonical working-memory `KindNote` item and adds it to the context block on every turn. It is registered automatically by `WithWorkingMemory()` — you do not construct it directly in typical usage.

Unlike `BatchedRecall` (which ranks items by embedding similarity), `LoadWorkingMemory` does an exact-ID lookup so the scratchpad always appears in context regardless of the current input. The item ID is derived deterministically from `(agentName, scope+chatID)`.

If you need to wire this processor manually (e.g. in a custom agent that builds its own retrieve pipeline via `WithRetrieveProcessors`):

```go
memory.WithRetrieveProcessors(memory.LoadWorkingMemory{Scope: memory.ScopeResource})
```

---

## Agent-Callable Tools

`AgentMemory` exposes four tools the LLM can invoke during a turn:

| Method | Tool name | What it does |
|---|---|---|
| `RememberTool()` | `memory.remember` | Save a new item. Args: `content` (required), `kind`, `scope`, `tags`, `pinned`. |
| `RecallTool()` | `memory.recall` | Semantic search. Args: `query` (required), `kind`, `scope`, `k`. Returns a JSON array. |
| `ForgetTool()` | `memory.forget` | Delete items. Args: `id` OR `match` + optional `kind` + `olderThanSeconds`. |
| `PinTool()` | `memory.pin` | Pin or unpin. Args: `id` (required), `pinned` (bool, required). |

Register them via `memory.WithTools(m.AllTools()...)` in the option chain.

---

## Compaction Types (`compaction` package)

### `compaction.NewStructuredCompactor(provider core.Provider) *StructuredCompactor`

Returns a `core.Compactor` that summarizes a message slice into a structured 9-section summary via one LLM call. Pass `nil` to require that every `CompactRequest` provides its own provider.

`StructuredCompactor` is safe for concurrent use.

### `core.CompactRequest`

| Field | Type | Notes |
|---|---|---|
| `Messages` | `[]ChatMessage` | Messages to summarize. |
| `SummarizerProvider` | `core.Provider` | Overrides the compactor's default provider. `nil` = use default. |
| `FocusHint` | `string` | User directive injected into the prompt (e.g., `"focus on user decisions"`). |
| `IsRecompact` | `bool` | `true` when the input already contains a prior compact. Adjusts prompt tone. |
| `OutputBudget` | `int` | Max tokens for the summary. `0` = 20 000 (default). |
| `ExtraSections` | `[]CompactSection` | Domain-specific sections appended to the standard 9. |

### `core.CompactResult`

| Field | Type | Notes |
|---|---|---|
| `SummaryText` | `string` | Full structured summary (scratchpad stripped). |
| `Sections` | `map[string]string` | Parsed per-section map. Keys are section titles. |
| `SourceTokens` | `int` | Estimated tokens in the input messages. |
| `SummaryTokens` | `int` | Tokens in the output summary. |
| `CompressionRatio` | `float64` | `SummaryTokens / SourceTokens`. |
| `PersistsTable` | `[]string` | What the compactor preserved (transparency for the UI). |
| `LostTable` | `[]string` | What was dropped. |
| `Warnings` | `[]string` | Non-empty when the result is usable but imperfect (e.g., `"partial_sections"`). |

### `compaction.EstimateContextTokens(messages []ChatMessage, model ModelInfo) int`

Approximate token count without a network call. Accurate to ~10–15%. Useful for building your own compaction trigger logic.

### `compaction.StripMediaBlocks(messages []ChatMessage) []ChatMessage`

Removes image/document attachments before a compaction call, replacing them with `[image]` or `[document]` text markers. Does not modify the originals.

### `compaction.CompactableToolNames() []string`

Returns the default whitelist of tool names whose results are safe to summarize away during compaction.

---

## Errors

| Error | Package | When |
|---|---|---|
| `"memory: no store configured"` | `memory` | Any method called without a `Store` |
| `"memory: no embedding configured"` | `memory` | `Recall` called without an `EmbeddingProvider` |
| `core.ErrNotFound` | `core` | `Get` when the item ID does not exist |
| `compaction.ErrEmptyMessages` | `compaction` | `Compact` called with an empty messages slice |
| `compaction.ErrNoProvider` | `compaction` | `Compact` called with no default provider and no `SummarizerProvider` in the request |
| `compaction.ErrSummaryParseFailed` | `compaction` | LLM response did not contain a valid `<summary>...</summary>` block |

All errors from `compaction` are sentinel values; use `errors.Is` for matching.

`AgentMemory` methods return Go `error` for infrastructure failures only (store I/O, network calls). Logic failures (item not found in a `Forget`) are expressed in the return value, not as errors.

---

## Thread-Safety

`AgentMemory` is safe for concurrent use after `Init` returns. `PersistTurn` may fire background goroutines; call `Close` to drain them before the process exits.

`StructuredCompactor` is safe for concurrent use.
