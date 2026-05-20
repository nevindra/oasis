# Perf + DX Fixes Batch — Items A–G

**Status:** Draft for review
**Date:** 2026-05-20
**Author:** nevindra
**Source findings:** core/ + agent/ + network/ review conducted 2026-05-20, surfacing 7 high-impact improvements (Items A–G).

---

## 1. Context

A multi-package review of `core/`, `agent/`, and `network/` identified seven changes that meet the bar of "minimum 3x improvement" along one or more of: user DX, framework speed, memory cost, and code simplification. The seven items split into two natural groups:

- **Architectural (A, B, C, E):** breaking changes to interfaces and protocol types that, once shipped, are very hard to revert. These are the items that unlock the headline memory and simplification wins, and they should land before v1.0.
- **Tactical (D, F, G):** internal fixes that are non-breaking and small in scope, but pay back in measurable throughput or maintenance cost.

This batch ships as one coordinated **v0.(x+1)** release. No deprecation shims. Pre-v1 sharp break, one migration pass for users.

The items are deliberately coupled in the spec because their wins interact:

- Item A (`ToolResult.Content` → `json.RawMessage`) on its own is a modest perf win. Its real value is enabling Item C's zero-copy store path.
- Item C (store-as-default) on its own would require a string ↔ bytes round-trip per Put/Get. Coupled with A, it becomes zero-copy.
- Item B (streaming-only `Provider`) on its own is a simplification, not a speed win. It rides along in the same release so satellite maintainers migrate once.

---

## 2. Goals

1. **10–100x reduction in per-iteration JSON marshal cost** (allocations and bytes) for tool-heavy agent runs, via Items A + C. The messages slice carries envelopes (~500B each) instead of inline payloads (~50KB each). Full content lives once in the store rather than being re-serialized in every iteration's provider call. Marshal cost over a run drops from `O(N² × payload)` to `O(N² × envelope)`.
2. **~50% LOC reduction across provider satellites** via Item B: provider authors implement only `ChatStream`; the non-streaming convenience becomes a free helper in `core`.
3. **Dispatch primitive consolidation** via Item E: `agent/` and `network/` share one `NewStandardDispatch` factory. Any future dispatch feature (metering, tracing, denylist) lands once.
4. **Measurable elimination of slice reallocation** in the agent loop via Item D, configurable trace size via Item F, and removed per-Execute allocations in network via Item G.
5. **One coordinated v0.(x+1) release.** Single CHANGELOG entry with a concrete migration section (before/after code blocks).

---

## 3. Non-goals

- **No transition aliases or deprecation cycle.** Pre-v1 sharp break.
- **No new external dependencies.** Stdlib only.
- **No changes to `ChatMessage.Content`** (stays `string`). Assistant text from providers is already a string; migrating it would be scope creep with no perf gain.
- **No changes to `StreamEvent` shape** beyond what Item B requires (it doesn't).
- **No new in-tree provider satellites or store satellites.** Existing ones (`provider/gemini`, `provider/openaicompat`, `store/sqlite`, `store/postgres`) get updates; nothing new is added.
- **No reshape of the `LoopConfig` field list** beyond adding `maxSteps` (Item F).
- **No new public option types** beyond `WithMaxSteps`. Item C reuses existing `WithToolResultStore`.

---

## 4. Detailed designs

### 4.1 Item A — `ToolResult.Content` becomes `json.RawMessage`

**Type change** (`core/types.go:99-104`):
```go
type ToolResult struct {
    Content     json.RawMessage `json:"content"`           // was: string
    Error       string          `json:"error,omitempty"`
    Attachments []Attachment    `json:"attachments,omitempty"`
}
```

**Erase change** (`core/erase.go:48, 89, 107` — three identical edits):
```go
// before:
return ToolResult{Content: string(body)}, nil
// after:
return ToolResult{Content: body}, nil
```
This drops the `string(body)` copy at each Erase boundary.

**New constructor helpers** added to `core/`:
```go
// JSONContent wraps already-encoded JSON bytes as a ToolResult Content value.
func JSONContent(raw []byte) json.RawMessage { return raw }

// TextContent wraps a plain string as a JSON-quoted RawMessage, suitable
// for use as ToolResult.Content from hand-rolled (non-Erase) tools.
func TextContent(s string) json.RawMessage {
    return json.RawMessage(strconv.Quote(s))
}

// TextResult is a convenience for hand-rolled tools producing plain text.
func TextResult(s string) ToolResult {
    return ToolResult{Content: TextContent(s)}
}
```

**Why `json.RawMessage`, not `[]byte`:** when `json.Marshal` encodes a `ChatMessage` containing a `ToolResult`, `RawMessage` embeds verbatim (no re-encoding). `[]byte` would be base64-encoded by Go's default JSON marshaler. We need the former.

**Honest framing of wins:**

| Where | Win |
|-------|-----|
| `core/erase.go` Execute/ExecuteRaw/ExecuteStream | 1 alloc + 1 copy avoided per tool call |
| `agent/iteration.go` truncation logic | Slicing operates on bytes; no string concat for the truncated payload |
| `core/tool_result_store.go` Put/Get (with Item C) | Bytes flow in/out; eliminates today's `[]rune(entry.content)` allocation in Get |
| Persistence-aware satellites (`store/sqlite`, `store/postgres`) | Store bytes natively without re-encoding |
| Consistency | Aligns with `ToolCall.Args`, `ChatMessage.Metadata`, `ResponseSchema.Schema` which are already `json.RawMessage` |

**Caveat acknowledged:** at the agent loop's message-assembly boundary, we still convert `RawMessage` → string to fit a tool result into `ChatMessage.Content` (which stays `string`). So Item A standalone is one fewer copy per tool call in the typed-tool path, not zero copies through the whole loop. The headline 10–100x memory win comes from Item C riding on A.

**Migration grep target:** any hand-rolled tool constructing `ToolResult{Content: "..."}` with a literal string. Replace with `core.TextResult("...")` or `core.JSONContent(rawBytes)`. The compiler catches every site at build time.

---

### 4.2 Item B — streaming-only `Provider`, `Chat` becomes a free helper

**Interface change** (`core/types.go:41-45`):
```go
// before:
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error)
    Name() string
}

// after:
type Provider interface {
    // ChatStream is the only provider entry point. Implementations MUST close ch
    // before returning. For non-streaming use, callers use core.Chat() helper.
    ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error)
    Name() string
}
```

**New helper** in `core/`:
```go
// Chat is a non-streaming convenience wrapper around Provider.ChatStream.
// It discards stream events and returns the final assembled response.
// For UI-facing streaming, call ChatStream directly.
func Chat(ctx context.Context, p Provider, req ChatRequest) (ChatResponse, error) {
    ch := make(chan StreamEvent, 64)
    done := make(chan struct{})
    go func() {
        defer close(done)
        for range ch { /* discard */ }
    }()
    resp, err := p.ChatStream(ctx, req, ch)
    <-done
    return resp, err
}
```

**Edge case — providers without true streaming:** legacy APIs that only do non-streaming implement `ChatStream` by calling the non-streaming endpoint, sending one synthetic `EventContentDelta` event with the full text, then closing. ~10 lines; behaviorally identical to today's pseudo-Chat fallback.

**What this actually wins:**

| Where | Before | After |
|-------|--------|-------|
| `provider/gemini`, `provider/openaicompat` | implement both `Chat` + `ChatStream` with duplicated request/assembly | implement only `ChatStream`; ~40-50% code reduction per provider |
| Wrappers: `observer/`, `agent/retry.go`, `ratelimit/` | each wraps two methods | each wraps one |
| Channel contract enforcement | "MUST close ch" buried in comment, unenforced | Channel discipline lives in one helper; provider tests can exercise it via `core.Chat` |

**The migration is mechanical but broad.** 13+ non-test callsites exist for `provider.Chat(...)`. Each is a `s/provider.Chat(ctx, req)/core.Chat(ctx, provider, req)/` substitution. `go build ./...` catches any missed site.

Files with `Chat()` callsites that need rewriting:
- `agent/inline_compactor.go:50`
- `agent/loop.go:205`
- `agent/iteration.go:101, 143`
- `agent/retry.go:80` (the retry wrapper's inner call)
- `compaction/structured.go:70`
- `memory/memory_orchestration.go:577, 675`
- `ratelimit/ratelimit.go:66`
- `observer/provider.go:56`
- `rag/retriever.go:526`
- `ingest/contextual.go:77`
- `ingest/graph.go:195`

Plus removal of standalone `Chat` method implementations from:
- `provider/gemini/gemini.go:62`
- `provider/openaicompat/provider.go:84`
- `observer/provider.go` (the wrapper's outer `Chat`)
- `agent/retry.go` (the wrapper's outer `Chat`)
- `ratelimit/ratelimit.go` (the wrapper's outer `Chat`)

**Honest trade-off:** this is a **simplification win**, not a speed win. The streaming-only path was already the hot path for production traffic. We're consolidating the contract and reducing satellite-side code, not making any single request faster. It ships in this batch because:
1. CHANGELOG gets one interface-change migration section, not three across releases.
2. Wrappers (`retry`, `ratelimit`, `observer`) get touched anyway during the batch.
3. Pre-v1 is the only window where this is acceptable.

---

### 4.3 Item C — store-as-default + bytes through the pipeline

**Three coupled sub-changes:**

#### 4.3.1 Auto-wire the in-memory store

`NewInMemoryToolResultStore` already exists (`core/tool_result_store.go:56`). In `agent/agentcore.go` (or wherever `LoopConfig` is finalized), default it:

```go
// In the agent's LoopConfig build:
if cfg.toolResultStore == nil && !cfg.toolResultStoreExplicitlyDisabled {
    cfg.toolResultStore = core.NewInMemoryToolResultStore()
}
```

**Opt-out:** `WithToolResultStore(nil)` explicitly disables and falls back to the legacy "truncation marker only" behavior. Two new internal flag fields on `AgentCore`:
- `toolResultStore core.ToolResultStore` (already exists)
- `toolResultStoreExplicitlyDisabled bool` (new — tracks "user passed nil deliberately")

The implementation of `WithToolResultStore` checks: if `s == nil`, set the disable flag; otherwise set the store.

**`read_full_result` becomes always-registered** (today it's conditional on store presence). Tool-list bloat is one entry per agent; acceptable.

#### 4.3.2 Couple `ToolResultStore` interface to Item A

Today (`core/tool_result_store.go:23-34`):
```go
type ToolResultStore interface {
    Put(ctx context.Context, content string) (id string, err error)
    Get(ctx context.Context, id string, offset, length int) (content string, total int, err error)
}
```

After:
```go
type ToolResultStore interface {
    Put(ctx context.Context, content json.RawMessage) (id string, err error)
    // offset and length are in bytes. Rune-safe alignment is the caller's
    // responsibility (read_full_result handles it for LLM-facing output).
    Get(ctx context.Context, id string, offset, length int) (content json.RawMessage, total int, err error)
}
```

This removes the string ↔ bytes round-trip on every Put/Get and eliminates the `[]rune(entry.content)` allocation in the in-memory implementation's `Get` (`core/tool_result_store.go:118`) — a silent win because that rune slice was allocated *every read*, not once at insert time.

The in-memory store's internal `storeEntry.content` field changes from `string` to `[]byte`. The eviction and TTL logic is byte-length-aware already.

#### 4.3.3 Rune-aware slicing moves to `read_full_result`

`read_full_result.go` does the rune alignment before returning to the LLM:

```go
func (t *readFullResultTool) Execute(ctx context.Context, in ReadFullResultIn) (ReadFullResultOut, error) {
    raw, total, err := t.store.Get(ctx, in.ID, in.Offset, in.Length)
    // ... error handling unchanged
    // raw is json.RawMessage. If the underlying value is a JSON string literal,
    // unquote it so the LLM sees plain text; otherwise return verbatim.
    text := unquoteIfJSONString(raw)
    text = alignToRuneBoundaries(text, in.Offset, in.Length)
    // ... rest unchanged
}
```

**Implementation note:** `unquoteIfJSONString` is a one-byte peek (`raw[0] == '"'`) plus `strconv.Unquote` on the matched case.

**Side effect:** the in-memory store no longer holds rune indexes. If users were relying on `offset`/`length` being rune-counted in the store API directly, that's a behavior change. The semantic from the LLM's perspective is preserved: `read_full_result` still returns rune-aligned text. Document explicitly that the **store interface's offset/length are bytes**, but the **read_full_result tool's offset/length remain runes** (because that's what the LLM understands).

**What changes for users:**

| User type | Impact |
|-----------|--------|
| App author, default config | Memory drops 10–100x on tool-heavy runs. Zero code changes. |
| App author with custom store satellite | Satellite signature changes; bump satellite version together. |
| Tool author using `Erase` | Zero impact. |
| Tool author constructing `ToolResult` by hand | One-line update via `core.TextResult(...)` (Item A). |

**What changes for satellite maintainers:**

`store/sqlite` and `store/postgres` need updates:
- Column type changes from TEXT to BYTEA / BLOB
- `Put`/`Get` signatures take/return `json.RawMessage`
- Migration: new column with renamed table, OR a one-shot column-type ALTER documented in satellite's CHANGELOG

**Risk:**
- Always-on store defaults: 10 MiB cap, 5 min TTL, FIFO eviction. Long-lived bot processes are bounded. Document in PHILOSOPHY.
- `read_full_result` becomes part of every agent's tool surface. If an LLM hallucinates a call with a bad id, it gets a clean "not found or expired" error. Already today's behavior.

---

### 4.4 Item D — message slice pre-allocation factor

**Change** (`agent/loop.go:102`):
```go
// before:
messages = make([]ChatMessage, len(initial), len(initial)+cfg.maxIter*4)

// after:
const preAllocPer = 8     // empirical: 1 assistant + ~5-7 tool result messages per iteration
const preAllocCeil = 2000 // guard against pathologically large maxIter

preAllocCap := cfg.maxIter * preAllocPer
if preAllocCap > preAllocCeil {
    preAllocCap = preAllocCeil
}
messages = make([]ChatMessage, len(initial), len(initial)+preAllocCap)
```

**Rationale for 8:** the existing comment claims "~4 appends per iteration" but real growth is `1 assistant + N tool result messages` where N is typically 3–7 (and can reach 50 via `execute_plan`). Factor 4 hits capacity around iteration 2 and reallocs 6–8 times in a 25-iteration run. Factor 8 covers the common case; pathological iterations still grow naturally.

**Rationale for the ceiling:** users who set very large `maxIter` (research agents, long autonomous runs) would otherwise pre-allocate megabytes upfront for runs that terminate in 10 iterations. 2000 messages × ~150 bytes = ~300KB upper bound on speculative capacity. Acceptable.

**Update the doc comment at `loop.go:95`** to reflect the new factor and the empirical justification.

**Risk:** none. Slice header allocation, no behavior change.

---

### 4.5 Item E — `NewStandardDispatch` factory

**New primitive in `agent/dispatch.go`:**
```go
// AgentRouter is an optional hook between built-ins and standard tool dispatch.
// Returning (result, true) short-circuits dispatch with that result.
// Returning (_, false) falls through to regular tool dispatch.
type AgentRouter func(ctx context.Context, tc ToolCall) (DispatchResult, bool)

type StandardDispatchConfig struct {
    Builtins          func(ctx context.Context, tc ToolCall, dispatch DispatchFunc) (DispatchResult, bool)
    SpawnHandler      func(ctx context.Context, args json.RawMessage, defs []ToolDefinition, exec ToolExecFunc) DispatchResult
    AgentRouter       AgentRouter // optional; network/ supplies this
    ExecuteTool       ToolExecFunc
    ExecuteToolStream ToolExecStreamFunc
    ResolvedToolDefs  []ToolDefinition
    StreamCh          chan<- StreamEvent
}

// NewStandardDispatch builds the recursive DispatchFunc.
// Order: Builtins → spawn_agent → AgentRouter → DispatchTool.
func NewStandardDispatch(cfg StandardDispatchConfig) DispatchFunc {
    var dispatch DispatchFunc
    dispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
        if cfg.Builtins != nil {
            if r, ok := cfg.Builtins(ctx, tc, dispatch); ok {
                return r
            }
        }
        if tc.Name == "spawn_agent" && cfg.SpawnHandler != nil {
            return cfg.SpawnHandler(ctx, tc.Args, cfg.ResolvedToolDefs, cfg.ExecuteTool)
        }
        if cfg.AgentRouter != nil {
            if r, ok := cfg.AgentRouter(ctx, tc); ok {
                return r
            }
        }
        return DispatchTool(ctx, cfg.ExecuteTool, cfg.ExecuteToolStream, tc.Name, tc.Args, cfg.StreamCh)
    }
    return dispatch
}
```

**`LLMAgent.makeDispatch`** (`agent/llm.go:85-97`) collapses to a single call to `NewStandardDispatch` with no `AgentRouter`.

**`Network.makeDispatch`** (`network/network.go:94-117`) collapses to a single call to `NewStandardDispatch` with `AgentRouter` set to the `agent_*` prefix matcher.

**What this wins:**
- **One place** to add cross-cutting dispatch features (metering, tracing, denylist).
- **Network reads as an extension of agent**, not a parallel implementation. Matches PHILOSOPHY: peers compose agent primitives, not duplicate them.
- **Dispatch order documented in code** (Builtins → spawn → router → DispatchTool). Today you grep both files to learn the order.

**What it does not win:**
- Not a 3x speed/memory win. Honestly: a ~2x maintenance simplification.
- LOC delta is small (~10 saved in network/, ~5 added to agent/).

**Risk:**
- No external API change. `LLMAgent` and `Network` keep the same public surface.
- `AgentRouter` is a new exported type in `agent/`. Low surface area; users who don't build peer networks ignore it.
- The big risk is getting dispatch *order* wrong during the refactor. Mitigation: every existing dispatch test in both packages stays as-is and pins the order. Add one new test that exercises the factory directly with all four positions.

---

### 4.6 Item F — `AgentResult.Steps` cap

**Spec contract:**
- `AgentResult.Steps` length is bounded by `maxSteps` (default 100).
- When exceeded, oldest entries are dropped — most-recent-N wins.
- Configurable via `agent.WithMaxSteps(n int) AgentOption`.

**Where it lives:**
- New field `maxSteps int` in `LoopConfig`.
- New option `WithMaxSteps(n)` in the agent options file.
- Implementation in `iteration.go:290` replaces `state.steps = append(state.steps, trace)` with a bounded-append.

**Implementation note** (for the implementation plan, not the spec contract): a naive shift-and-append is O(N) per call. For cap=100 across realistic agent runs (≤25 iterations × ≤5 tool calls = 125 steps), shift cost is acceptable. If benchmarks show otherwise, switch to a ring buffer with head pointer; serialize to chronological order once in `AgentResult` at run-end.

**`StepTrace.Input`/`Output` truncation** is already in place (200/500 chars at collection time per `core/agent.go:104,107`). No change there.

**Resolved sub-question:** `WithMaxSteps(0)` means "unbounded" — 0 is the natural "disable cap" sentinel, matching Go convention for zero-value integer options. Document on the option's godoc.

**Risk:**
- Behavior change visible to anyone who reads `Steps` from `AgentResult`. Long debugging traces are lossy at the head. Document in CHANGELOG and on the `Steps` godoc.
- Tool outputs have a recovery path via `read_full_result`. Agent-level delegations don't have an equivalent retrieval mechanism — they are simply lost from `Steps` when capped. Acceptable: long-run debugging needs the `observer/` traces, not in-memory `Steps`.

---

### 4.7 Item G — network parameter schema cache

**Change** (`network/network.go:188-205`):
```go
// New package-level var:
var agentToolParamSchema = json.RawMessage(
    `{"type":"object","properties":{"task":{"type":"string","description":"The user's original message, copied verbatim. Do not paraphrase, translate, or summarize."}},"required":["task"]}`,
)

// buildToolDefs uses the cached schema and a pre-sized slice:
func (n *Network) buildToolDefs(toolDefs []core.ToolDefinition) []core.ToolDefinition {
    defs := make([]core.ToolDefinition, 0, len(n.sortedAgentNames)+len(toolDefs))
    for _, name := range n.sortedAgentNames {
        defs = append(defs, core.ToolDefinition{
            Name:        "agent_" + name,
            Description: n.agents[name].Description(),
            Parameters:  agentToolParamSchema,
        })
    }
    defs = append(defs, toolDefs...)
    return defs
}
```

**Bonus:** the pre-sized `make` on `defs` eliminates the growth allocations on the slice itself.

**Risk:** none. `json.RawMessage` is safe to share across goroutines as long as it is treated as immutable (it is — no consumer in-tree mutates `ToolDefinition.Parameters`).

---

## 5. Migration impact

### 5.1 Root module

- Version bump from `v0.x.y` to `v0.(x+1).0`.
- `CHANGELOG.md`: new `[0.(x+1).0] - <release date>` section with a `### Migration` subsection containing before/after code blocks for each breaking change (A, B, C). The migration section IS the migration guide — no separate doc.
- New `[Unreleased]` placeholder added at top.

### 5.2 In-tree packages

| Package | Type of change | Notes |
|---------|----------------|-------|
| `core/` | Breaking: `ToolResult.Content`, `Provider`, `ToolResultStore` | Add `Chat`, `JSONContent`, `TextContent`, `TextResult` helpers |
| `agent/` | Internal: dispatch factory, pre-alloc, store auto-wire, `WithMaxSteps` | All callsites of `provider.Chat(...)` rewritten |
| `network/` | Internal: dispatch via factory, schema cache | Uses `agent.NewStandardDispatch` |
| `compaction/`, `memory/`, `rag/`, `ingest/`, `ratelimit/` | Mechanical `provider.Chat(...)` → `core.Chat(...)` rewrites | No semantic change |
| `observer/` | Wrapper now wraps `ChatStream` only | Removes the standalone `Chat` method implementation |

### 5.3 Satellites

| Satellite | Change required |
|-----------|-----------------|
| `provider/gemini` | Delete standalone `Chat` method (gemini.go:62). Keep `ChatStream`. Tag bump. |
| `provider/openaicompat` | Delete standalone `Chat` method (provider.go:84). Keep `ChatStream`. Tag bump. |
| `store/sqlite` | Column type TEXT → BLOB. Signature changes to `json.RawMessage`. Migration documented in satellite CHANGELOG. Tag bump. |
| `store/postgres` | Column type TEXT → BYTEA. Signature changes to `json.RawMessage`. Migration documented in satellite CHANGELOG. Tag bump. |

### 5.4 External users

Anyone with:
- A custom `Provider` implementation: delete their `Chat` method. If they call it explicitly (rare), they switch to `core.Chat`.
- A custom `ToolResultStore` implementation: update Put/Get signatures to `json.RawMessage`.
- A hand-rolled tool constructing `ToolResult{Content: "..."}`: replace with `core.TextResult("...")` or `core.JSONContent(...)`.
- Code reading `AgentResult.Steps`: aware that traces are now capped at 100 by default.

Users who pinned a `v0.x` version remain unaffected until they upgrade.

---

## 6. Testing strategy

**Existing test suites are the primary safety net.** Agent has ~7400 LOC of tests; they must continue to pass with `-race`. The breaking changes will surface as compile errors first; fixing each compile error fixes the test.

**New tests added by item:**

| Item | New test |
|------|----------|
| A | `TestToolResultContentRoundTrip` — verifies `RawMessage` round-trips through JSON wire encoding without re-encoding |
| A | `TestTextResultJSONEncodes` — verifies `core.TextResult("hello")` produces wire `"hello"` |
| B | `TestCoreChatHelperDrainsChannel` — verifies `core.Chat` drains and closes correctly even when provider sends 1000 events |
| B | `TestProviderInterfaceRequiresChatStreamOnly` — compile-time check (verified by the interface declaration itself) |
| C | `TestToolResultStoreDefaultAutoWired` — verifies an agent constructed without `WithToolResultStore` has a non-nil store |
| C | `TestToolResultStoreNilExplicitDisable` — verifies `WithToolResultStore(nil)` results in legacy truncation marker behavior |
| C | `TestReadFullResultRuneAligned` — verifies the tool returns rune-safe content from byte-offset store |
| D | `BenchmarkMessageSlicePreallocReallocs` — verifies allocs/op drops vs the factor-4 baseline |
| E | `TestStandardDispatchOrder` — verifies the four-position order: Builtins → spawn → router → DispatchTool |
| F | `TestAgentResultStepsCapped` — verifies cap=100 default and `WithMaxSteps(n)` override |
| F | `TestAgentResultStepsKeepsRecent` — verifies oldest-drop semantics |
| G | `BenchmarkNetworkBuildToolDefs` — verifies allocs/op drops to constant regardless of agent count |

**Benchmark deltas to capture for CHANGELOG:**
- `BenchmarkRunLoopToolHeavy` (new): a tool-heavy multi-iteration run, measuring `B/op` and `allocs/op` before/after. Items A + C + D combined should produce the headline number.
- `BenchmarkProviderSurfaceLOC` is not a real benchmark — use `tokei` or `cloc` to report LOC delta in the CHANGELOG for Item B.

---

## 7. Risk register

| Risk | Likelihood | Mitigation |
|------|-----------|------------|
| Item B callsite rewrite misses a site | Low | `go build ./...` catches every missed site at compile time |
| Item C breaks an external store satellite | Medium | Satellites have their own `go.mod`; users opt in to upgrades. Bundled tag bump signals the break clearly |
| `read_full_result` rune-alignment edge case (multi-byte UTF-8 split at byte boundary) | Medium | Test with mixed ASCII + CJK content at byte offsets that fall mid-rune |
| Item F oldest-drop loses important debugging context | Low | Document in godoc and CHANGELOG. Users with deep-trace needs use `observer/` |
| Migration confuses external users | Medium | CHANGELOG Migration section with copy-pasteable before/after blocks for each breaking change |
| Item A `string` → `RawMessage` causes downstream JSON marshaling to double-encode | Low | RawMessage is specifically designed to skip re-encoding. Tests verify wire output is identical to today |
| The batch is too large to merge as one PR | Medium | Land the changes in the order in §8: D + G + F first (non-breaking), then A + C as one PR (coupled), then B as one PR, then E as one PR. Master stays green at each step |

---

## 8. Order of operations

The batch is **one user-visible release**, but internally the work lands in a sequence of small PRs that keep master green:

1. **PR 1 (non-breaking):** Items D, G, F.
   - Pre-alloc factor (D), schema cache (G), `WithMaxSteps` option (F).
   - Master builds and passes; no public API changes.
2. **PR 2 (non-breaking):** Item E.
   - Extract `NewStandardDispatch`, switch `LLMAgent` and `Network` to use it.
   - Add new tests pinning dispatch order.
3. **PR 3 (breaking — coordinated):** Items A + C.
   - `ToolResult.Content` → `json.RawMessage`.
   - `ToolResultStore` interface → byte-based.
   - Auto-wire in-memory store.
   - Helpers (`TextResult`, `JSONContent`, `TextContent`) added.
   - In-tree consumers (`agent/iteration.go`, `read_full_result.go`, hand-rolled tools) updated.
   - Satellites `store/sqlite`, `store/postgres` updated and tagged.
4. **PR 4 (breaking):** Item B.
   - `Provider` interface drops `Chat`; `core.Chat` helper added.
   - All `provider.Chat(...)` callsites rewritten.
   - Satellites `provider/gemini`, `provider/openaicompat` updated and tagged.
5. **PR 5 (release):** Tag `v0.(x+1).0`. Promote `[Unreleased]` to dated section. Write `### Migration` section into CHANGELOG with the before/after blocks.

Each PR is independently reverable. The cumulative release is a single migration event for users.

---

## 9. Open questions

- **Should `core.Chat` allocate a `sync.Pool` for the discard channel?** Open. Benchmark first; pool only if `core.Chat` shows up as a hot allocator in real workloads. Out of scope for this batch.
- **Should `core.TextResult` panic on a string containing invalid UTF-8?** No — `strconv.Quote` handles this by emitting `\xff`-style escapes. Document the behavior on the helper's godoc.
