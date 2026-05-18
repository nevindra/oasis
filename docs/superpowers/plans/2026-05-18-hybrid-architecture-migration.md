# Hybrid Architecture Migration — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reshape the Oasis repo from "1 module + 4 satellites where some shouldn't be satellites" into "1 root module with public subpackages + curated re-exports + 9 heavy-dep satellites" per [2026-05-18 hybrid-architecture-design](../specs/2026-05-18-hybrid-architecture-design.md).

**Architecture:** Single root `oasis` module exposes a curated re-export umbrella (`oasis.go`) over public subpackages (`agent/`, `workflow/`, `compaction/`, etc.). Subpackages depend on a leaf `oasis/core/` package for shared protocol types and interfaces (resolves the re-export circular-import constraint). Heavy/optional-dep code (`store/sqlite`, `observer`, `sandbox`, etc.) becomes satellites with their own `go.mod`. Cross-subpackage boundaries enforced by `golangci-lint depguard`. Runtime guts (`loop`, `suspend`, `batch`, `stream`) hide in `internal/`.

**Tech Stack:** Go 1.25+ multi-module workspace (`go.work`), `golangci-lint` (depguard), standard `go test ./...` per module, no third-party orchestration.

---

## Critical foundation decision (resolved here, was open in spec §10)

The hybrid spec left open: *"Decide whether `types` and `catalog` get their own subpackages or stay at root namespace."* This plan resolves it:

**Protocol types and core interfaces MOVE to a new `oasis/core/` subpackage.** Rationale:
- The re-export model (`oasis.NewSlidingWindow = compaction.NewSlidingWindow`) requires root to import subpackages.
- Subpackages currently import root for `Provider`, `Processor`, `Tool`, `AnyTool`, `Message`, etc.
- Both cannot hold — Go forbids circular imports. A third leaf package must own the shared types.
- `oasis/core/` is that leaf. Nothing under `oasis/*` imports back into it.

`catalog.go` (the model vocabulary — `Protocol`, `Platform`, `ModelInfo`, …) **also moves to `oasis/core/`** to keep all framework-wide vocabulary in one place. Provider satellites and observer import it from there.

This is one deviation from the hybrid spec (§10 default was "stay at root") and one alignment with the prior microkernel spec (which had `oasis/core/`). The naming is intentional — `core` is the leaf shared by all subpackages, NOT the kernel pattern that was abandoned.

---

## File reorganization map

| Source (current) | Destination | Phase | Notes |
|---|---|---|---|
| `types.go` (protocol types) | `core/types.go` | 0 | Provider, Message, ChatRequest, ChatResponse, Tool, ToolCall, ToolResult, ToolSchema, etc. |
| `processor.go` (`Processor` interface) | `core/processor.go` | 0 | Interface goes to core; chain helper to `processor/` |
| `tool_atomic.go` (`Tool[In,Out]`, `AnyTool`, `Erase`) | `core/tool.go` + `tool/erase.go` | 0 | Interfaces to core; concrete `Erase()` helper to `tool/` |
| `catalog.go` | `core/catalog.go` | 0 | Vocabulary types |
| `compaction.go` (`Compactor` interface) | `core/compactor.go` | 0 | Interface to core (used by `WithCompaction` option) |
| `ratelimit/` (satellite) | `ratelimit/` (subpackage) | 1 | Delete `go.mod`, `go.sum`, remove from `go.work` |
| `guardrail/` (satellite) | `guardrail/` (subpackage) | 1 | Same |
| `compaction/` (satellite) | `compaction/` (subpackage) | 1 | Same |
| `agent.go`, `agentcore.go`, `llmagent.go`, `handle.go` | `agent/` | 2 | Largest move; touches most |
| `workflow*.go` (4 files) | `workflow/` | 2 | |
| `network.go` | `network/` | 2 | |
| `memory.go` | `memory/` | 2 | |
| `skill*.go` (4 files in root) | `skills/` | 2 | `skills/` currently holds only asset dirs |
| `retriever.go`, `cosine.go` | `rag/` satellite | 4 | New satellite (embedding clients, vector libs) |
| `loop.go`, `suspend.go`, `batch.go`, `stream.go` | `internal/runtime/` | 5 | Runtime guts |
| `retry.go`, `scheduler.go`, `tracer.go`, `ingest_checkpoint.go` | TBD per task | 2 | Decided per file |
| `store/{sqlite,postgres}/` (subpackages) | satellites | 3 | Add `go.mod`; remove pgx + sqlite deps from root |
| `provider/{gemini,openaicompat}/` (subpackages) | satellites | 3 | Add `go.mod` |
| `observer/` (subpackage) | satellite | 3 | Move OTEL deps (~15 packages — heaviest dep weight in tree) |
| `ingest/` (subpackage) | satellite | 3 | Move PDF/DOCX/embedding deps |
| `sandbox/` (subpackage) | satellite | 3 | Move Docker SDK |

**Files staying at root:** `oasis.go` (new — the re-export umbrella), `doc.go` (top-level getting-started).

---

## Commit strategy

User explicitly said *"no need to commit until all task finished"*. The plan reflects that: **one final commit at the end of Phase 6** containing the full reshape. Per-phase commits are NOT included.

Risk acknowledged: a single mega-commit makes mid-flight rollback to a phase boundary expensive (must use `git reset` + restage). To mitigate, **the working tree must be green (all tests pass) at every phase boundary** so the executing agent has confidence to proceed without intermediate commits.

If at any point the agent decides intermediate checkpoints are valuable, taking a `git stash` snapshot or branch-tag is reasonable — these don't pollute history.

---

## Parallel-execution markers

Each phase header notes parallelism. Tasks marked **[PARALLEL]** within a phase can be dispatched to subagents simultaneously. Tasks without that marker must be sequential within their phase.

---

# Phase 0 — Foundation: create `oasis/core/` and migrate protocol types

**Goal:** Establish the leaf package that owns the framework's shared types. After this phase, the entire root module compiles with types imported from `core` instead of declared at root, removing the circular-import barrier that blocks re-exports.

**Parallelism:** Steps 0.1–0.5 sequential (each builds on prior). Step 0.6 (verification) sequential.

**Duration estimate:** ~4-6 hours.

---

### Task 0.1: Create `oasis/core/` scaffold

**Files:**
- Create: `core/doc.go`
- Create: `core/go.mod`-equivalent decision (NONE — `core/` is a subpackage of the root module, no separate `go.mod`)

- [ ] **Step 1: Create the directory and doc**

```bash
mkdir -p /home/nezhifi/Code/LLM/oasis/core
```

Create `/home/nezhifi/Code/LLM/oasis/core/doc.go`:

```go
// Package core defines the protocol types and interfaces shared across all
// Oasis subpackages and satellites. It is a leaf package: nothing under
// github.com/nevindra/oasis/* imports anything other than core itself.
//
// User code should not import this package directly. Use the curated
// re-exports from github.com/nevindra/oasis instead.
package core
```

- [ ] **Step 2: Verify package compiles empty**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./core/...`
Expected: no output (success).

---

### Task 0.2: Move protocol types from `types.go` to `core/types.go`

**Files:**
- Read: `types.go` (27.2K — discover all type/interface declarations)
- Create: `core/types.go`
- Modify: `types.go` (remove migrated types, leave only types that genuinely belong at root — likely empty after migration)

**Approach:** This is a `git mv`-style move at the symbol level, not a file-level move. `types.go` contains many declarations; each is evaluated for destination.

- [ ] **Step 1: Inventory `types.go` contents**

Run:
```bash
grep -E '^(type|func|const|var) ' /home/nezhifi/Code/LLM/oasis/types.go > /tmp/types-inventory.txt
cat /tmp/types-inventory.txt
```

Expected: list of all top-level declarations in types.go. Use this to decide what moves to core vs. stays.

**Decision rule per symbol:**
- Protocol type (used in `Provider.Chat`, `Tool.Execute`, message flow): → `core/`
- Workflow-specific type (`WorkflowDefinition`, `NodeType`, etc., per spec §7.5): → `workflow/` (Phase 2)
- Agent config option struct private to root files: leave for now, address in Phase 2

- [ ] **Step 2: Create `core/types.go` with the protocol types**

For each protocol type identified in Step 1, copy declaration verbatim to `core/types.go` under `package core`. At minimum this includes:

- `Message`, `MessageRole` (constants), `Content`, `ContentPart`
- `ChatRequest`, `ChatResponse`, `ChatOptions`, `FinishReason`, `Usage`
- `ChatMessage` (if distinct from Message)
- `ToolCall`, `ToolResult`, `ToolSchema`, `ToolOutput`, `ToolContext`
- `Task`, `Result`, `TaskOptions`
- `StreamEvent` interface + concrete events (`TextDelta`, `ToolCallStart`, `ToolCallEnd`, `Done`)
- `Suspended`, `SuspendReason`
- Any other types referenced by `Provider`, `Tool`, `Processor`, `Agent`, or the loop

If the type has methods, move the methods too.

- [ ] **Step 3: Remove migrated declarations from root `types.go`**

For each declaration moved, delete it from `/home/nezhifi/Code/LLM/oasis/types.go`. If `types.go` ends up empty or with only stub content, delete the file entirely:

```bash
# If empty:
rm /home/nezhifi/Code/LLM/oasis/types.go
```

- [ ] **Step 4: Add re-export aliases temporarily in root**

To minimize churn during Phase 0, create `/home/nezhifi/Code/LLM/oasis/types_aliases.go`:

```go
package oasis

import "github.com/nevindra/oasis/core"

// Temporary aliases during Phase 0 migration. These keep existing root-package
// callers compiling without rewriting every reference site. Phase 2 moves
// callers into subpackages that import `core` directly, at which point
// this file is deleted.

type Message = core.Message
type MessageRole = core.MessageRole
type Content = core.Content
type ContentPart = core.ContentPart
type ChatRequest = core.ChatRequest
type ChatResponse = core.ChatResponse
type ChatOptions = core.ChatOptions
type ChatMessage = core.ChatMessage
type FinishReason = core.FinishReason
type Usage = core.Usage
type ToolCall = core.ToolCall
type ToolResult = core.ToolResult
type ToolSchema = core.ToolSchema
type ToolOutput = core.ToolOutput
type ToolContext = core.ToolContext
type Task = core.Task
type Result = core.Result
type TaskOptions = core.TaskOptions
type StreamEvent = core.StreamEvent
type TextDelta = core.TextDelta
type ToolCallStart = core.ToolCallStart
type ToolCallEnd = core.ToolCallEnd
type Done = core.Done
type Suspended = core.Suspended
type SuspendReason = core.SuspendReason

// Add constant aliases for MessageRole values, FinishReason values, etc.
// Exact list depends on inventory in Step 1.
```

For each constant in core (e.g. `core.RoleSystem`), add:
```go
const RoleSystem = core.RoleSystem
```

- [ ] **Step 5: Verify root module still compiles**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./...`
Expected: success.

- [ ] **Step 6: Run all tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./...`
Expected: all tests pass.

---

### Task 0.3: Move `Provider` interface from root to `core/`

**Files:**
- Read: find where `Provider` is declared (likely `types.go` or `provider.go` at root — check during Step 1)
- Create or modify: `core/provider.go`
- Modify: original location (remove declaration)
- Modify: `types_aliases.go` (add Provider alias)

- [ ] **Step 1: Find Provider declaration**

Run:
```bash
grep -n "type Provider" /home/nezhifi/Code/LLM/oasis/*.go
```

Expected: one or two matches identifying the source file.

- [ ] **Step 2: Copy declaration to `core/provider.go`**

Create `/home/nezhifi/Code/LLM/oasis/core/provider.go`:

```go
package core

import "context"

// Provider talks to an LLM. Implementations MUST be safe to call concurrently.
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// StreamingProvider is an optional capability — providers that support
// server-side streaming implement this in addition to Provider.
type StreamingProvider interface {
    Provider
    ChatStream(ctx context.Context, req ChatRequest, out chan<- StreamEvent) error
}

// EmbeddingProvider is an optional capability for providers that expose
// an embeddings API.
type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

(If the existing Provider/StreamingProvider/EmbeddingProvider has different exact signatures, mirror those — do not redesign here.)

- [ ] **Step 3: Remove Provider declaration from original location**

Delete the `type Provider interface { ... }` block from wherever it currently lives. Same for `StreamingProvider`, `EmbeddingProvider` if at root.

- [ ] **Step 4: Add aliases to `types_aliases.go`**

Append to `/home/nezhifi/Code/LLM/oasis/types_aliases.go`:

```go
type Provider = core.Provider
type StreamingProvider = core.StreamingProvider
type EmbeddingProvider = core.EmbeddingProvider
```

- [ ] **Step 5: Verify build and tests**

Run:
```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```
Expected: all pass.

---

### Task 0.4: Move `Tool`, `AnyTool`, `Erase` from `tool_atomic.go` to `core/` and `tool/`

**Files:**
- Read: `tool_atomic.go`
- Create: `core/tool.go` (interfaces only)
- Create: `tool/erase.go` (concrete `Erase[In,Out]` helper) — subpackage with own `doc.go`
- Create: `tool/doc.go`
- Modify: `tool_atomic.go` (delete after migration)
- Modify: `types_aliases.go` (add aliases)

- [ ] **Step 1: Read `tool_atomic.go` to inventory contents**

Run: `Read /home/nezhifi/Code/LLM/oasis/tool_atomic.go`

Identify: which symbols are interfaces (→ `core/`) and which are concrete helpers (→ `tool/`).

- [ ] **Step 2: Create `core/tool.go` with interfaces**

```go
package core

import (
    "context"
    "encoding/json"
)

// Tool is a type-safe single-operation capability. Atomic: one Tool = one
// operation (NOT a bundle of operations).
type Tool[In, Out any] interface {
    Name() string
    Schema() ToolSchema
    Execute(ctx context.Context, in In) (Out, error)
}

// AnyTool is the type-erased form that the agent loop iterates over.
type AnyTool interface {
    Name() string
    Schema() ToolSchema
    ExecuteRaw(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

// MultimodalTool is an optional capability for tools returning multimodal output.
type MultimodalTool interface {
    AnyTool
    ExecuteMultimodal(ctx context.Context, args json.RawMessage) (ToolOutput, error)
}

// ContextAwareTool is an optional capability for tools needing the
// invoking agent's context (per-thread state, etc.).
type ContextAwareTool interface {
    AnyTool
    SetContext(ToolContext)
}
```

Mirror whatever signatures exist in the current `tool_atomic.go`; do not redesign.

- [ ] **Step 3: Create `tool/` subpackage scaffolding**

```bash
mkdir -p /home/nezhifi/Code/LLM/oasis/tool
```

Create `/home/nezhifi/Code/LLM/oasis/tool/doc.go`:

```go
// Package tool provides helpers for constructing and adapting tools that
// implement the contracts in github.com/nevindra/oasis/core.
//
// The main entry point is Erase, which adapts a type-safe Tool[In, Out]
// into an AnyTool that the agent loop can dispatch.
package tool
```

Create `/home/nezhifi/Code/LLM/oasis/tool/erase.go` by copying the body of `Erase` from `tool_atomic.go`, updating its receiver/argument types from `Tool[In, Out]` and `AnyTool` to `core.Tool[In, Out]` and `core.AnyTool`:

```go
package tool

import (
    "context"
    "encoding/json"

    "github.com/nevindra/oasis/core"
)

// Erase adapts a type-safe Tool into the type-erased AnyTool the agent loop
// uses. The returned AnyTool unmarshals raw JSON into In, calls Execute,
// and marshals Out back to JSON.
func Erase[In, Out any](t core.Tool[In, Out]) core.AnyTool {
    // body copied from existing tool_atomic.go Erase implementation
    // with type references updated to core.* prefix
}
```

- [ ] **Step 4: Create `tool/erase_test.go` by moving `tool_atomic_test.go`**

```bash
git mv /home/nezhifi/Code/LLM/oasis/tool_atomic_test.go /home/nezhifi/Code/LLM/oasis/tool/erase_test.go
```

Edit `/home/nezhifi/Code/LLM/oasis/tool/erase_test.go`:
- Change `package oasis` (or `package oasis_test`) to `package tool` (or `package tool_test`)
- Update any type references to use `core.` prefix where they reference Tool/AnyTool

- [ ] **Step 5: Delete `tool_atomic.go` from root**

```bash
rm /home/nezhifi/Code/LLM/oasis/tool_atomic.go
```

- [ ] **Step 6: Add aliases to `types_aliases.go`**

Append:
```go
type AnyTool = core.AnyTool
// Tool is a generic interface — Go does not allow generic aliases, so
// users must use core.Tool[In,Out] directly or via the tool subpackage.
type MultimodalTool = core.MultimodalTool
type ContextAwareTool = core.ContextAwareTool
```

Also add a function-variable re-export for Erase in `tool_aliases.go` (a new file at root):

```go
package oasis

import "github.com/nevindra/oasis/tool"

// Erase adapts a type-safe Tool into AnyTool. See package github.com/nevindra/oasis/tool.
var Erase = tool.Erase[any, any] // placeholder; see note below
```

NOTE: Go generic re-export is awkward. The cleanest pattern is to require users to import `tool` directly for `tool.Erase[A, B](...)`. Do NOT alias the generic function at root. Update the curated re-export plan to expose `tool` as a subpackage users import explicitly:

```go
// In oasis.go (later, Phase 1+):
// (no Erase re-export — users import "github.com/nevindra/oasis/tool")
```

- [ ] **Step 7: Verify build and tests**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```

If `tool_registry_test.go` exists at root and depends on the moved `Erase`, update its imports to use `tool.Erase`.

Expected: all pass.

---

### Task 0.5: Move `Processor` from `processor.go` to `core/`, move `ProcessorChain` to `processor/`

**Files:**
- Read: `processor.go`
- Create: `core/processor.go`
- Create: `processor/chain.go`
- Create: `processor/doc.go`
- Move: `processor_test.go` → `processor/chain_test.go` (split if needed; some tests may be for the interface contract — keep those in `core/contract_processor_test.go`)
- Modify: `types_aliases.go` (add `Processor` alias)

- [ ] **Step 1: Read `processor.go` and inventory**

Run: `Read /home/nezhifi/Code/LLM/oasis/processor.go`

Identify:
- `type Processor interface` → `core/`
- `type ProcessorChain` (concrete slice helper) → `processor/`
- Any helper functions → `processor/` unless they're zero-dep utilities

- [ ] **Step 2: Create `core/processor.go`**

```go
package core

import "context"

// Processor transforms a message slice in the pipeline. Compaction,
// guardrails, memory injection, and similar concerns all satisfy this
// interface. Implementations MUST be safe to call concurrently.
type Processor interface {
    Process(ctx context.Context, msgs []Message) ([]Message, error)
}
```

(Mirror current signature exactly.)

- [ ] **Step 3: Create `processor/` subpackage**

```bash
mkdir -p /home/nezhifi/Code/LLM/oasis/processor
```

Create `/home/nezhifi/Code/LLM/oasis/processor/doc.go`:

```go
// Package processor provides helpers for composing core.Processor
// implementations into pipelines.
package processor
```

Create `/home/nezhifi/Code/LLM/oasis/processor/chain.go` by copying `ProcessorChain` from root `processor.go`:

```go
package processor

import (
    "context"

    "github.com/nevindra/oasis/core"
)

// Chain composes multiple Processors into a single Processor that runs
// them in order. If any processor returns an error, the chain stops and
// the error is propagated.
type Chain []core.Processor

// Process implements core.Processor.
func (c Chain) Process(ctx context.Context, msgs []core.Message) ([]core.Message, error) {
    // copy body from existing ProcessorChain.Process
}
```

NOTE: rename `ProcessorChain` → `Chain` per Go convention (no stutter inside its package). Add an alias at root for backward compat during migration.

- [ ] **Step 4: Move tests**

```bash
git mv /home/nezhifi/Code/LLM/oasis/processor_test.go /home/nezhifi/Code/LLM/oasis/processor/chain_test.go
```

Update package declaration and type references.

- [ ] **Step 5: Delete root `processor.go`**

```bash
rm /home/nezhifi/Code/LLM/oasis/processor.go
```

- [ ] **Step 6: Add aliases**

Append to `/home/nezhifi/Code/LLM/oasis/types_aliases.go`:
```go
type Processor = core.Processor
```

Create `/home/nezhifi/Code/LLM/oasis/processor_aliases.go`:
```go
package oasis

import "github.com/nevindra/oasis/processor"

// ProcessorChain is retained at root as an alias to processor.Chain
// for backward compatibility during the migration. New code should use
// processor.Chain directly.
type ProcessorChain = processor.Chain
```

- [ ] **Step 7: Verify build and tests**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```
Expected: all pass.

---

### Task 0.6: Move `Compactor` interface and `catalog.go` to `core/`

**Files:**
- Read: `compaction.go` (root, defines `Compactor` interface)
- Read: `catalog.go` (root, model vocabulary)
- Create: `core/compactor.go`
- Create: `core/catalog.go`
- Delete: `compaction.go` (root), `catalog.go` (root)
- Modify: `types_aliases.go`, agent.go (update `WithCompaction` to reference `core.Compactor`)

- [ ] **Step 1: Move `Compactor` interface to `core/compactor.go`**

```go
package core

import "context"

// Compactor turns a message list into a structured summary via an LLM call.
// Implementations MUST be safe to call concurrently.
type Compactor interface {
    Compact(ctx context.Context, req CompactRequest) (CompactResult, error)
}

// CompactRequest, CompactResult, CompactSection — copy verbatim from
// root compaction.go.
type CompactRequest struct { /* ... */ }
type CompactSection struct { /* ... */ }
type CompactResult struct { /* ... */ }
```

Copy struct bodies verbatim from `/home/nezhifi/Code/LLM/oasis/compaction.go`.

- [ ] **Step 2: Delete root `compaction.go`**

```bash
rm /home/nezhifi/Code/LLM/oasis/compaction.go
```

- [ ] **Step 3: Move `catalog.go` to `core/catalog.go`**

```bash
git mv /home/nezhifi/Code/LLM/oasis/catalog.go /home/nezhifi/Code/LLM/oasis/core/catalog.go
```

Edit `/home/nezhifi/Code/LLM/oasis/core/catalog.go`: change `package oasis` → `package core`.

Also move test:
```bash
git mv /home/nezhifi/Code/LLM/oasis/catalog_types_test.go /home/nezhifi/Code/LLM/oasis/core/catalog_types_test.go
```
Update package declaration in the test.

- [ ] **Step 4: Add aliases**

Append to `/home/nezhifi/Code/LLM/oasis/types_aliases.go`:
```go
type Compactor = core.Compactor
type CompactRequest = core.CompactRequest
type CompactSection = core.CompactSection
type CompactResult = core.CompactResult

// Catalog types
type Protocol = core.Protocol
type Platform = core.Platform
type ModelInfo = core.ModelInfo
type ModelCapabilities = core.ModelCapabilities
type ModelPricing = core.ModelPricing
type ModelStatus = core.ModelStatus
```

Add constant aliases for any enum-style consts catalog.go declared.

- [ ] **Step 5: Update existing satellite imports**

The current satellites (`ratelimit/`, `guardrail/`, `compaction/`, `mcp/`) currently import `oasis` for `oasis.Provider`, `oasis.Processor`, etc. These imports continue to work via the type aliases in `types_aliases.go`. **Do NOT update satellite imports in Phase 0** — they're aliases that resolve to the right types. Phase 1+ updates them to import `core` directly.

- [ ] **Step 6: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
cd /home/nezhifi/Code/LLM/oasis/ratelimit && go build ./... && go test ./...
cd /home/nezhifi/Code/LLM/oasis/guardrail && go build ./... && go test ./...
cd /home/nezhifi/Code/LLM/oasis/compaction && go build ./... && go test ./...
cd /home/nezhifi/Code/LLM/oasis/mcp && go build ./... && go test ./...
```

Expected: all pass.

---

### Task 0.7: Phase 0 verification checkpoint

- [ ] **Step 1: Confirm root no longer declares the migrated types**

```bash
grep -l "^type Provider\|^type Processor\|^type Tool\[\|^type Compactor\|^type Message\b" /home/nezhifi/Code/LLM/oasis/*.go
```
Expected: NO matches (all have moved to `core/`).

- [ ] **Step 2: Confirm `core/` has no internal-oasis imports**

```bash
cd /home/nezhifi/Code/LLM/oasis && go list -deps ./core/... | grep nevindra/oasis | grep -v '/oasis/core'
```
Expected: empty output.

- [ ] **Step 3: Run full test suite for all modules**

```bash
cd /home/nezhifi/Code/LLM/oasis && \
  go test ./... && \
  cd ratelimit && go test ./... && cd .. && \
  cd guardrail && go test ./... && cd .. && \
  cd compaction && go test ./... && cd .. && \
  cd mcp && go test ./... && cd ..
```
Expected: all green.

---

# Phase 1 — Demote misplaced satellites back to subpackages

**Goal:** `ratelimit/`, `guardrail/`, `compaction/` are stdlib-only and were extracted as separate Go modules by mistake (per hybrid spec H7). Roll them back to subpackages of the root module. Add their first re-export aliases to a new `oasis.go` umbrella file. Set up the `golangci-lint depguard` scaffold.

**Parallelism:** Tasks 1.1, 1.2, 1.3 are **[PARALLEL]** (three independent module demotions). Tasks 1.4–1.6 sequential after.

**Duration estimate:** ~2-3 hours with parallelism.

---

### Task 1.1: Demote `ratelimit` to subpackage **[PARALLEL with 1.2, 1.3]**

**Files:**
- Delete: `ratelimit/go.mod`, `ratelimit/go.sum`
- Modify: `ratelimit/*.go` (update import paths)
- Modify: `go.work` (remove `./ratelimit`)

- [ ] **Step 1: Delete the satellite's module files**

```bash
rm /home/nezhifi/Code/LLM/oasis/ratelimit/go.mod
rm /home/nezhifi/Code/LLM/oasis/ratelimit/go.sum
```

- [ ] **Step 2: Update `ratelimit/*.go` imports to use `core` directly**

Run:
```bash
grep -rn 'oasis "github.com/nevindra/oasis"\|"github.com/nevindra/oasis"' /home/nezhifi/Code/LLM/oasis/ratelimit/
```

For every match, change:
- Import path from `oasis "github.com/nevindra/oasis"` to `"github.com/nevindra/oasis/core"`
- Type references from `oasis.Provider` → `core.Provider`, `oasis.Processor` → `core.Processor`, etc.

If `ratelimit` only uses Provider, the changes are minimal — likely 3-5 sites.

- [ ] **Step 3: Remove `./ratelimit` from `go.work`**

Edit `/home/nezhifi/Code/LLM/oasis/go.work`:

Before:
```
use (
    .
    ./compaction
    ./guardrail
    ./mcp
    ./ratelimit
)
```

After:
```
use (
    .
    ./mcp
)
```

(Compaction and guardrail will also be removed in their parallel tasks. The final state after 1.1+1.2+1.3 keeps only `.` and `./mcp` in `use`.)

- [ ] **Step 4: Verify ratelimit builds as subpackage**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./ratelimit/... && go test ./ratelimit/...
```
Expected: success.

---

### Task 1.2: Demote `guardrail` to subpackage **[PARALLEL with 1.1, 1.3]**

Same recipe as Task 1.1, paths swapped to `guardrail/`.

- [ ] **Step 1: Delete module files**

```bash
rm /home/nezhifi/Code/LLM/oasis/guardrail/go.mod
rm /home/nezhifi/Code/LLM/oasis/guardrail/go.sum
```

- [ ] **Step 2: Update imports**

```bash
grep -rn 'oasis "github.com/nevindra/oasis"\|"github.com/nevindra/oasis"' /home/nezhifi/Code/LLM/oasis/guardrail/
```

Replace each `oasis.X` reference with `core.X`. Update import lines accordingly.

- [ ] **Step 3: (`go.work` edit consolidated in Task 1.1 — skip here if 1.1 already removed `./guardrail`)**

- [ ] **Step 4: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./guardrail/... && go test ./guardrail/...
```
Expected: success.

---

### Task 1.3: Demote `compaction` to subpackage **[PARALLEL with 1.1, 1.2]**

Same recipe as Task 1.1, paths swapped to `compaction/`.

- [ ] **Step 1: Delete module files**

```bash
rm /home/nezhifi/Code/LLM/oasis/compaction/go.mod
rm /home/nezhifi/Code/LLM/oasis/compaction/go.sum
```

- [ ] **Step 2: Update imports**

```bash
grep -rn 'oasis "github.com/nevindra/oasis"\|"github.com/nevindra/oasis"' /home/nezhifi/Code/LLM/oasis/compaction/
```

Replace `oasis.Compactor`, `oasis.CompactRequest`, `oasis.CompactResult`, `oasis.Provider`, `oasis.ChatMessage`, etc. with `core.X` equivalents.

- [ ] **Step 3: (`go.work` edit — skip if 1.1 handled it)**

- [ ] **Step 4: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./compaction/... && go test ./compaction/...
```
Expected: success.

---

### Task 1.4: Consolidate `go.work` and verify all-module build

After 1.1–1.3 complete (parallelism converged):

- [ ] **Step 1: Ensure `go.work` is correct**

The final `go.work` should be:

```
go 1.26.1

use (
    .
    ./mcp
)
```

- [ ] **Step 2: Workspace-wide build and test**

```bash
cd /home/nezhifi/Code/LLM/oasis && \
  go build ./... && \
  go test ./... && \
  cd mcp && go build ./... && go test ./... && cd ..
```
Expected: all green.

---

### Task 1.5: Create the `oasis.go` re-export umbrella with Phase 1 entries

**Files:**
- Create: `oasis.go`

- [ ] **Step 1: Create `oasis.go` with header**

Create `/home/nezhifi/Code/LLM/oasis/oasis.go`:

```go
// Package oasis is the public umbrella for the Oasis agent framework.
//
// This file curates the re-export surface: users import a single package
// (github.com/nevindra/oasis) and get the common API via aliases.
//
// Niche or power-user APIs are deliberately NOT re-exported — those callers
// import the relevant subpackage directly (e.g. "github.com/nevindra/oasis/compaction").
//
// Adding a re-export here is a deliberate decision: it signals "this is part
// of the curated public surface." Do not auto-mirror every new export in a
// subpackage.
package oasis

import (
    "github.com/nevindra/oasis/compaction"
    "github.com/nevindra/oasis/guardrail"
    "github.com/nevindra/oasis/ratelimit"
)

// --- Compaction ---

// (Add re-exports for the canonical compaction constructors. Inspect
// compaction/ for what's available — likely NewStructuredCompactor and
// a sliding-window variant.)
var NewStructuredCompactor = compaction.NewStructuredCompactor

// --- Guardrail ---

// Re-exports for the most common guardrail. Inspect guardrail/ to confirm
// the constructor name (NewInjectionGuard per spec example).
var NewInjectionGuard = guardrail.NewInjectionGuard

// --- Rate limiting ---

// Re-exports for the rate limiter wrapper.
var WithRateLimit = ratelimit.WithRateLimit
var RPM = ratelimit.RPM
var TPM = ratelimit.TPM
```

NOTE: actual constructor/function names depend on what each subpackage exports. Inspect each package's `doc.go` and exported symbols, then list only the curated set. **Discovery commands:**

```bash
grep -E '^func [A-Z]|^var [A-Z]' /home/nezhifi/Code/LLM/oasis/compaction/*.go | grep -v _test.go
grep -E '^func [A-Z]|^var [A-Z]' /home/nezhifi/Code/LLM/oasis/guardrail/*.go | grep -v _test.go
grep -E '^func [A-Z]|^var [A-Z]' /home/nezhifi/Code/LLM/oasis/ratelimit/*.go | grep -v _test.go
```

Curate: pick 3-5 most commonly-used per package. Niche knobs stay subpackage-only.

- [ ] **Step 2: Verify build**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./...
```
Expected: success.

---

### Task 1.6: Set up `golangci-lint` with `depguard` scaffold

**Files:**
- Create: `.golangci.yml`

- [ ] **Step 1: Check if a config already exists**

```bash
ls /home/nezhifi/Code/LLM/oasis/.golangci.yml /home/nezhifi/Code/LLM/oasis/.golangci.yaml 2>&1
```

If a config exists, merge into it. Otherwise, create new.

- [ ] **Step 2: Create or extend `.golangci.yml`**

Create `/home/nezhifi/Code/LLM/oasis/.golangci.yml` (if none exists):

```yaml
version: "2"

linters:
  default: none
  enable:
    - depguard

linters-settings:
  depguard:
    rules:
      # core is a leaf: nothing under oasis/* may be imported from it.
      core:
        files:
          - "core/**/*.go"
        deny:
          - pkg: "github.com/nevindra/oasis"
            desc: "core is a leaf package; it must not import from oasis or its subpackages"
          - pkg: "github.com/nevindra/oasis/"
            desc: "core is a leaf package; it must not import from oasis subpackages"

      # ratelimit only depends on core.
      ratelimit:
        files:
          - "ratelimit/**/*.go"
        deny:
          - pkg: "github.com/nevindra/oasis$"
            desc: "ratelimit must not import the root oasis package (would cause circular import with re-exports)"
          # add more denies as other subpackages get added

      # guardrail only depends on core.
      guardrail:
        files:
          - "guardrail/**/*.go"
        deny:
          - pkg: "github.com/nevindra/oasis$"
            desc: "guardrail must not import the root oasis package"

      # compaction only depends on core.
      compaction:
        files:
          - "compaction/**/*.go"
        deny:
          - pkg: "github.com/nevindra/oasis$"
            desc: "compaction must not import the root oasis package"
```

NOTE: golangci-lint v2 schema is current at the time of writing (2026-05). If the version installed differs, adapt the schema. Check with `golangci-lint version`.

- [ ] **Step 3: Verify lint passes**

```bash
cd /home/nezhifi/Code/LLM/oasis && golangci-lint run ./...
```

If `golangci-lint` is not installed, install:
```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

Expected: no depguard violations. Other linters may flag things but they're disabled in this config (we use `default: none` and only enable depguard for now).

- [ ] **Step 4: Verify all tests still pass after lint config added**

```bash
cd /home/nezhifi/Code/LLM/oasis && go test ./...
```

---

### Task 1.7: Phase 1 verification checkpoint

- [ ] **Step 1: Confirm satellite-to-subpackage demotion is complete**

```bash
ls /home/nezhifi/Code/LLM/oasis/ratelimit/go.mod /home/nezhifi/Code/LLM/oasis/guardrail/go.mod /home/nezhifi/Code/LLM/oasis/compaction/go.mod 2>&1
```
Expected: all three "No such file or directory".

- [ ] **Step 2: Confirm only valid satellite modules remain**

```bash
find /home/nezhifi/Code/LLM/oasis -maxdepth 3 -name go.mod
```
Expected:
```
/home/nezhifi/Code/LLM/oasis/go.mod
/home/nezhifi/Code/LLM/oasis/mcp/go.mod
```

- [ ] **Step 3: Smoke-test re-exports compile**

Create `/tmp/oasis_smoke_test/main.go`:

```go
package main

import (
    "github.com/nevindra/oasis"
)

func main() {
    _ = oasis.NewInjectionGuard
    _ = oasis.WithRateLimit
    _ = oasis.NewStructuredCompactor
}
```

This is for visual confirmation only — do not commit. If the agent prefers, do this check by inspecting `oasis.go` symbols via:

```bash
cd /home/nezhifi/Code/LLM/oasis && go doc .
```

Expected: lists the re-exported aliases.

- [ ] **Step 4: Full workspace test**

```bash
cd /home/nezhifi/Code/LLM/oasis && go test ./... && (cd mcp && go test ./...)
```
Expected: all green.

---

# Phase 2 — Move remaining root `.go` files into subpackages

**Goal:** Move `input` (if present), `memory`, `workflow`, `network`, `skills`, `agent` code from root into per-concern subpackages. After this phase, root contains only `oasis.go`, `doc.go`, alias files, and (temporarily) the runtime files (`loop.go`, `suspend.go`, `batch.go`, `stream.go`) which move to `internal/` in Phase 5.

**Parallelism:**
- 2.1 (memory), 2.2 (workflow), 2.3 (network), 2.4 (skills) are **[PARALLEL]** — independent subpackage moves.
- 2.5 (agent) is sequential and last, because agent.go imports everything else.

**Duration estimate:** ~1-2 days.

---

### Recipe: Move a root-file group into a subpackage

The following recipe applies to every Phase 2 task. Each task lists the specific files and special cases; the steps below are the standard pattern.

**Standard steps for each subpackage move:**

1. Create the destination directory (if not present): `mkdir -p <pkg>/`
2. Move all source files via `git mv`: preserves history
3. Update each moved file:
   - Change `package oasis` → `package <pkg>`
   - Replace internal type references with `core.X` where the type lives in core
   - Add `import "github.com/nevindra/oasis/core"` if needed
4. Move corresponding `_test.go` files; update package decl and imports
5. Create `<pkg>/doc.go` with a real getting-started comment
6. Create `<pkg>/example_test.go` with at least one `ExampleNewXxx`
7. Add depguard rule block for `<pkg>` in `.golangci.yml`
8. Add curated re-exports to `oasis.go` (only the API users genuinely need at the umbrella)
9. Remove obsolete entries from `types_aliases.go` if a type fully moves away from root
10. Verify: `go build ./<pkg>/... && go test ./<pkg>/...` then `go test ./...`

---

### Task 2.1: Move `memory.go` and `agentmemory.go` to `memory/` **[PARALLEL with 2.2, 2.3, 2.4]**

**Files:**
- Modify: `memory.go` (root, 27.9K) → `memory/memory.go`
- Modify: `agentmemory.go` (root, ~?K — check first) → `memory/agentmemory.go`
- Modify: `memory_test.go` (root, 54.5K) → `memory/memory_test.go`
- Create: `memory/doc.go`, `memory/example_test.go`
- Modify: any root file currently importing/using the moved types

NOTE: `memory/` directory already exists per project structure (per CLAUDE.md). Check if it has content:

```bash
ls /home/nezhifi/Code/LLM/oasis/memory/ 2>&1
```

If empty or scaffolding only, proceed with move. If it has substantive code, integrate (most likely backend-agnostic helpers vs. orchestration in root — both belong here per hybrid spec §8.1).

- [ ] **Step 1: Inspect existing `memory/` content**

```bash
ls /home/nezhifi/Code/LLM/oasis/memory/ && head -20 /home/nezhifi/Code/LLM/oasis/memory/*.go 2>&1
```

- [ ] **Step 2: Move root memory files**

```bash
cd /home/nezhifi/Code/LLM/oasis
git mv memory.go memory/memory_orchestration.go
git mv memory_test.go memory/memory_orchestration_test.go
[ -f agentmemory.go ] && git mv agentmemory.go memory/agentmemory.go
```

(Renamed to `memory_orchestration.go` to avoid colliding with any pre-existing `memory.go` in the subpackage.)

- [ ] **Step 3: Update package declarations and imports**

For each moved file:
- Change `package oasis` → `package memory`
- Update type references: `Message` → `core.Message`, `Provider` → `core.Provider`, etc.
- Update test files' package decl to `memory` or `memory_test`
- Update any `oasis.X` qualifier inside tests to `memory.X` for moved symbols, `core.X` for core symbols

Use:
```bash
grep -rn "package oasis" /home/nezhifi/Code/LLM/oasis/memory/
```
to find every file needing the package rename.

- [ ] **Step 4: Find and update root callers**

```bash
cd /home/nezhifi/Code/LLM/oasis && grep -rln 'agentMemory\|MemoryStore\|memory\.\|ExtractedFact' *.go --include='*.go'
```

For each caller in remaining root files (likely `agent.go`, `llmagent.go`):
- Add `import "github.com/nevindra/oasis/memory"`
- Update references from local `agentMemory` to `memory.AgentMemory` (or whatever final name)

If some types are unexported in root (lowercase) and were used cross-file inside root package, they need to become exported during the move OR the callers also move to `memory/`. Decide per case.

- [ ] **Step 5: Create `memory/doc.go`**

```go
// Package memory provides agent memory orchestration: fact extraction,
// embedding-based recall, and prompt injection of recalled context.
//
// Memory is a core.Processor — wire it into an agent via
// core.WithProcessors(memory.Recall(provider)) or the curated re-export
// oasis.WithMemory(...).
//
// Storage is decoupled: pass any implementation of MemoryStore to
// configure where extracted facts persist.
package memory
```

(Adjust the example to match the actual API.)

- [ ] **Step 6: Create `memory/example_test.go`**

```go
package memory_test

import (
    "github.com/nevindra/oasis/memory"
)

func ExampleRecall() {
    // Construct a memory processor with the canonical defaults.
    // Replace with the actual minimal correct usage discovered during the move.
    _ = memory.Recall
}
```

(The exact example depends on what `memory` ends up exporting; fill in a runnable minimal example after Step 3 stabilizes the API.)

- [ ] **Step 7: Add depguard rule**

Edit `/home/nezhifi/Code/LLM/oasis/.golangci.yml`, add under `linters-settings.depguard.rules`:

```yaml
      memory:
        files:
          - "memory/**/*.go"
        deny:
          - pkg: "github.com/nevindra/oasis$"
            desc: "memory must not import the root oasis package"
```

- [ ] **Step 8: Add curated re-exports**

Append to `/home/nezhifi/Code/LLM/oasis/oasis.go`:

```go
import (
    // ... existing ...
    "github.com/nevindra/oasis/memory"
)

// --- Memory ---
var Recall = memory.Recall
// (add others as appropriate after inspecting memory/ exports)
```

- [ ] **Step 9: Remove redundant aliases from `types_aliases.go`**

If any types that moved to `memory/` had aliases in `types_aliases.go` (unlikely — that file is for `core` aliases), remove them.

- [ ] **Step 10: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
golangci-lint run ./memory/...
```
Expected: all pass.

---

### Task 2.2: Move `workflow*.go` to `workflow/` **[PARALLEL with 2.1, 2.3, 2.4]**

**Files:**
- `workflow.go` (23.3K) → `workflow/workflow.go`
- `workflow_definition.go` (14.2K) → `workflow/definition.go`
- `workflow_exec.go` (16.6K) → `workflow/exec.go`
- `workflow_steps.go` (6.9K) → `workflow/steps.go`
- Plus test files (`workflow_test.go`, `workflow_definition_test.go`, `workflow_exec_test.go`, `workflow_steps_test.go`)
- Also: per hybrid spec §7.5, `WorkflowDefinition`, `NodeType`, `NodeTemplate`, `NodeDefinition`, `DefinitionRegistry` MUST be moved out of root types if any still live there (Phase 0 should have already handled this if they were in `types.go`).

- [ ] **Step 1: Move source files**

```bash
cd /home/nezhifi/Code/LLM/oasis
mkdir -p workflow
git mv workflow.go workflow/workflow.go
git mv workflow_definition.go workflow/definition.go
git mv workflow_exec.go workflow/exec.go
git mv workflow_steps.go workflow/steps.go
git mv workflow_test.go workflow/workflow_test.go
git mv workflow_definition_test.go workflow/definition_test.go
git mv workflow_exec_test.go workflow/exec_test.go
git mv workflow_steps_test.go workflow/steps_test.go
```

- [ ] **Step 2: Update package declarations**

For each moved file:
- `package oasis` → `package workflow`
- Add `import "github.com/nevindra/oasis/core"` where needed
- Replace internal references to migrated types with `core.X`

- [ ] **Step 3: Find and update root callers**

```bash
cd /home/nezhifi/Code/LLM/oasis && grep -rln 'Workflow\|WorkflowDefinition\|NodeDefinition\|DefinitionRegistry' *.go --include='*.go'
```

For each remaining root caller (likely `agent.go`), add `import "github.com/nevindra/oasis/workflow"` and update references.

- [ ] **Step 4: Create `workflow/doc.go`**

```go
// Package workflow provides DAG-based agent orchestration. A Workflow
// composes multiple agents into a directed graph of steps; it satisfies
// the core.Agent interface so workflows can be nested or used wherever
// an Agent is expected.
//
// Use NewWorkflow to construct a workflow; add steps via WithSteps.
// See example_test.go for a complete minimal example.
package workflow
```

- [ ] **Step 5: Create `workflow/example_test.go`**

Write a minimal runnable example that constructs a 2-step workflow. Reference the actual exported API discovered during Step 1.

- [ ] **Step 6: Add depguard rule**

```yaml
      workflow:
        files:
          - "workflow/**/*.go"
        deny:
          - pkg: "github.com/nevindra/oasis$"
            desc: "workflow must not import the root oasis package"
          - pkg: "github.com/nevindra/oasis/agent"
            desc: "workflow must not depend on agent — composes via core.Agent interface"
```

- [ ] **Step 7: Add re-exports**

Append to `/home/nezhifi/Code/LLM/oasis/oasis.go`:

```go
import (
    // ...
    "github.com/nevindra/oasis/workflow"
)

// --- Workflow ---
type Workflow = workflow.Workflow
var NewWorkflow = workflow.NewWorkflow
// Add WithSteps, WithRetry, etc. as appropriate.
```

- [ ] **Step 8: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
golangci-lint run ./workflow/...
```

---

### Task 2.3: Move `network.go` to `network/` **[PARALLEL with 2.1, 2.2, 2.4]**

**Files:**
- `network.go` (7.8K) → `network/network.go`
- `network_test.go` (3.5K) → `network/network_test.go`

- [ ] **Step 1: Move files**

```bash
cd /home/nezhifi/Code/LLM/oasis
mkdir -p network
git mv network.go network/network.go
git mv network_test.go network/network_test.go
```

- [ ] **Step 2: Update package and imports**

- `package oasis` → `package network`
- Replace `Provider`, `Message`, `Agent`, etc. with `core.X`

- [ ] **Step 3: Update root callers**

```bash
grep -rln 'Network\b' /home/nezhifi/Code/LLM/oasis/*.go
```

Add `import "github.com/nevindra/oasis/network"` to root callers; update references.

- [ ] **Step 4: Create `network/doc.go`**

```go
// Package network composes multiple agents into a peer network with
// configurable routing. A Network satisfies core.Agent so it can be used
// anywhere an Agent is expected.
package network
```

- [ ] **Step 5: Create `network/example_test.go`** with a minimal Network construction example.

- [ ] **Step 6: Add depguard rule for `network/` (analogous to workflow's).**

- [ ] **Step 7: Add re-exports to `oasis.go`:**

```go
type Network = network.Network
var NewNetwork = network.NewNetwork
```

- [ ] **Step 8: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
golangci-lint run ./network/...
```

---

### Task 2.4: Move `skill*.go` to `skills/` **[PARALLEL with 2.1, 2.2, 2.3]**

**Files:**
- `skill.go` (13.1K) → `skills/skill.go`
- `skill_builtin.go` (2.6K) → `skills/builtin.go`
- `skill_scan.go` (614B) → `skills/scan.go`
- `skill_tool.go` (8.9K) → `skills/tool.go`
- `skill_test.go` (19.6K) → `skills/skill_test.go`

NOTE: `skills/` directory currently exists with ONLY asset subdirectories (`oasis-design-system/`, `oasis-docx/`, etc.) — no Go code. Adding Go files at `skills/*.go` is safe and turns the directory into a true subpackage.

- [ ] **Step 1: Move source files**

```bash
cd /home/nezhifi/Code/LLM/oasis
git mv skill.go skills/skill.go
git mv skill_builtin.go skills/builtin.go
git mv skill_scan.go skills/scan.go
git mv skill_tool.go skills/tool.go
git mv skill_test.go skills/skill_test.go
```

- [ ] **Step 2: Update packages and imports**

- `package oasis` → `package skills`
- Replace core type references with `core.X`

Note that `skills/` will now contain BOTH Go files (`skill.go`, `builtin.go`, etc.) AND asset directories (`oasis-design-system/`, `oasis-docx/`, …). The asset dirs are not Go packages — they're embedded via `embed.FS`. Confirm the existing `embed.FS` directives in the moved skill files use paths relative to `skills/` correctly (likely no change needed since they're now in the same dir as the assets).

- [ ] **Step 3: Update root callers**

```bash
grep -rln 'SkillProvider\|Skill\b\|skillScanner\|ScanSkills' /home/nezhifi/Code/LLM/oasis/*.go
```

Add `import "github.com/nevindra/oasis/skills"` where needed; update references.

- [ ] **Step 4: Create `skills/doc.go`**

```go
// Package skills loads "skill" instructions — markdown files with YAML
// frontmatter that define agent capabilities discoverable at runtime.
//
// Use NewProvider to load skills from a directory; use Provider.Tool() to
// expose the skill-loading capability as an AnyTool the agent loop can
// dispatch.
package skills
```

- [ ] **Step 5: Create `skills/example_test.go`** with a minimal skill-loading example.

- [ ] **Step 6: Add depguard rule for `skills/`.**

- [ ] **Step 7: Add re-exports:**

```go
import (
    // ...
    "github.com/nevindra/oasis/skills"
)

// --- Skills ---
type SkillProvider = skills.Provider
var NewSkillProvider = skills.NewProvider
```

- [ ] **Step 8: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```

---

### Task 2.5: Move `agent.go`, `agentcore.go`, `llmagent.go`, `handle.go` to `agent/` (SEQUENTIAL — last)

**Goal:** Move the biggest, most-connected files. Must happen after 2.1–2.4 because `agent.go` imports memory, workflow, network, skills — those need to be in their final locations first so import paths are stable.

**Files:**
- `agent.go` (28.7K) → `agent/agent.go`
- `agentcore.go` (12.9K) → `agent/core.go` (NOTE: rename to avoid clash with `core` package — `agent/core.go` is in package `agent`, the file name `core.go` is fine since file names don't conflict across packages)
- `llmagent.go` (15.3K) → `agent/llm.go`
- `handle.go` (5.4K) → `agent/handle.go`
- Test files (`agent_test.go`, `agentcore_test.go`, `handle_test.go`, `spawn_test.go`)
- `retry.go` (8.8K) → `agent/retry.go` (if it's agent-specific; otherwise to `internal/`)
- `retry_test.go` (12.0K) → `agent/retry_test.go`

- [ ] **Step 1: Determine where `retry.go`, `scheduler.go`, `tracer.go`, `ingest_checkpoint.go` belong**

```bash
head -30 /home/nezhifi/Code/LLM/oasis/retry.go /home/nezhifi/Code/LLM/oasis/scheduler.go /home/nezhifi/Code/LLM/oasis/tracer.go /home/nezhifi/Code/LLM/oasis/ingest_checkpoint.go
```

Decision rules:
- `retry.go`: if it's the agent's retry logic, → `agent/`; if it's a generic helper → keep at root or move to `internal/`
- `scheduler.go`: likely workflow/agent execution scheduling → check which uses it. Probably → `internal/runtime/` (Phase 5).
- `tracer.go`: per microkernel spec §5.4, moves to `observer/` — but observer becomes satellite in Phase 3. For Phase 2, leave at root; Phase 3 absorbs it.
- `ingest_checkpoint.go`: belongs to `ingest/` — move it in Task 3.5 (ingest satellite promotion).

- [ ] **Step 2: Move agent source files**

```bash
cd /home/nezhifi/Code/LLM/oasis
mkdir -p agent
git mv agent.go agent/agent.go
git mv agentcore.go agent/agentcore.go
git mv llmagent.go agent/llm.go
git mv handle.go agent/handle.go
git mv agent_test.go agent/agent_test.go
git mv agentcore_test.go agent/agentcore_test.go
git mv handle_test.go agent/handle_test.go
git mv spawn_test.go agent/spawn_test.go
# Move retry only if Step 1 decided it belongs here:
git mv retry.go agent/retry.go
git mv retry_test.go agent/retry_test.go
```

- [ ] **Step 3: Update package declarations and imports**

For each moved file:
- `package oasis` → `package agent`
- Replace core type refs: `Provider` → `core.Provider`, `Message` → `core.Message`, `Tool` → `core.Tool`, `Processor` → `core.Processor`, `Compactor` → `core.Compactor`, etc.
- Add imports: `github.com/nevindra/oasis/core`, plus the subpackages the agent code calls into (memory, workflow, network, skills — but only if it does so directly; the design is that agent calls `core.Processor`, not specific processor implementations, so likely NO subpackage imports beyond core).

- [ ] **Step 4: Handle the WithCompaction option**

`agent.go` currently has `WithCompaction(c Compactor, threshold float64)`. After moving:
- `WithCompaction` lives in `agent/agent.go` (or split to `agent/options.go`)
- It takes `core.Compactor` as its type
- Re-export at root: `oasis.WithCompaction = agent.WithCompaction` (already covered by Step 9 below)

- [ ] **Step 5: Update remaining root files that referenced the moved code**

After moving, the only `.go` files at root should be:
- `oasis.go` (re-export umbrella)
- `doc.go` (top-level doc)
- `types_aliases.go` (Phase 0 temp aliases — these stay until Phase 6)
- `processor_aliases.go` (Phase 0 temp)
- `loop.go`, `suspend.go`, `batch.go`, `stream.go` (move to `internal/` in Phase 5)
- `loop_test.go`, `suspend_test.go`, `stream_test.go`, `loop_bench_test.go` (move with their source)
- `testhelpers_test.go` — decide: if used by other tests at root, keep; otherwise move to `internal/` with loop tests
- `compaction_wiring_test.go` (small wiring test — likely move to `agent/` since it tests `WithCompaction`)

```bash
git mv compaction_wiring_test.go agent/compaction_wiring_test.go
```

Update its package decl.

- [ ] **Step 6: Create `agent/doc.go`**

```go
// Package agent provides the LLMAgent implementation and the Agent
// interface contract. Use NewLLMAgent to construct an agent; wire
// behavior via functional options (WithTools, WithProcessors,
// WithSystemPrompt, WithModel, WithCompaction, …).
//
// The Agent interface (defined in core) is satisfied by LLMAgent,
// workflow.Workflow, and network.Network — these can be composed
// uniformly anywhere an Agent is expected.
package agent
```

- [ ] **Step 7: Create `agent/example_test.go`**

```go
package agent_test

import (
    "context"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

func ExampleNewLLMAgent() {
    // Minimal agent setup. Replace fakeProvider with a real provider
    // (e.g. openaicompat.New(...)) in production.
    var provider core.Provider // fakeProvider{}

    a := agent.NewLLMAgent("assistant", "You are helpful.", provider,
        agent.WithSystemPrompt("Respond concisely."),
    )

    _, _ = a.Execute(context.Background(), core.Task{Input: "Hi"})
}
```

(Adjust signature to match the actual `NewLLMAgent` after the move.)

- [ ] **Step 8: Add depguard rule for `agent/`**

```yaml
      agent:
        files:
          - "agent/**/*.go"
        deny:
          - pkg: "github.com/nevindra/oasis$"
            desc: "agent must not import the root oasis package"
          # agent is allowed to depend on core only; specific subpackage
          # imports require an exception here.
```

- [ ] **Step 9: Add agent re-exports to `oasis.go`**

```go
import (
    // ...
    "github.com/nevindra/oasis/agent"
)

// --- Agent ---
type LLMAgent = agent.LLMAgent
type AgentHandle = agent.AgentHandle
type Option = agent.Option // functional option type
var NewLLMAgent = agent.NewLLMAgent
var Spawn = agent.Spawn

// --- Agent options (the v1.0-targeted set) ---
var WithTools = agent.WithTools
var WithTool = agent.WithTool
var WithProcessors = agent.WithProcessors
var WithSystemPrompt = agent.WithSystemPrompt
var WithModel = agent.WithModel
var WithMaxIterations = agent.WithMaxIterations
var WithLogger = agent.WithLogger
var WithCompaction = agent.WithCompaction
// Note: WithObserver belongs to the observer satellite — added in Phase 3.
```

(The exact set depends on what agent ends up exporting. Curate; do not auto-alias every internal option.)

- [ ] **Step 10: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && \
  go build ./... && \
  go test ./... && \
  (cd mcp && go test ./...) && \
  golangci-lint run ./...
```

Expected: all green.

---

### Task 2.6: Phase 2 verification checkpoint

- [ ] **Step 1: Confirm root .go files match expected end-of-phase set**

```bash
ls /home/nezhifi/Code/LLM/oasis/*.go
```

Expected (allowed at root after Phase 2):
- `oasis.go` (umbrella)
- `doc.go`
- `types_aliases.go` (Phase 0 transitional)
- `processor_aliases.go` (Phase 0 transitional)
- `tool_aliases.go` (Phase 0 transitional, if created)
- `loop.go`, `suspend.go`, `batch.go`, `stream.go` (move to internal/ in Phase 5)
- `tracer.go` (moves to observer/ in Phase 3)
- `scheduler.go` (decide in Phase 5)
- `ingest_checkpoint.go` (moves with ingest in Phase 3.5)
- Their `_test.go` companions

Anything else still at root → either move now or document why staying.

- [ ] **Step 2: Confirm all subpackages have `doc.go` + `example_test.go`**

```bash
for d in agent workflow network compaction guardrail ratelimit memory skills tool processor; do
  echo "=== $d ==="
  ls /home/nezhifi/Code/LLM/oasis/$d/doc.go /home/nezhifi/Code/LLM/oasis/$d/example_test.go 2>&1
done
```

Expected: all files present.

- [ ] **Step 3: Run full lint and test suite**

```bash
cd /home/nezhifi/Code/LLM/oasis && \
  golangci-lint run ./... && \
  go test ./... && \
  (cd mcp && go test ./...)
```

Expected: zero violations, all tests pass.

---

# Phase 3 — Promote heavy-dep subpackages to satellites

**Goal:** Move heavy deps (Docker SDK, OTEL, pgx, sqlite driver, PDF/DOCX readers, embedding clients) out of the root module by promoting `store/sqlite`, `store/postgres`, `provider/gemini`, `provider/openaicompat`, `observer`, `ingest`, `sandbox` into separate Go modules. This fixes the "every user gets Docker SDK" problem.

**Parallelism:** All seven promotions (3.1–3.7) are **[PARALLEL]**. Each is an independent satellite with its own `go.mod`.

**Duration estimate:** ~1 day with parallelism.

---

### Recipe: Promote a subpackage to satellite

For each subpackage `<path>` being promoted:

1. From inside the subpackage directory, run `go mod init github.com/nevindra/oasis/<path>`
2. Add `replace github.com/nevindra/oasis => ../../` (or `../` depending on nesting depth)
3. Run `go mod tidy` inside the subpackage
4. Add the path to root `go.work`'s `use` block: `go work use ./<path>`
5. Remove the satellite-specific deps from root `go.mod` (run `go mod tidy` at root)
6. Verify the subpackage builds and tests pass: `cd <path> && go test ./...`
7. Verify root still builds with deps removed: `cd <repo-root> && go build ./... && go test ./...`
8. Verify cross-module usage works (the satellite imports `core` via the replace directive)
9. If the satellite is part of the curated public API: add re-export aliases to root `oasis.go` (BUT — satellites are typically NOT re-exported per hybrid spec §4.4; users import them explicitly. Exception: `WithStore`, `WithObserver` agent options that take satellite types may be exposed.)

---

### Task 3.1: Promote `store/sqlite` to satellite **[PARALLEL with 3.2–3.7]**

**Files:**
- Create: `store/sqlite/go.mod`
- Modify: root `go.mod` (drop `modernc.org/sqlite` and its transitive `modernc.org/*` deps)
- Modify: root `go.work` (add `./store/sqlite`)

- [ ] **Step 1: Initialize the module**

```bash
cd /home/nezhifi/Code/LLM/oasis/store/sqlite
go mod init github.com/nevindra/oasis/store/sqlite
```

- [ ] **Step 2: Add replace directive**

Edit `/home/nezhifi/Code/LLM/oasis/store/sqlite/go.mod`, append:

```
replace github.com/nevindra/oasis => ../../
replace github.com/nevindra/oasis/core => ../../core
```

(The second replace handles direct `core` imports.)

- [ ] **Step 3: Update store/sqlite imports**

Inside `store/sqlite/*.go`, change references like `oasis.Store`, `oasis.Message` to either:
- `core.Store`, `core.Message` if those types live in core (add `import "github.com/nevindra/oasis/core"`)
- Leave as `oasis.X` if it's an alias (works because of replace), but prefer the direct `core` import for clarity

`Store` interface: if it's in root currently, decide whether to move to `core/` (likely yes — it's a protocol type used by many stores and the loop). If yes, move it in Phase 0 retroactively (add a task), or move now as part of Phase 3.

- [ ] **Step 4: Add to `go.work`**

```bash
cd /home/nezhifi/Code/LLM/oasis
go work use ./store/sqlite
```

- [ ] **Step 5: Tidy the satellite**

```bash
cd /home/nezhifi/Code/LLM/oasis/store/sqlite
go mod tidy
```

Expected: pulls in `modernc.org/sqlite` and its transitives.

- [ ] **Step 6: Remove `modernc.org/sqlite` from root `go.mod`**

```bash
cd /home/nezhifi/Code/LLM/oasis
go mod tidy
```

`go mod tidy` should drop sqlite if no remaining root code imports it. If it doesn't, manually remove the requires.

- [ ] **Step 7: Add `doc.go` and `example_test.go`** to `store/sqlite/` if not already present, per the DX checklist.

- [ ] **Step 8: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis/store/sqlite && go test ./...
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```

Expected: all pass; root `go.mod` no longer requires `modernc.org/sqlite`.

---

### Task 3.2: Promote `store/postgres` to satellite **[PARALLEL with 3.1, 3.3–3.7]**

Same recipe as 3.1, paths swapped to `store/postgres`. Heavy dep dropped from root: `github.com/jackc/pgx/v5` and its transitives (`puddle`, `pgservicefile`, etc.).

- [ ] **Step 1: `cd store/postgres && go mod init github.com/nevindra/oasis/store/postgres`**
- [ ] **Step 2: Add `replace github.com/nevindra/oasis => ../../` and `replace github.com/nevindra/oasis/core => ../../core`**
- [ ] **Step 3: Update imports inside `store/postgres/` to use `core` for shared types**
- [ ] **Step 4: `go work use ./store/postgres`**
- [ ] **Step 5: `cd store/postgres && go mod tidy`**
- [ ] **Step 6: `cd ../.. && go mod tidy` (root drops pgx)**
- [ ] **Step 7: Add `doc.go`, `example_test.go`**
- [ ] **Step 8: Verify build + test in satellite and root**

---

### Task 3.3: Promote `provider/gemini` to satellite **[PARALLEL with 3.1, 3.2, 3.4–3.7]**

**Files:** `provider/gemini/` (currently subpackage; raw HTTP, no SDK — light deps but per hybrid spec §4.1 isolated for provider evolution)

Same recipe. No big dep to drop (uses stdlib HTTP), but isolates provider versioning.

- [ ] Steps 1-8 per recipe; replace path `provider/gemini`.

---

### Task 3.4: Promote `provider/openaicompat` to satellite **[PARALLEL with 3.1–3.3, 3.5–3.7]**

Same recipe; replace path `provider/openaicompat`.

NOTE: `provider/` may contain shared utilities (`provider/catalog/`, `provider/resolve/`). Decide per directory:
- `provider/catalog/` — looks like a shared registry of provider metadata. If pure data, keep as subpackage of root; if it requires per-provider satellite deps, leave it where it is and let satellites import it.
- `provider/resolve/` — likely model-name resolution helpers. Same decision.

```bash
ls /home/nezhifi/Code/LLM/oasis/provider/catalog/ /home/nezhifi/Code/LLM/oasis/provider/resolve/
head -20 /home/nezhifi/Code/LLM/oasis/provider/catalog/*.go /home/nezhifi/Code/LLM/oasis/provider/resolve/*.go 2>&1
```

If they're stdlib-only: keep as root subpackages. Add depguard rules.

- [ ] Steps 1-8 per recipe for `provider/openaicompat`.

---

### Task 3.5: Promote `observer/` to satellite **[PARALLEL with 3.1–3.4, 3.6, 3.7]**

**Files:**
- `observer/` directory (currently subpackage; OTEL deps — by far the heaviest in tree)
- ALSO: move root `tracer.go` (1.7K) into `observer/tracer.go` as part of this task — it's the agent-side wrapper

- [ ] **Step 1: Move root `tracer.go` into observer**

```bash
cd /home/nezhifi/Code/LLM/oasis
git mv tracer.go observer/tracer.go
```

- [ ] **Step 2: `cd observer && go mod init github.com/nevindra/oasis/observer`**
- [ ] **Step 3: Add `replace github.com/nevindra/oasis => ../` and `replace github.com/nevindra/oasis/core => ../core`**
- [ ] **Step 4: Update observer source: `package oasis` (in moved tracer.go) → `package observer`, fix imports**
- [ ] **Step 5: `go work use ./observer`**
- [ ] **Step 6: `cd observer && go mod tidy`** — pulls in OTEL stack (~15 packages)
- [ ] **Step 7: `cd .. && go mod tidy`** — root drops all `go.opentelemetry.io/*` requires
- [ ] **Step 8: Add `doc.go`, `example_test.go`**
- [ ] **Step 9: Add depguard rule for observer (in its own .golangci.yml or root config that scans satellites)**
- [ ] **Step 10: Find root code that called observer types**

If `agent.go` (now `agent/`) had a `WithObserver(t observer.Tracer)` option, agent needs to depend on observer — but that re-couples agent to a heavy dep. Resolution:
- Define `Tracer` interface in `core/observer.go` (a minimal contract — just the methods the agent loop calls)
- agent's `WithObserver` takes `core.Tracer`
- observer satellite implements `core.Tracer`
- Users wire: `agent.NewLLMAgent(..., agent.WithObserver(observer.NewTracer(...)))`

This is the cleanest separation. Add the `Tracer` interface to `core/` and update agent.

- [ ] **Step 11: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis/observer && go test ./...
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```

Expected: root `go.mod` has no `go.opentelemetry.io/*` entries.

---

### Task 3.6: Promote `ingest/` to satellite **[PARALLEL with 3.1–3.5, 3.7]**

**Files:**
- `ingest/` directory (PDF, DOCX, CSV readers, chunkers, embedding clients)
- Move root `ingest_checkpoint.go` into `ingest/checkpoint.go` (per file already exists in ingest/ as `checkpoint.go` — verify and decide whether to merge or rename)

- [ ] **Step 1: Move root `ingest_checkpoint.go`**

```bash
cd /home/nezhifi/Code/LLM/oasis
# First check if ingest/checkpoint.go already exists:
ls ingest/checkpoint.go 2>&1
```

If it does: rename the root file before moving to avoid collision:
```bash
git mv ingest_checkpoint.go ingest/root_checkpoint.go
```

Otherwise:
```bash
git mv ingest_checkpoint.go ingest/checkpoint.go
```

Update package decl, fix imports.

- [ ] **Step 2: `cd ingest && go mod init github.com/nevindra/oasis/ingest`**
- [ ] **Step 3: Add `replace github.com/nevindra/oasis => ../` and `replace github.com/nevindra/oasis/core => ../core`**
- [ ] **Step 4: Fix imports inside `ingest/`**
- [ ] **Step 5: `go work use ./ingest`**
- [ ] **Step 6: `cd ingest && go mod tidy`** — pulls in PDF, DOCX, embedding clients
- [ ] **Step 7: `cd .. && go mod tidy`** — root drops `github.com/ledongthuc/pdf`, etc.
- [ ] **Step 8: Add `doc.go`, `example_test.go`**
- [ ] **Step 9: Verify**

---

### Task 3.7: Promote `sandbox/` to satellite **[PARALLEL with 3.1–3.6]**

**Files:** `sandbox/` (Docker SDK)

- [ ] **Step 1: `cd sandbox && go mod init github.com/nevindra/oasis/sandbox`**
- [ ] **Step 2: Add `replace github.com/nevindra/oasis => ../` and `replace github.com/nevindra/oasis/core => ../core`**
- [ ] **Step 3: Fix imports inside `sandbox/`**
- [ ] **Step 4: `go work use ./sandbox`**
- [ ] **Step 5: `cd sandbox && go mod tidy`** — pulls in Docker SDK + containerd/moby ecosystem
- [ ] **Step 6: `cd .. && go mod tidy`** — root drops `github.com/docker/docker`, `github.com/docker/go-connections`, `github.com/Microsoft/go-winio`, etc.
- [ ] **Step 7: Add `doc.go`, `example_test.go`**
- [ ] **Step 8: Verify**

---

### Task 3.8: Phase 3 verification checkpoint

- [ ] **Step 1: Verify root `go.mod` deps shrunk dramatically**

```bash
cd /home/nezhifi/Code/LLM/oasis && grep -E '^\s+github\.com|^\s+go\.opentelemetry\.io|^\s+modernc\.org' go.mod
```

Expected: NO matches for any of:
- `modernc.org/sqlite` (in `store/sqlite`)
- `github.com/jackc/pgx` (in `store/postgres`)
- `go.opentelemetry.io/*` (in `observer`)
- `github.com/docker/*` (in `sandbox`)
- `github.com/ledongthuc/pdf` (in `ingest`)
- `github.com/go-shiori/go-readability` (in `ingest`)

Allowed root deps: `github.com/google/uuid` (lightweight), stdlib-equivalents.

- [ ] **Step 2: Verify all satellites build and test independently**

```bash
for sat in mcp store/sqlite store/postgres provider/gemini provider/openaicompat observer ingest sandbox; do
  echo "=== $sat ==="
  (cd /home/nezhifi/Code/LLM/oasis/$sat && go test ./...) || echo "FAIL: $sat"
done
```

Expected: all pass.

- [ ] **Step 3: Workspace-wide test**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```

- [ ] **Step 4: Verify `go.work`**

```bash
cat /home/nezhifi/Code/LLM/oasis/go.work
```

Expected:
```
go 1.26.1

use (
    .
    ./mcp
    ./store/sqlite
    ./store/postgres
    ./provider/gemini
    ./provider/openaicompat
    ./observer
    ./ingest
    ./sandbox
)
```

---

# Phase 4 — Extract `rag/` as new satellite

**Goal:** Move `retriever.go` (31.3K) and `cosine.go` (578B) from root into a new `rag/` satellite. These together are the retrieval subsystem — embedding clients, vector math, hybrid retrievers, rerankers.

**Parallelism:** Sequential (single satellite).

**Duration estimate:** ~half day.

---

### Task 4.1: Create `rag/` satellite scaffold

- [ ] **Step 1: Create the directory**

```bash
mkdir -p /home/nezhifi/Code/LLM/oasis/rag
```

- [ ] **Step 2: Move source files**

```bash
cd /home/nezhifi/Code/LLM/oasis
git mv retriever.go rag/retriever.go
git mv cosine.go rag/cosine.go
git mv retriever_test.go rag/retriever_test.go
```

- [ ] **Step 3: Update package decl**

In each moved file: `package oasis` → `package rag`. Replace internal type references with `core.X` (especially `core.Message`, `core.EmbeddingProvider`, `core.Document` or similar).

- [ ] **Step 4: Initialize the module**

```bash
cd /home/nezhifi/Code/LLM/oasis/rag
go mod init github.com/nevindra/oasis/rag
```

Append replace directives:
```
replace github.com/nevindra/oasis => ../
replace github.com/nevindra/oasis/core => ../core
```

- [ ] **Step 5: `cd /home/nezhifi/Code/LLM/oasis && go work use ./rag`**

- [ ] **Step 6: `cd rag && go mod tidy`**

If `retriever.go` referenced embedding clients (e.g. shared with `ingest/` or `provider/`), the import will pull them in. If circular (rag depends on a satellite that depends on rag) — break by lifting shared interfaces to `core/`.

- [ ] **Step 7: Create `rag/doc.go`**

```go
// Package rag provides retrieval-augmented generation primitives: a
// Retriever interface, vector cosine similarity, reranking, and hybrid
// retrieval composing dense and sparse signals.
//
// Retrievers are typically wired into an agent as a core.Processor that
// injects relevant context into the message history before the LLM call.
package rag
```

- [ ] **Step 8: Create `rag/example_test.go`**

Minimal example showing constructing a Retriever and calling its main method.

- [ ] **Step 9: Update root callers**

```bash
grep -rln 'Retriever\|HybridRetriever\|Reranker\|Cosine' /home/nezhifi/Code/LLM/oasis/*.go /home/nezhifi/Code/LLM/oasis/agent/*.go
```

For each remaining caller: add `import "github.com/nevindra/oasis/rag"` and update references. The agent shouldn't directly depend on rag (rag is opt-in) — if `agent.go` has a `WithRetriever` option, decide whether to:
- Keep it as `agent.WithRetriever(core.Retriever)` with the interface in core (preferred — minimal core surface, decoupled)
- Or remove and require users to wire via `WithProcessors(rag.AsProcessor(retriever))`

The hybrid spec §6.9 puts rag in the "lives in satellite module" column. Don't add a `WithRetriever` agent option; users compose explicitly. If one exists currently, deprecate/remove.

- [ ] **Step 10: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis/rag && go test ./...
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```

---

# Phase 5 — Move runtime guts to `internal/`

**Goal:** `loop.go`, `suspend.go`, `batch.go`, `stream.go`, and `scheduler.go` are runtime mechanics — not protocol types, not user-facing primitives. Hide them in `internal/runtime/` so Go's hard-enforced privacy (anything under `internal/` is unreachable from outside the parent module) prevents accidental external coupling.

**Parallelism:** Sequential within phase.

**Duration estimate:** ~3-4 hours.

---

### Task 5.1: Create `internal/runtime/` and move loop + suspend + batch + stream

**Files:**
- `loop.go` (30.5K) → `internal/runtime/loop.go`
- `loop_test.go` (24.7K) → `internal/runtime/loop_test.go`
- `loop_bench_test.go` (2.0K) → `internal/runtime/loop_bench_test.go`
- `suspend.go` (11.8K) → `internal/runtime/suspend.go`
- `suspend_test.go` (26.1K) → `internal/runtime/suspend_test.go`
- `batch.go` (2.7K) → `internal/runtime/batch.go`
- `stream.go` (7.4K) → `internal/runtime/stream.go`
- `stream_test.go` (33.1K) → `internal/runtime/stream_test.go`
- `testhelpers_test.go` (6.2K) → `internal/runtime/testhelpers_test.go` (or split — agent/ may also want some)

- [ ] **Step 1: Create directory**

```bash
mkdir -p /home/nezhifi/Code/LLM/oasis/internal/runtime
```

- [ ] **Step 2: Move files**

```bash
cd /home/nezhifi/Code/LLM/oasis
git mv loop.go internal/runtime/loop.go
git mv loop_test.go internal/runtime/loop_test.go
git mv loop_bench_test.go internal/runtime/loop_bench_test.go
git mv suspend.go internal/runtime/suspend.go
git mv suspend_test.go internal/runtime/suspend_test.go
git mv batch.go internal/runtime/batch.go
git mv stream.go internal/runtime/stream.go
git mv stream_test.go internal/runtime/stream_test.go
git mv testhelpers_test.go internal/runtime/testhelpers_test.go
```

- [ ] **Step 3: Update package declarations**

For each moved `.go` file: `package oasis` → `package runtime`. Update imports — likely heavy: `core.Message`, `core.Provider`, `core.Tool`, `core.AnyTool`, `core.Processor`, `core.StreamEvent`, etc.

The loop calls into Provider, iterates Tools, runs Processors — all from `core`. The loop is `agent`-facing: `agent.NewLLMAgent` will construct a `runtime.Loop` (or similar) internally and call into it.

- [ ] **Step 4: Update `agent/` to consume `internal/runtime`**

`agent/agent.go` (or `agent/llm.go`) wires the loop. After this move, `agent/` imports `github.com/nevindra/oasis/internal/runtime` and calls `runtime.New(...)` or equivalent.

This is INTRA-MODULE — `agent/` and `internal/runtime/` are in the same root module, so the `internal/` rule allows the import (only forbidden from outside-module callers).

Note: this means satellites CANNOT directly import `internal/runtime` (Go enforces). If a satellite legitimately needs runtime mechanics, that signals the design has a leak — investigate before adding a workaround.

- [ ] **Step 5: Move `scheduler.go` too if it's runtime mechanics**

```bash
head -30 /home/nezhifi/Code/LLM/oasis/scheduler.go
```

If it's agent execution scheduling (e.g. concurrency control, work-stealing), → `internal/runtime/scheduler.go`. If it's user-facing API (e.g. cron-like scheduled agent runs), → `agent/scheduler.go` or its own subpackage.

Decision: most likely runtime. Move:
```bash
git mv scheduler.go internal/runtime/scheduler.go
git mv scheduler_test.go internal/runtime/scheduler_test.go
```

Update package decl.

- [ ] **Step 6: Add `internal/runtime/doc.go`**

```go
// Package runtime contains the agent execution loop, suspend/resume,
// batch primitives, and streaming infrastructure.
//
// This is an internal package: it is consumed by oasis/agent and may not
// be imported from outside the github.com/nevindra/oasis module.
package runtime
```

- [ ] **Step 7: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```

- [ ] **Step 8: Confirm no satellite reaches into internal/**

```bash
for sat in mcp store/sqlite store/postgres provider/gemini provider/openaicompat observer ingest sandbox rag; do
  (cd /home/nezhifi/Code/LLM/oasis/$sat && grep -rn 'oasis/internal' . --include='*.go') && echo "VIOLATION: $sat"
done
```

Expected: no violations. Go's `internal/` rule should prevent this from compiling anyway, but verify.

---

# Phase 6 — Cleanup, docs, final audit

**Goal:** Remove transitional alias files, finalize re-exports, update PHILOSOPHY.md and CLAUDE.md, write CHANGELOG entry, single commit.

**Parallelism:** Tasks 6.1–6.4 are **[PARALLEL]**. Task 6.5 (commit) sequential at end.

**Duration estimate:** ~4-6 hours.

---

### Task 6.1: Remove transitional alias files **[PARALLEL with 6.2, 6.3, 6.4]**

**Files:**
- Delete: `types_aliases.go`, `processor_aliases.go`, `tool_aliases.go` (if exists)

The Phase 0 transitional aliases existed to keep root callers compiling during the migration. After Phase 2, root has no callers except `oasis.go` (re-exports). The aliases in `types_aliases.go` likely overlap with what `oasis.go` re-exports — clean up.

- [ ] **Step 1: Audit overlap**

```bash
grep -E '^type|^var|^const' /home/nezhifi/Code/LLM/oasis/types_aliases.go /home/nezhifi/Code/LLM/oasis/processor_aliases.go 2>&1
```

For each alias, decide:
- It's something users want from `oasis.*` → MOVE the alias declaration into `oasis.go`, then delete from `*_aliases.go`
- It's internal-only → DELETE outright (no users)

Hybrid spec §6 lists the v1.0-targeted public surface: Provider, Message, ChatRequest, ChatResponse, ToolCall, ToolResult, ToolSchema, Task, Result, Usage, StreamEvent + its variants, Suspended, Tool[In,Out], AnyTool, Processor, Agent (interface), LLMAgent, AgentHandle, ProcessorChain. Pick from this list.

- [ ] **Step 2: Consolidate into `oasis.go`**

`oasis.go` should grow a section like:

```go
// --- Protocol types (re-exported from core) ---

type Message = core.Message
type MessageRole = core.MessageRole
type ChatRequest = core.ChatRequest
type ChatResponse = core.ChatResponse
type ToolCall = core.ToolCall
type ToolResult = core.ToolResult
type ToolSchema = core.ToolSchema
type Task = core.Task
type Result = core.Result
type Usage = core.Usage
type StreamEvent = core.StreamEvent
type TextDelta = core.TextDelta
type ToolCallStart = core.ToolCallStart
type ToolCallEnd = core.ToolCallEnd
type Done = core.Done
type Suspended = core.Suspended

// --- Core interfaces (re-exported from core) ---

type Provider = core.Provider
type StreamingProvider = core.StreamingProvider
type EmbeddingProvider = core.EmbeddingProvider
type AnyTool = core.AnyTool
type Processor = core.Processor
type Agent = core.Agent
type Compactor = core.Compactor

// Tool is generic — Go does not allow generic type aliases at the
// package level. Users wanting the type-safe form import core directly:
//   import "github.com/nevindra/oasis/core"
//   var t core.Tool[In, Out] = ...
// Or use Erase from the tool subpackage to adapt to AnyTool.
```

- [ ] **Step 3: Delete transitional files**

```bash
cd /home/nezhifi/Code/LLM/oasis
rm types_aliases.go processor_aliases.go
[ -f tool_aliases.go ] && rm tool_aliases.go
```

- [ ] **Step 4: Verify**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./... && go test ./...
```

---

### Task 6.2: Final root `go.mod` audit **[PARALLEL with 6.1, 6.3, 6.4]**

- [ ] **Step 1: Inspect root deps**

```bash
cat /home/nezhifi/Code/LLM/oasis/go.mod
```

Expected after Phase 3:
- `github.com/google/uuid` (lightweight UUID generation)
- Maybe stdlib log/slog handlers if used
- NO heavy deps (no Docker, no OTEL, no DB drivers, no PDF/DOCX, no embedding clients)

- [ ] **Step 2: Run `go mod tidy`**

```bash
cd /home/nezhifi/Code/LLM/oasis && go mod tidy
```

- [ ] **Step 3: Verify final state**

The `require` block should be minimal — under 5 direct deps if possible. If anything heavy remains, trace why:

```bash
go mod why <package>
```

Address by moving the importer to the appropriate satellite, or accepting the dep with a justification comment.

---

### Task 6.3: Update PHILOSOPHY.md and CLAUDE.md **[PARALLEL with 6.1, 6.2, 6.4]**

**Files:**
- Modify: `docs/PHILOSOPHY.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update PHILOSOPHY.md**

Read `docs/PHILOSOPHY.md`. Per hybrid spec H12, drop any "microkernel" framing. Replace with "clean monolith for primitives + satellites for heavy deps" language.

Key edits expected:
- Any "microkernel" mention → "hybrid architecture"
- Any "core imports nothing" claim → "core types in oasis/core leaf; satellites for opt-out dep weight"
- "Curated batteries" framing stays (per H9), but battery = subpackage, not separate module

If the doc has a structural-overview section, replace it with the architecture-summary from this plan.

- [ ] **Step 2: Update CLAUDE.md project structure block**

The current CLAUDE.md project structure block describes pre-migration state. Update to match new layout:

```markdown
## Project Structure

oasis/                              # FRAMEWORK
|-- oasis.go                        # Re-export umbrella (public surface)
|-- doc.go                          # Top-level getting-started
|
|-- core/                           # Protocol types + interfaces (leaf package)
|-- agent/                          # LLMAgent + Spawn + functional options
|-- workflow/                       # DAG-based orchestration
|-- network/                        # Multi-agent peer networks
|-- compaction/                     # Compaction processors
|-- guardrail/                      # Guardrail processors
|-- ratelimit/                      # Rate limiter wrapper
|-- memory/                         # Memory orchestration
|-- skills/                         # Skill loader + asset embedding
|-- processor/                      # ProcessorChain helper
|-- tool/                           # Erase helper for type-safe tools
|
|-- internal/runtime/               # Agent loop, suspend/resume, batch, stream
|
|-- (satellites — each its own go.mod)
|   |-- mcp/                        # MCP client integration
|   |-- store/{sqlite,postgres}/    # Storage backends
|   |-- provider/{gemini,openaicompat}/  # LLM providers
|   |-- observer/                   # OTEL observability
|   |-- ingest/                     # Document ingestion (PDF, DOCX, embeddings)
|   |-- sandbox/                    # Docker-based code sandbox
|   |-- rag/                        # Retrieval-augmented generation
|
|-- tools/{...}/                    # Tool implementations (per CLAUDE.md current)
|-- cmd/                            # CLI tools (mcp-docs, modelgen, ix)
```

Add a note: *"`cmd/bot_example` was removed during the migration (per microkernel spec §D13); hybrid spec H10 recommends a reference app but does NOT recreate it. Add separately if desired."*

- [ ] **Step 3: Verify docs still resolve internal links**

```bash
cd /home/nezhifi/Code/LLM/oasis && grep -rn '\](docs/' docs/ CLAUDE.md README.md 2>&1 | head -20
```

Fix any broken links pointing to moved files.

---

### Task 6.4: CHANGELOG entry **[PARALLEL with 6.1, 6.2, 6.3]**

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add `[Unreleased]` section with the migration**

Edit `/home/nezhifi/Code/LLM/oasis/CHANGELOG.md`, under `[Unreleased]`:

```markdown
### Changed

- **BREAKING**: Restructured the repository into a hybrid architecture per `docs/superpowers/specs/2026-05-18-hybrid-architecture-design.md`. Highlights:
  - Protocol types and core interfaces moved to new leaf package `github.com/nevindra/oasis/core`.
  - Primitives reorganized into public subpackages: `agent`, `workflow`, `network`, `compaction`, `guardrail`, `ratelimit`, `memory`, `skills`, `processor`, `tool`.
  - Heavy/optional-dep code extracted as satellites with their own `go.mod`: `store/sqlite`, `store/postgres`, `provider/gemini`, `provider/openaicompat`, `observer`, `ingest`, `sandbox`, `rag` (plus existing `mcp`). Each is opt-in — adding `import "github.com/nevindra/oasis/store/sqlite"` is the only way to pull in the sqlite driver.
  - Runtime mechanics (`loop`, `suspend`, `batch`, `stream`) moved to `internal/runtime/` (Go-enforced privacy).
  - Root `oasis` package is now a curated re-export umbrella: most users continue with `import "github.com/nevindra/oasis"` and get the common API via type aliases.
  - The earlier extracted satellites `ratelimit`, `guardrail`, and `compaction` have been demoted back to subpackages: their import paths are unchanged (`github.com/nevindra/oasis/ratelimit` etc.), but they no longer have their own `go.mod`.

### Migration notes

- Root `go.mod` no longer requires `modernc.org/sqlite`, `github.com/jackc/pgx/v5`, the OTEL stack, the Docker SDK, or PDF/DOCX readers. Apps that previously got those transitively must now explicitly import the corresponding satellite.
- All re-exported types/functions from `oasis.*` are unchanged in name. If your code imported `oasis.WithCompaction`, it still works.
- Direct imports of the satellites (`oasis/store/sqlite`, etc.) are unchanged.

### Removed

- The pre-migration root-package types declared in `types.go` are gone — use the type aliases at the umbrella package (`oasis.Message`, `oasis.Provider`, …) or import `oasis/core` directly.
```

- [ ] **Step 2: Verify formatting**

```bash
head -50 /home/nezhifi/Code/LLM/oasis/CHANGELOG.md
```

---

### Task 6.5: Final verification and single commit

- [ ] **Step 1: Workspace-wide build and test**

```bash
cd /home/nezhifi/Code/LLM/oasis && \
  go build ./... && \
  go test ./...

for sat in mcp store/sqlite store/postgres provider/gemini provider/openaicompat observer ingest sandbox rag; do
  echo "=== $sat ==="
  (cd /home/nezhifi/Code/LLM/oasis/$sat && go build ./... && go test ./...) || { echo "FAIL: $sat"; exit 1; }
done
```

Expected: all green.

- [ ] **Step 2: Lint pass**

```bash
cd /home/nezhifi/Code/LLM/oasis && golangci-lint run ./...
```

Expected: zero violations.

- [ ] **Step 3: Confirm acceptance criteria from spec §11**

Tick each:

```bash
# (1) Root go.mod minimal
grep -c 'go.opentelemetry\|modernc.org/sqlite\|github.com/jackc/pgx\|github.com/docker\|github.com/ledongthuc/pdf' go.mod || echo "ok"

# (2) Single-import primitives
go doc github.com/nevindra/oasis | head -50

# (3) Per-subpackage doc.go + example_test.go
for d in agent workflow network compaction guardrail ratelimit memory skills tool processor; do
  ls $d/doc.go $d/example_test.go 2>&1
done

# (4) depguard enforcement
golangci-lint run ./...

# (5) Satellites
find . -maxdepth 3 -name go.mod | sort
```

- [ ] **Step 4: Confirm prior spec is marked superseded**

The 2026-05-17 microkernel spec header already says "Superseded: see hybrid-architecture-design.md" — verify it does. If not, add the note.

```bash
head -10 /home/nezhifi/Code/LLM/oasis/docs/superpowers/specs/2026-05-17-microkernel-migration-design.md
```

If not marked, edit the header to add `**Superseded by:** [2026-05-18-hybrid-architecture-design.md]`.

- [ ] **Step 5: Stage everything and commit**

```bash
cd /home/nezhifi/Code/LLM/oasis

git status

# Stage all changes (the agent should review `git status` output and confirm
# the file list looks right — moved files should appear as renames):
git add -A

git commit -m "$(cat <<'EOF'
refactor!: migrate to hybrid architecture (subpackages + heavy-dep satellites)

Restructure the framework per docs/superpowers/specs/2026-05-18-hybrid-architecture-design.md:

- New core/ leaf package owns protocol types and interfaces (Provider,
  Tool, AnyTool, Processor, Compactor, Message, ChatRequest, etc.)
- Primitives split into focused public subpackages: agent, workflow,
  network, compaction, guardrail, ratelimit, memory, skills, processor,
  tool
- Heavy/optional-dep code extracted as satellites (own go.mod):
  store/sqlite, store/postgres, provider/gemini, provider/openaicompat,
  observer, ingest, sandbox, rag (joining the existing mcp satellite)
- Runtime mechanics (loop, suspend, batch, stream, scheduler) hidden in
  internal/runtime (Go-enforced privacy)
- oasis.go is now a curated re-export umbrella so the 80% case stays
  `import "github.com/nevindra/oasis"`
- ratelimit, guardrail, compaction demoted back from satellite to
  subpackage — their stdlib-only deps did not justify satellite cost
- Cross-subpackage boundaries enforced by golangci-lint depguard

Root go.mod now depends on stdlib + lightweight helpers only (uuid, etc).
Users opt in to heavy deps by importing the relevant satellite.

Refs: docs/superpowers/specs/2026-05-18-hybrid-architecture-design.md
EOF
)"
```

- [ ] **Step 6: Tag the release candidate (do NOT push)**

```bash
cd /home/nezhifi/Code/LLM/oasis
git tag v0.17.0-rc.1
echo "Tagged v0.17.0-rc.1. Push when ready: git push origin migration/microkernel v0.17.0-rc.1"
```

(The branch is still called `migration/microkernel` from the prior plan — consider renaming to `migration/hybrid` before pushing, or rename in a follow-up PR.)

- [ ] **Step 7: Confirm status clean**

```bash
git status
```

Expected: `nothing to commit, working tree clean`.

---

# Self-review checklist (run after writing)

The author of this plan should walk through these checks before handing off:

1. **Spec coverage:** Every section in `2026-05-18-hybrid-architecture-design.md` maps to at least one task here:
   - §4.1 litmus test → applied in Phase 3 (only heavy-dep things become satellites)
   - §4.2 DX checklist → applied per subpackage in Phases 1–5
   - §4.3 depguard → Phase 1.6 + extended per subpackage move
   - §4.4 re-export → Phase 1.5 + extended per subpackage
   - §4.5 internal/ → Phase 5
   - §5.1 final layout → end-state after Phase 6
   - §8.1 file map → mirrored in this plan's File Reorganization Map
   - §8.2 migration sequence → Phases A→E mapped to Phases 1→6 (with new Phase 0 added for the foundation work the spec under-specified)
   - §8.3 in-flight work → Phase 1 demote handles it
   - §9 (decision register) → applied as guidance throughout
   - §11 acceptance criteria → checked in Task 6.5 Step 3
   - §10 open items → resolved in plan header (core/ choice) or deferred (bot_example, skills weight measure, bundle-helper pattern)

2. **Placeholder scan:** No "TBD", "implement later", or "similar to Task N" patterns. Some `head -20 /path/to/file` discovery commands appear — these are explicit inventory steps before code writing, not placeholders.

3. **Type consistency:** Type names used consistently across tasks:
   - `core.Provider`, `core.Processor`, `core.Tool[In,Out]`, `core.AnyTool`, `core.Compactor` — these names appear in Phase 0 declarations and are referenced in every later phase.
   - Subpackage names (`agent.LLMAgent`, `workflow.Workflow`, `network.Network`, etc.) used consistently.
   - Re-export pattern (`var X = pkg.X` for funcs, `type X = pkg.X` for types) used consistently.

4. **Known gaps acknowledged in the plan:**
   - `cmd/bot_example` not recreated (per hybrid spec H10 it should be, but it was deleted under microkernel plan — deferred to follow-up).
   - `Store` interface migration: Task 3.1 notes it should move to `core/`; if it currently lives at root in `types.go`, Phase 0 Task 0.2 catches it; if it lives in `store/` already, no action needed. The plan trusts the executing agent to verify and act.
   - Generic `Tool[In, Out]` re-export at root not possible — documented in Task 6.1 with a clear note for users on how to access the generic form.

---

# Plan complete

This plan covers Phase 0 (foundation) through Phase 6 (cleanup) of the hybrid architecture migration. Estimated AI-native execution time: **3-5 days** with parallel subagent dispatch where marked.

**Final state:**
- 1 root module (`oasis`) with minimal deps + 9 satellites with their own `go.mod`
- Curated re-export surface at root preserving the one-import DX
- `core/` leaf package owns shared types
- `internal/runtime/` hides loop mechanics
- depguard-enforced cross-subpackage boundaries
- All tests pass; root `go.mod` no longer pulls Docker SDK, OTEL, sqlite, pgx, PDF readers, etc.

---
