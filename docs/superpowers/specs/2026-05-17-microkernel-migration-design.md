# Microkernel Migration Design

**Status:** Draft for review
**Date:** 2026-05-17
**Author:** nevindra

**Deferred decisions:**
- Final framework name (currently "oasis" as working placeholder — to be decided before Phase 0 starts)

---

## 1. Context

Oasis has grown from "a Go agent framework" into something that no longer matches its own `docs/PHILOSOPHY.md`. The root package is ~29K LOC across ~40 files. Recent absorptions (MCP client, todo tooling, sandbox runtime, browser bindings, skill architecture v2, compaction primitives, deferred tool schemas) have pulled the framework toward "a Go reimplementation of one specific agent harness" rather than a general agent framework.

PHILOSOPHY.md says: *"Consolidate aggressively"*, *"every export is a commitment"*, *"opinionated core, composable edges"*. The current root package contradicts all three: hundreds of accidental exports, edges absorbed into core, no enforced boundary.

This migration restructures Oasis around a **microkernel architecture**: a tiny `core` module that defines the agent execution loop and a small set of contracts, with all other functionality living in independently-versioned satellite modules. The result aligns the code with the philosophy and unlocks per-module evolution, per-module versioning, opt-in dependencies, and a clearer mental model.

The migration is pre-v1.0 work. The framework currently has no external users. This is the cheapest possible moment to do this.

---

## 2. Goals

1. **Easier to maintain** — each module has its own scope, tests, release cycle, contributors.
2. **Easier to refactor** — dependency inversion means changing a satellite cannot break core; pre-v1 module APIs can iterate freely.
3. **Polyglot capability available when needed** — the boundary protocols (MCP for tools, gRPC for retrieval/sandbox in the future) allow non-Go implementations without forcing them.
4. **Framework stays agnostic, app composes** — core knows nothing about specific tools, retrieval strategies, MCP servers, compactors, etc. Apps wire what they want at construction time.

**Nice-to-have:**

5. **Smaller bundles** — apps import only modules they use.
6. **User focus** — each module has its own clear purpose; users learn one thing at a time.

---

## 3. Non-goals

- **Not building a harness layer in this migration.** The "batteries-included CC-shaped bundle" is deferred to Phase 3b. Migration is pure microkernel split.
- **Not defining a polyglot wire protocol now.** Deferred to Phase 3c. MCP already covers tools; sandbox is already a separate process. Other polyglot edges wait for a real driving need.
- **Not supporting external-user migration.** No external users exist. Breaking changes ship freely with CHANGELOG notes only.
- **Not changing what Oasis does** — only how it's organized. Feature work is suspended for the duration.

---

## 4. Cross-cutting design principles

These principles run through every section below.

### 4.1 Microkernel architecture — the one rule

**`oasis/core` imports nothing under `github.com/nevindra/oasis/*`.**

This is enforced by CI:

```bash
test -z "$(go list -deps github.com/nevindra/oasis/core/... \
    | grep nevindra/oasis \
    | grep -v /core)"
```

Dependency direction is strict: satellites import `core`; the app imports satellites and `core`; `core` imports nothing internal. This is dependency inversion at the package level. It is the entire reason the architecture works.

### 4.2 TanStack-style DX

Quality bar that applies to every module:

| Principle | How it lands in Oasis |
|---|---|
| Headless / logic-only core | Microkernel agent loop knows nothing about specific tools/providers/processors |
| Composable primitives | Small focused interfaces, not god-objects |
| Type-driven API | Generics where they prevent misuse (`Tool[In, Out]`); type inference does the work |
| Powerful defaults + infinite override | Every primitive has a "just works" path *and* a "tweak everything" path |
| Stable core, opt-in features | Per-module versioning; new capabilities ship via optional-capability interfaces |
| Documentation as product | Every module has `example_test.go` + a real `doc.go` |
| Build it like a library | Composition at the app layer, never takeover |

### 4.3 Per-module DX checklist

Every extracted module must satisfy:

1. ✅ `example_test.go` with at least one `ExampleNewXxx()` that runs in CI
2. ✅ Functional options pattern (`WithFoo(...)`) — no giant config structs
3. ✅ Generics where they prevent runtime errors
4. ✅ Minimum exported surface (cut convenience exports)
5. ✅ Defaults are runnable (zero-config constructors work)
6. ✅ Typed errors where the caller might branch on failure mode
7. ✅ `doc.go` is a real getting-started document

### 4.4 Execution mode: AI-native

**This spec is executed by a single AI agent on a long-running branch, not by multiple human authors coordinating through PRs.** This changes several structural decisions in the spec.

**What "AI-native execution" means:**
- One continuous branch (`migration/microkernel`); no parallel humans pulling from master
- No PR review cycle between intermediate states
- Tests run at logical checkpoints, not after every commit
- Each "extraction" can be one atomic operation rather than a sequence of staged commits
- Rollback is via git tag, not via reverting individual PRs

**What this collapses in the spec below:**

| Spec construct | Why it exists in human workflows | AI-native treatment |
|---|---|---|
| `LegacyTool` bridge in P0.1 (`Tool` → `LegacyTool` rename + `WrapLegacy` adapter) | Lets master compile while half-migrated | **Skip** — rewrite directly to `AnyTool` in one motion (P0.1 already executing with the bridge — accepted as one-time exception) |
| "Move files to subdirectory, become module later" (P0.2, P0.3, P0.5 → Phase 2) | Two-stage reorganization for reviewable diffs | **Merge** — each piece becomes a Go module *at the moment it's extracted from core* |
| Phase 0 (in-place untangle) → Phase 1 (lock contracts) → Phase 2 (modularize) as separate temporal phases | Reviewable arc with clear handoffs | **Inline** — each module's extraction includes its own untangle + module-creation + contract-stress-test; formal lock candidate ships after all extractions, when contracts have been validated by real implementations |
| Per-extraction "PR title, commits, CI checks" template (§9.2) | Code review process | Adapt as commit-grouping convention; no PR exists |

**What still applies (not human-coordination artifacts):**

- The kernel-discipline CI check (§4.1) — real invariant
- The stress-test-by-implementation rule (§8.4) — design verification, more important for AI not less
- Stop-the-line gates (§12.1) — safety mechanism, applies to AI
- Versioning discipline (§9.6, §11) — protects future users
- The contract freeze concept (§8) — happens, but reframed as "after all extractions stress-test the contracts" rather than as a separate temporal phase

**P0.1 exception:** P0.1 (Tool shape migration) was planned and started executing before this realization. It retains the `LegacyTool` bridge pattern. The bridge is removed as a natural consequence of the next extraction (`oasis/mcp`), since `mcp_tool_wrapper.go` migrates to `AnyTool` directly there. Wasted work is bounded.

**Concrete implication for the rest of this spec:** sections §7 (Phase 0) and §9 (Phase 2) describe the *logical* work that must happen, not the temporal phasing. The AI-native execution sequences them as:

```
P0.1 (in-flight, bridge exception) →
  per module {untangle in core + extract as Go module + stress-test contracts} →
  formalize and lock kernel contracts (P1 equivalent) →
  tag v1.0 candidates
```

See updated timeline in §15.

### 4.5 Positioning statement

> **Oasis = kernel + curated batteries (composable, opt-in) + optional harness layer (opinionated bundles).**

This positioning differentiates Oasis from two failure modes:

- **"Tiny kernel + bring your own everything"** — too austere; every user reinvents skill loading, rate limiting, retrieval, etc. Discoverable, but unusable as a starter.
- **"Big monolithic framework"** — what Oasis currently is; everything in core, hundreds of accidental exports, can't evolve modules independently.

**Curated batteries** means: every common need in production agent systems has a maintained satellite module under `oasis/`. Users compose what they want. The kernel never assumes a battery is present. This is the TanStack pattern — small core, official sub-packages for common needs (`@tanstack/query-persist-client`, `@tanstack/query-broadcast`, etc.).

**Test for "is X a battery?"** If a non-trivial percentage of production agent apps need a capability, it gets a curated module — even if the kernel could theoretically work without it. Skill loading, MCP integration, rate limiting, RAG, guardrails, compaction — all batteries. The kernel doesn't need them; users almost always do.

**Test for "is X harness?"** If a capability only makes sense as part of a specific *style* of agent (e.g. Claude-Code-style self-tracking with `todo_write`), it lives in `oasis/harness/<style>` (Phase 3b). Most users won't import it. Those who do get an opinionated bundle.

This statement is load-bearing for the extraction decisions in §9.1 — particularly D16 (skills as battery) and D17 (HITL as own module).

---

## 5. Target architecture

### 5.1 Final repo layout

```
oasis/                          (monorepo, multiple Go modules)
├── core/                       ← THE KERNEL — own go.mod
│   ├── types.go               (protocol types: ChatRequest, ToolCall, etc.)
│   ├── agent.go               (Agent interface)
│   ├── llmagent.go            (LLMAgent base implementation)
│   ├── loop.go                (execution loop)
│   ├── suspend.go             (suspend/resume)
│   ├── handle.go              (Spawn, AgentHandle)
│   ├── processor.go           (Processor interface + chain)
│   ├── stream.go              (StreamEvent — emitted by loop)
│   ├── batch.go               (batch primitives, loop-adjacent)
│   └── doc.go
│
├── provider/{gemini, openaicompat}/   ← already split, becomes own modules
├── store/{sqlite, postgres}/           ← already split, becomes own modules
├── memory/                              ← already split (helpers; will absorb agentMemory from root)
├── observer/                            ← already split (OTEL)
├── ingest/                              ← already split (chunking)
├── sandbox/                             ← already split (ix bindings)
├── skills/                              ← already split
├── tools/{knowledge, remember, ...}/   ← already split (each its own module)
│
├── rag/           ← NEW MODULE — was retriever.go + cosine.go in root
├── compaction/    ← NEW MODULE — was compaction_*.go (5 files)
├── workflow/      ← NEW MODULE — was workflow*.go (4 files)
├── network/       ← NEW MODULE — was network.go
├── mcp/           ← NEW MODULE — was mcp_*.go (10 files)
├── guardrail/     ← NEW MODULE — was guardrail.go
├── ratelimit/     ← NEW MODULE — was ratelimit.go (✅ extracted as ratelimit/v0.1.0)
├── skills/        ← NEW MODULE — was skill*.go (5 files in root) + existing asset dirs (per D16)
├── input/         ← NEW MODULE — HITL: extracts InputHandler interface + ask_user tool from core (per D17)
│
└── (cmd/bot_example deleted in P0.1)
```

`catalog.go` stays in core as vocabulary/protocol types (see D15). It's pure data types (`Protocol`, `Platform`, `ModelInfo`, `ModelCapabilities`, `ModelPricing`, `ModelStatus`) plus a parser helper — shared across satellites (`provider/*`, `observer/`) as protocol-level vocabulary, analogous to how `ChatRequest`/`ChatResponse` live in core.

### 5.2 Module count

~19 Go modules total. **Nine** new modules created from current root (catalog stays in core; skill + HITL added as batteries per D16 and D17). The rest already exist as subpackages and graduate to their own `go.mod`.

### 5.3 Dependency direction

```
        cmd/bot_example
              │ imports
              ▼
   [rag] [mcp] [workflow] [...]  [provider/gemini] [store/sqlite] ...
              │ all import only
              ▼
        oasis/core
              │
              ▼
       (nothing under oasis/*)
```

### 5.4 Judgment calls (locked)

- `stream.go` stays in core (load-bearing for the loop's public API)
- `batch.go` stays in core (78 lines, loop-adjacent)
- `tracer.go` moves to `observer/`
- `cosine.go` moves with `rag/`

---

## 6. Kernel contract surface

The entire locked v1.0 surface of `oasis/core`. Each item below is a permanent commitment.

### 6.1 Design principles

1. **Minimum viable contracts.** Methods that are convenient but not load-bearing get cut.
2. **Generics where they prevent misuse.** Type-erased fallbacks (`AnyTool`) for the loop's heterogeneous machinery.
3. **Capability via separate interface, never extension.** New abilities = new interface + type assertion (the `io.ReaderAt` pattern).
4. **Protocol types are data, no methods.** Plain structs with optional fields; zero values preserve behavior.
5. **No `interface{}` / `any` at boundaries** unless type-erasure is the explicit point.

### 6.2 Litmus test for kernel inclusion

> "Does the agent execution loop literally call this in its main path?"

Pass → kernel. Fail → satellite module.

Application of this test (resolved during brainstorming):

- `Provider` ✅ — loop calls `provider.Chat(...)`
- `Tool` / `AnyTool` ✅ — loop dispatches tool calls
- `Processor` ✅ — loop runs pipeline pre/post LLM
- `Agent` ✅ — meta-contract for what the loop runs
- `Store` ❌ — loop produces messages; caller persists. **Lives in `oasis/store`.**
- `Memory` ❌ — recall + inject is exactly what a Processor does. **Lives in `oasis/memory`.**

### 6.3 Tier A — Core interfaces (locked at v1.0)

```go
// Anything that can run a task. Workflows, networks, LLM agents — all satisfy this.
type Agent interface {
    Execute(ctx context.Context, task Task) (Result, error)
}

// Talks to an LLM. Synchronous; streaming is a separate capability.
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// Type-safe tool. User-facing API uses generics.
// Atomic: one Tool = one operation (NOT a bundle).
type Tool[In, Out any] interface {
    Name() string
    Schema() ToolSchema
    Execute(ctx context.Context, in In) (Out, error)
}

// Type-erased tool. The loop iterates these.
type AnyTool interface {
    Name() string
    Schema() ToolSchema
    ExecuteRaw(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// Transforms messages in the pipeline (compaction, guardrails, memory recall, etc.)
type Processor interface {
    Process(ctx context.Context, msgs []Message) ([]Message, error)
}
```

**Four interfaces (five if counting Tool's two forms).** That is the entire kernel contract.

### 6.4 Tier B — Optional capability interfaces

Additive forever. Each locks individually once shipped.

```go
type StreamingProvider interface {
    Provider
    ChatStream(ctx context.Context, req ChatRequest, out chan<- StreamEvent) error
}

type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type MultimodalTool interface {
    AnyTool
    ExecuteMultimodal(ctx context.Context, args json.RawMessage) (ToolOutput, error)
}

type ContextAwareTool interface {
    AnyTool
    SetContext(ToolContext)
}
```

### 6.5 Tier C — Protocol types (data, no methods)

```go
type Message struct {
    Role       MessageRole
    Content    Content
    ToolCalls  []ToolCall
    ToolCallID string
}

type MessageRole int           // System, User, Assistant, Tool

type Content struct {
    Text  string
    Parts []ContentPart         // multimodal
}

type ChatRequest struct {
    Messages []Message
    Tools    []ToolSchema
    Options  ChatOptions
}

type ChatResponse struct {
    Message      Message
    FinishReason FinishReason
    Usage        Usage
}

type ToolCall struct {
    ID        string
    Name      string
    Arguments json.RawMessage
}

type ToolResult struct {
    ID     string
    Output json.RawMessage
    Error  string              // business error, not Go error
}

type ToolSchema struct {
    Name        string
    Description string
    InputSchema json.RawMessage // JSON Schema
}

type Task struct {
    Input   string
    Options TaskOptions
}

type Result struct {
    Output   string
    Messages []Message
    Usage    Usage
}

type Usage struct {
    InputTokens  int
    OutputTokens int
    // extensible via optional fields
}

// Sealed via unexported marker method
type StreamEvent interface{ streamEvent() }
type TextDelta struct{ Delta string }
type ToolCallStart struct{ Call ToolCall }
type ToolCallEnd struct{ Result ToolResult }
type Done struct{ Result Result }

// Suspend/resume primitive — opaque to core
type Suspended struct {
    AgentID string
    State   json.RawMessage     // caller persists
    Reason  SuspendReason
}
```

### 6.6 Tier D — Concrete types

```go
type LLMAgent struct{ /* unexported fields */ }
type AgentHandle struct{ /* unexported fields */ }
type ProcessorChain []Processor
```

### 6.7 Tier E — Constructors

```go
func NewLLMAgent(p Provider, opts ...Option) *LLMAgent
func Spawn(ctx context.Context, a Agent, t Task) (AgentHandle, error)
```

### 6.8 Tier F — Functional options (v1.0 locked set)

```go
type Option func(*agentConfig)

func WithTools(tools ...AnyTool) Option
func WithProcessors(p ...Processor) Option
func WithSystemPrompt(prompt string) Option
func WithModel(model string) Option
func WithMaxIterations(n int) Option
func WithLogger(l *slog.Logger) Option
func WithObserver(t observer.Tracer) Option
```

Adding new `WithXxx(...)` options is always non-breaking. This is the primary mechanism for adding configurability post-v1.0.

### 6.9 What's NOT in core (lives in satellite modules)

| Concept | Module | Why not core |
|---|---|---|
| `Store` | `oasis/store` | Loop doesn't persist; caller does |
| `MemoryProvider`, `agentMemory`, fact extraction | `oasis/memory` | Memory recall is just a Processor |
| `Retriever`, `Reranker`, `HybridRetriever` | `oasis/rag` | Complete subsystem; only some users need it |
| `CompactionStrategy` | `oasis/compaction` | Multiple competing strategies; satisfies Processor |
| `Guardrail` | `oasis/guardrail` | A *kind of* Processor |
| `RateLimiter` | `oasis/ratelimit` | Wraps Provider — composition |
| `Workflow` types | `oasis/workflow` | Workflow satisfies Agent |
| `Network` types | `oasis/network` | Network satisfies Agent |
| `MCPClient`, `MCPRegistry`, etc. | `oasis/mcp` | MCP tools become `AnyTool` from core's view |
| `SkillProvider`, `Skill`, skill loading | `oasis/skills` | Loadable agent instructions — a battery, not kernel (per D16). Owns its asset directory. |
| `InputHandler`, `ask_user` tool | `oasis/input` | HITL is just a Tool; no need for special kernel auto-wiring (per D17) |

(`catalog.go` types stay in core — see D15. Pure vocabulary types shared across satellites; analogous to how `ChatRequest`/`ChatResponse` live in core.)

### 6.10 Total locked surface

| Tier | Count |
|---|---|
| Interfaces (A + B) | ~8 |
| Protocol types (C) | ~12 + catalog vocabulary (`Protocol`, `Platform`, `ModelInfo`, `ModelCapabilities`, `ModelPricing`, `ModelStatus`) |
| Concrete types (D) | ~3 |
| Constructors (E) | ~2 |
| Options at launch (F) | ~7 |

**~38 designed exports**, down from hundreds of accidental ones in current root.

---

## 7. Phase 0 — Decoupling

**Goal:** Untangle in-place so modules can be extracted cleanly. No new modules created in this phase; all work happens in the current root.

**Duration:** ~4 weeks (human-coordinated estimate).

> **AI-native note (per §4.4):** Only P0.1 runs as described here (it's in-flight with the `LegacyTool` bridge exception). P0.2–P0.5 logically describe untangling work that, under AI-native execution, merges with the corresponding Phase 2 extraction into a single atomic operation per module. The "move files to subdirectory but still in root module" step is skipped — files move directly into a new Go module. Read this section as a description of what *logically* happens during each module's extraction, not as a separate temporal phase.

### 7.1 Inventory of coupling found

| Coupling | Severity | Location |
|---|---|---|
| **MCP → core** | 🔴 Heavy | `agent.go` (25 refs, 5 options, 3 config fields), `agentcore.go` (18 refs, MCPRegistry init), `llmagent.go` (5 refs, `MCP()` accessor), `types.go` (3 refs, concept) |
| **Compaction → core** | 🟡 Medium | `agent.go` (`Compactor` field + `WithCompaction` option), `compaction.go` defines interface |
| **Workflow types in `types.go`** | 🟡 Medium | `types.go:685-748` — `WorkflowDefinition`, `NodeType`, `NodeTemplate`, `NodeDefinition`, `DefinitionRegistry` |
| **Memory orchestration in core** | 🔴 Heavy | `memory.go` (782 lines) — `agentMemory`, fact extraction, embeddings, prompt building, persistence |
| **Tool shape** | 🟡 Design | `types.go:96-99` — current `Tool` is a *bundle* |
| **HITL auto-wiring** | 🟡 Medium | `agent.go:742` defines `InputHandler` interface; loop auto-generates `ask_user` tool when InputHandler is set — auto-magic that violates "composition at app layer" (per D17) |
| **Skill loading in root** | 🟡 Medium | `skill.go`, `skill_builtin.go`, `skill_scan.go`, `skill_tool.go` (~25K Go LOC) sit in root. Spec earlier called `skills/` "already split" — that was wrong (only assets live there, no Go code) |

### 7.2 P0.1 — Tool shape decision (gates all other Phase 0 work)

**Decision: Path B (atomic tools with generics).**

Current shape (Path A — rejected):
```go
type Tool interface {
    Definitions() []ToolDefinition
    Execute(ctx, name string, args json.RawMessage) (ToolResult, error)
}
```

New shape (Path B — adopted):
```go
type Tool[In, Out any] interface {
    Name() string
    Schema() ToolSchema
    Execute(ctx context.Context, in In) (Out, error)
}

type AnyTool interface {
    Name() string
    Schema() ToolSchema
    ExecuteRaw(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}
```

Work:
1. Add new `Tool[In, Out]` + `AnyTool` interfaces alongside existing `Tool` (which gets renamed to `legacyTool` temporarily)
2. Implement adapter: `Erase[In, Out](t Tool[In, Out]) AnyTool`
3. Migrate every existing tool in `tools/*` to atomic shape
4. Where one old tool implemented N operations, split into N atomic tools
5. Update loop to consume `AnyTool` only
6. Remove `legacyTool`

**Effort:** ~1 week. Touches every tool package. Pays back forever in DX.

### 7.3 P0.2 — Decouple MCP from core

After P0.1 is done.

1. Move `mcp_*.go` files into `oasis/mcp/` directory (still in root module — extraction happens in Phase 2)
2. Remove `WithMCPServer`, `WithMCPServers`, `WithSharedMCPRegistry`, `WithMCPLifecycleHandler`, `WithDeferredSchemas` options from `agent.go`
3. Remove `mcpStartupConfigs`, `sharedMCPRegistry`, `mcpLifecycleHandler` fields from `agentConfig`
4. Remove `mcpRegistry` field and init from `agentcore.go`
5. Remove `LLMAgent.MCP()` accessor from `llmagent.go`
6. Design new wiring pattern in `oasis/mcp`:
   ```go
   reg := mcp.NewRegistry()
   reg.Register(ctx, mcpConfig)
   tools := reg.Tools()       // returns []core.AnyTool
   agent := core.NewLLMAgent(p, core.WithTools(tools...))
   ```
7. Move deferred-schema support to `mcp.WithDeferredSchemas()` registry option
8. Update `bot_example` to use new pattern; verify ergonomics
9. CI check passes: `grep -r "MCP\|mcp" core/*.go` returns only legitimate references

**Effort:** ~3-4 days. Biggest piece.

**Critical:** if new ergonomics feel worse than old, fix in `oasis/mcp`'s API, not by re-coupling core.

### 7.4 P0.3 — Decouple Compaction

1. Kill the `Compactor` interface — compaction is a `Processor`
2. Remove `compactor`, `compactThreshold` fields from `agentConfig`
3. Remove `WithCompaction()` option
4. Move `compaction_*.go` to `oasis/compaction/` directory
5. Refactor existing compaction implementations to satisfy `core.Processor`
6. Update `bot_example`: `WithProcessors(compaction.SlidingWindow(threshold, summarizer))`

**Effort:** ~1-2 days.

### 7.5 P0.4 — Evict workflow types from `types.go`

1. Move `WorkflowDefinition`, `NodeType`, `NodeTemplate`, `NodeDefinition`, `DefinitionRegistry` out of `types.go`
2. Land them in `workflow/types.go`
3. Update workflow code in root to reference moved types

**Effort:** ~0.5 day. Mechanical.

### 7.6 P0.5 — Move memory orchestration out

1. Move `agentMemory`, `ExtractedFact`, `MemoryStore`, fact extraction, embeddings, prompt building, persistence → `oasis/memory/`
2. Kernel keeps **zero** memory code (memory becomes purely a Processor)
3. New pattern: `memory.Recall(provider) core.Processor`
4. Refactor `bot_example` to wire memory explicitly

**Effort:** ~3-4 days. Significant — memory.go is 782 lines with many cross-cutting concerns.

### 7.7 P0.6 — Final coupling audit

Run after P0.1–P0.5:

```bash
grep -E "MCP|Compactor|Workflow|agentMemory|Retriever|Guardrail|RateLimit" core/*.go
```

Should return zero (or only legitimate `Processor` references). Investigate and fix anything that surfaces.

**Effort:** ~1-2 days.

### 7.8 Phase 0 acceptance criteria

1. ✅ Tool shape migrated to Path B
2. ✅ Core files contain zero references to MCP, Compactor, Workflow types, Memory orchestration
3. ✅ `bot_example` builds and runs end-to-end with new wiring patterns
4. ✅ All existing tests pass
5. ✅ Ships as `v0.16.0` with one consolidated migration note

### 7.9 Phase 0 timeline

| Week | Work |
|---|---|
| 1 | P0.1 Tool decision + implementation; start P0.2 MCP |
| 2 | Finish P0.2; P0.3 Compaction; P0.4 Workflow types |
| 3 | P0.5 Memory orchestration |
| 4 | P0.6 Audit, bot_example polish, ship v0.16.0 |

---

## 8. Phase 1 — Contract freeze

**Goal:** Design and lock the kernel's contract surface. Ship as `v0.17.0` — the "v1.0 lock candidate."

**Duration:** ~3 weeks (external review skipped per decision register §14).

### 8.1 P1.1 — Per-interface design review

For each of the ~8 kernel interfaces (Tiers A + B from §6):

- Methods: minimum viable set
- Error semantics
- Invariants (concurrency, idempotency, ordering)
- Naming
- DX checklist (§4.3)

Output: ADR-style note per interface in `docs/concepts/contracts/<name>.md`.

**Effort:** ~1 week.

### 8.2 P1.2 — Protocol type review

For each of the ~12 protocol types (Tier C from §6):

- Field-by-field review
- Optional vs required
- Forward compatibility (zero-value behavior)
- JSON tags (these go on the wire — hard to change later)
- Doc comments

Output: locked `core/types.go` with finalized field sets.

**Effort:** ~3-4 days.

### 8.3 P1.3 — Functional options surface

Define `Option` mechanism + initial set of `WithXxx` options (Tier F from §6).

- Naming consistency
- One option = one concern
- Doc comment example per option

Output: locked option set in `docs/concepts/contracts/options.md`.

**Effort:** ~2-3 days.

### 8.4 P1.4 — Stress-test by implementation (critical step)

Build minimal-but-real implementations of every contract to validate the design. Skipping this is how the wrong API gets locked.

| Interface | Stress test |
|---|---|
| `Provider` | Toy provider (canned responses) — verify `LLMAgent` works against it |
| `StreamingProvider` | Toy provider that streams |
| `Tool[In, Out]` | 3 tools: no-input/string-out, string-in/struct-out, struct-in/struct-out |
| `AnyTool` | Manual implementation without generic Tool |
| `Processor` | Uppercase transform, block-bad-words drop, memory-recall stub inject |
| `Agent` (custom) | Rule-based router that satisfies Agent (proves non-LLM agents work) |
| Workflow shape | Minimal workflow wrapping 2 LLMAgents — proves Workflow satisfies Agent |
| Network shape | Minimal network — proves Network satisfies Agent |

**Rule:** if any implementation feels awkward or requires reaching past the public surface, the contract is wrong. Fix before locking.

Each test lives in `core/contract_*_test.go` and runs in CI forever.

**Effort:** ~1 week.

### 8.5 P1.5 — Spec doc + doc comments

- Every exported symbol in `core` has a doc comment usable by an LLM coding assistant
- Every interface doc comment includes contract section: invariants, concurrency, error semantics
- `core/doc.go` is a real getting-started doc
- `docs/concepts/kernel-contracts.md` consolidates for human readers

**Effort:** ~3-4 days.

### 8.6 P1.6 — External review window

**SKIPPED.** Decision in §14. Substitute: use the contracts to build one new agent app shape (research agent or codegen agent) before locking. If contracts hold across two different shapes, they're probably OK.

### 8.7 P1.7 — Ship v0.17.0

Tag, push, update CHANGELOG. Release notes explicitly state:

> "This is the v1.0 lock candidate for `oasis/core`. The interfaces, protocol types, options, and constructors in this release are intended to be permanent. Please report any usability issues."

Stay in v0.x until it feels solid in real use.

### 8.8 Phase 1 acceptance criteria

1. ✅ Every interface has ADR note + locked doc comment
2. ✅ Every interface has at least one passing stress-test implementation
3. ✅ At least one "different shape" agent app built (not just bot_example)
4. ✅ `v0.17.0` tagged with "v1 lock candidate" framing in CHANGELOG
5. ✅ `core/doc.go` is a real getting-started doc

### 8.9 Phase 1 timeline

| Week | Work |
|---|---|
| 1 | P1.1 Interface design review |
| 2 | P1.2 Protocol types + P1.3 Options |
| 3 | P1.4 Stress test + P1.5 Docs + P1.7 Ship |

---

## 9. Phase 2 — Extraction

**Goal:** Each satellite module becomes its own Go module with its own `go.mod`.

**Duration:** ~14 weeks (human-coordinated estimate).

> **AI-native note (per §4.4):** Under AI-native execution, this phase is the unit of work, not a phase that follows Phase 0. Each module extraction is one atomic operation that includes: untangling from core (Phase 0-style work), creating the new `go.mod` (Phase 2-style work), updating all importers, and stress-testing the kernel contracts with the new module (Phase 1-style work). Per-PR templates and per-PR CHANGELOG entries below are read as per-extraction-commit-cluster conventions, since no PRs exist on the long-running branch.

### 9.1 Extraction order

| # | Module | LOC | Why this position |
|---|---|---|---|
| 1 | `ratelimit` | ~150 | Smallest possible test of the extraction pattern (✅ plan written) |
| 2 | `guardrail` | ~600 | First Processor extraction |
| 3 | `compaction` | ~750 | Second Processor extraction |
| 4 | `network` | ~300 | First Agent extraction |
| 5 | `input` | ~100 (mostly removing core auto-wiring) | HITL extraction — un-couples InputHandler + ask_user from kernel (per D17) |
| 6 | `skills` | ~25K Go code + asset dirs | Skill loading battery (per D16); moves `skill*.go` from root into module, absorbs existing `skills/` asset dirs |
| 7 | `mcp` | ~3000 | Heaviest agent.go surgery |
| 8 | `workflow` | ~2500 | Larger Agent implementation |
| 9 | `rag` | ~6000+ | Largest module; extract when process is routine |

**9 modules total.** Catalog stays in core (D15); skill + input added as batteries (D16, D17).

**Stop-the-line rule:** if extraction #1 reveals a pattern problem, fix the pattern before #2.

### 9.2 Per-PR shape (template)

**Title:** `feat!: extract oasis/<module> as separate Go module`

**Commits within the PR:**

1. Add `<module>/go.mod` + `<module>/go.sum`
   - `go mod init github.com/nevindra/oasis/<module>`
   - `go mod tidy`
   - Add local-dev replace directive
2. Move package declarations + imports
   - Update intra-module imports to use `core.` prefix for kernel types
3. Update all importers (bot_example, other modules, tests)
4. CI checks pass
5. CHANGELOG entry + migration note

### 9.3 CI guards (cumulative, tighten with every extraction)

```bash
# 1. Kernel discipline: core imports NOTHING under oasis/*
test -z "$(go list -deps github.com/nevindra/oasis/core/... \
    | grep nevindra/oasis | grep -v /core)"

# 2. Module independence: this module imports only what it declared
test -z "$(go list -deps github.com/nevindra/oasis/<module>/... \
    | grep nevindra/oasis | grep -v -E '(/core|/<module>)')"

# 3. All modules build
for mod in $(find . -name go.mod | xargs dirname); do
    (cd "$mod" && go build ./... && go test ./...) || exit 1
done
```

### 9.4 CHANGELOG entry template

```markdown
## [0.X.0] - YYYY-MM-DD

### Changed
- **BREAKING**: `oasis/<module>` is now a separate Go module.
  - Import path: `github.com/nevindra/oasis/<module>`
  - Migration:
    ```go
    // Before
    import "github.com/nevindra/oasis"
    agent := oasis.WithFoo(c, t)

    // After
    import (
        "github.com/nevindra/oasis/core"
        "github.com/nevindra/oasis/<module>"
    )
    agent := core.WithProcessors(<module>.NewFoo(...))
    ```
```

### 9.5 Release strategy per extraction

Each extraction ships **two tags:**

- Root: `v0.18.0`, `v0.19.0`, … (bot_example + CHANGELOG)
- New module: `<module>/v0.1.0` (its first version)

### 9.6 Versioning across the marathon

```
oasis/core              v0.17.0  →  stays v0.17.x throughout Phase 2
                        v1.0.0   →  tagged ONLY after all extractions done AND 1+ month in real use

oasis/ratelimit         <module>/v0.1.0  →  PR #1
oasis/catalog           <module>/v0.1.0  →  PR #2
oasis/guardrail         <module>/v0.1.0  →  PR #3
oasis/compaction        <module>/v0.1.0  →  PR #4
oasis/network           <module>/v0.1.0  →  PR #5
oasis/mcp               <module>/v0.1.0  →  PR #6
oasis/workflow          <module>/v0.1.0  →  PR #7
oasis/rag               <module>/v0.1.0  →  PR #8

root                    v0.25.0  →  approximate state at Phase 2 end
```

**Rule:** `oasis/core` does NOT get version bumps during extractions. If a contract problem surfaces mid-Phase-2, pause extractions, fix core, ship new core, resume. Don't smuggle silent core changes into extraction PRs.

### 9.7 Phase 2 timeline

| Week | Extraction |
|---|---|
| 8 | ratelimit (pattern validation) |
| 9 | catalog |
| 10 | guardrail |
| 11 | compaction |
| 12 | network |
| 13-14 | mcp (2 weeks) |
| 15-16 | workflow (2 weeks) |
| 17-21 | rag (5 weeks) |

### 9.8 Tooling

- `go.work` workspace file at repo root for local development (avoids fighting `replace` directives)
- Single CI run covers all modules via the cumulative script
- Single PR can change multiple modules atomically when needed

### 9.9 Critical risks

1. **Mid-marathon contract discovery.** Stop-the-line authority must be used if the `Agent` or `Processor` contract reveals a flaw during extraction. Don't ship through it.
2. **No end-to-end agent app for validation.** `cmd/bot_example` was deleted in P0.1. Per-extraction validation relies on the kernel contract stress tests (§8.4) plus each module's `example_test.go`. If gaps appear, add a minimal end-to-end test fixture inside the relevant module rather than reviving a reference app.
3. **`rag` being too big.** 6000+ LOC may have internal coupling that needs untangling. If so, treat as mini-untangle inside its own extraction.

### 9.10 Phase 2 acceptance criteria

1. ✅ All 8 modules have their own `go.mod`
2. ✅ CI-enforced: `oasis/core` imports nothing under `oasis/*`
3. ✅ CI-enforced: each module imports only declared deps
4. ✅ Each module's `example_test.go` runs and passes
5. ✅ All tests pass across all modules
6. ✅ CHANGELOG documents every extraction with migration notes

---

## 10. Phase 3+ (deferred, not in this migration's scope)

Named explicitly so they don't get lost:

| Phase | What |
|---|---|
| **3a — Docs pass** | Per-module recipe books; rewrite `docs/concepts/` to match new structure; user migration guide |
| **3b — Harness layer** | If batteries-included ergonomics are desired later, build `oasis/harness` as opinionated bundle. Low-risk addition because kernel is stable. |
| **3c — Polyglot escape hatch** | For edges where Go is the wrong choice (likely embeddings/reranking): define gRPC contract, ship Python sidecar option for `oasis/rag`. Driven by real need, not speculation. |
| **3d — Maintenance mode** | Modules promote to v1.0 individually as they stabilize. New capabilities arrive via optional-capability interfaces. Focus shifts from restructure to features. |

---

## 11. Per-module versioning rules (post-Phase 2)

| Module | Rules |
|---|---|
| `oasis/core` | v1.0 once stress-tested in 2+ app shapes for ≥1 month. After v1.0: new interfaces ADDED only (optional-capability pattern). Breaking changes require `core/v2`. |
| Satellite modules | Each promotes to v1.0 independently when stable. No schedule. Some may stay v0.x indefinitely if inherently experimental. |
| Root module | Continues as home of CHANGELOG and top-level docs. Eventually nobody imports it directly (`cmd/bot_example` deleted in P0.1). |

### 11.1 Module v1.0 promotion criteria

All must be true:

1. ✅ Used in 2+ different apps for non-trivial work
2. ✅ DX checklist passes (§4.3)
3. ✅ No pending design changes
4. ✅ User can write working code on first try using only `go doc` output

### 11.2 Backwards-compat policy post-v1.0

- **Allowed:** add new interfaces, add new options, add fields with safe zero values, grow methods on concrete types
- **Forbidden:** modify interface signatures, remove options, repurpose fields, rename exports
- **Deprecation cycle:** announce → leave deprecated for 2 minor releases → remove in next major
- **Breaking changes after v1.0:** require `/v2` module path

---

## 12. Risk management

### 12.1 Checkpoint gates

| Checkpoint | Pass criteria | Failure action |
|---|---|---|
| Pre-Phase 0 | Spec reviewed; time commitment realistic; name decided | Pause or refine |
| End of P0.1 | Tool shape migration complete; existing tests green; new contracts in place | Fix ergonomics in satellite, **not** by re-coupling core |
| Per-extraction | CI dependency check green; module's `example_test.go` passes; existing tests still pass | Don't proceed until green |
| Contract freeze (post-extractions) | Stress tests pass; ≥2 different agent shapes implemented against contracts | Redesign offending contracts before tagging v1.0 candidate |
| Mid-extraction (if contract problem surfaces) | — | **Stop the line.** Pause, fix core, retag, resume |

### 12.2 Rollback / abort criteria

- **Phase 0:** if decoupling makes ergonomics worse AND we can't find better ergonomics in the satellite → consider keeping that piece in core as a deliberate exception.
- **Phase 1:** if stress tests reveal we can't implement basic shapes → redesign (not abort).
- **Phase 2:** if 3 consecutive extractions reveal coupling we missed → pause, audit, possibly return to Phase 0.
- **True abort:** if at any point the old architecture serves you better than the partial new one, revert to pre-Phase-0 tag and walk away. Sunk cost is not a reason to keep going.

---

## 13. Success metrics

### 13.1 Quantitative

- Root `oasis` LOC: 29K → ~5K (in `core/`)
- Accidental exports: hundreds → ~32 designed exports
- `go test ./core/...` runtime: fast enough to run on every edit
- Time to add a new Provider/Tool/Processor: measure now, measure after

### 13.2 Qualitative

- "Can I explain what each module does in one sentence?" — yes
- "Can a new contributor (human or AI) understand a single module without understanding all of Oasis?" — yes
- "Can I refactor `oasis/rag` without fear of breaking unrelated code?" — yes
- "Does PHILOSOPHY.md actually describe the code now?" — yes

### 13.3 Ongoing health practice

Quarterly self-audit using codedb:
- `codedb_outline` on each module → structure stays clean
- `codedb_deps` → dependency graph matches intent
- `codedb_hot` → which modules churn most (signal of contract iteration needed)

---

## 14. Decision register

Decisions made during brainstorming, recorded for future reference:

| # | Decision | Rationale |
|---|---|---|
| D1 | Microkernel architecture (not just multi-module monorepo) | Multi-module without kernel discipline is worst-of-both-worlds; cost is the same, benefit much larger |
| D2 | Incremental migration (not big-bang) | Big refactors fail; incremental always shippable; contracts refine as we discover |
| D3 | Pure microkernel split (not + harness, not + polyglot) | Smaller scope; lower risk; gets 80% of the value |
| D4 | TanStack-style DX as cross-cutting quality bar | Aligns with PHILOSOPHY; microkernel naturally enables it |
| D5 | Store and Memory NOT in core | Litmus test failed for both; cleaner microkernel |
| D6 | Tool shape: Path B (atomic + generics) | Cleaner; unlocks generics; matches MCP's model |
| D7 | Memory fully evicted from core (no kernel hook) | Memory IS a Processor — no special core treatment needed |
| D8 | Phase 1 external review (P1.6) skipped | No external users; substitute is building 2nd app shape |
| D9 | Per-module versioning, independent v1.0 promotions | Modules evolve on own timelines; user opts into stability per-package |
| D10 | Framework name deferred | Will decide before Phase 0 starts |
| D11 | `go.work` workspace file for local development | Avoids fighting replace directives during multi-module dev |
| D12 | AI-native execution mode (single AI agent, long-running branch) | No multi-author coordination needed; collapses bridge patterns, merges Phase 0 untangling with Phase 2 extraction per module |
| D13 | `cmd/bot_example` deleted in P0.1 | No longer the integration gate; replaced by per-module `example_test.go` + kernel contract stress tests |
| D14 | 8 wrapper/CC-shaped tool packages deleted in P0.1 | `tools/{knowledge, remember, skill, shell, file, search, schedule, todo}` will be reimplemented in their owner modules during extractions, or in the harness layer (Phase 3b) for CC-shaped ones |
| D15 | `catalog.go` stays in core (not extracted) | Pure vocabulary types (`Protocol`, `Platform`, `ModelInfo`, `ModelCapabilities`, `ModelPricing`, `ModelStatus`) shared across satellites (`provider/*`, `observer/`). Analogous to `ChatRequest`/`ChatResponse` — protocol types belong with the kernel. Also avoids naming collision with existing `provider/catalog/` subpackage. Drops extraction count from 8 → 7. |
| D16 | Skill loading extracted as `oasis/skills` battery (NOT moved to harness, NOT deleted) | "Loadable agent instructions" is a fundamental agent pattern (every non-trivial agent app needs it), not a Claude-Code-only feature. Earlier confusion conflated the *pattern* (general, needed) with the *implementation format* (CC-specific frontmatter — can be redesigned). Module absorbs current `skill*.go` files + existing `skills/` asset directory; assets become embedded via `embed.FS`. |
| D17 | HITL extracted as `oasis/input` module; `InputHandler` interface removed from core | Auto-magic in core (loop auto-generates `ask_user` tool when InputHandler is set) violates §4 "composition at app layer, never takeover." Cleaner design: `input.AskUserTool(handler) AnyTool` wired explicitly via `WithTools`. Kernel forgets HITL exists. |
| D18 | Pin positioning statement in spec §4.5 | "Kernel + curated batteries + optional harness" makes the battery-vs-harness distinction load-bearing. Differentiates from "tiny kernel, BYO everything" (austere/unusable) and "monolithic framework" (current state). Test for battery: needed by non-trivial % of production agent apps. Test for harness: only makes sense as part of one specific agent style. |

---

## 15. Total timeline

Two estimates depending on execution mode:

### 15.1 Human-coordinated estimate (original)

| Phase | Duration | Cumulative |
|---|---|---|
| Phase 0: Decouple | 4 weeks | week 4 |
| Phase 1: Lock candidate | 3 weeks | week 7 |
| Phase 2: Extraction | 14 weeks | week 21 |
| **Total** | **~21 weeks (~5 months)** | — |

### 15.2 AI-native estimate (actual execution mode, per §4.4 and D12)

| Work | Duration |
|---|---|
| P0.1 Tool shape (in-flight, bridge exception) | ~1 week |
| Per-module extractions (untangle + modularize + stress-test, 9 modules) | ~5-6 weeks total |
| Contract freeze + v0.17.0 lock candidate tag | ~1 week |
| **Total** | **~6-8 weeks** |

The AI-native total is significantly shorter primarily because Phase 0 and Phase 2 collapse per-module (eliminating the in-place-untangle-then-modularize two-step) and Phase 1 happens continuously (each extraction's stress-test is contract validation in real time).

Per-module v1.0 promotions happen on their own timeline after extractions complete (D9).

---

## 16. Open items before Phase 0 starts

- [ ] Decide final framework name (or commit to keeping "oasis")
- [ ] Approve this spec
- [ ] Begin Phase 0 by writing the writing-plans implementation plan for P0.1 (Tool shape migration)
