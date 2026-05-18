# Hybrid Architecture Design

**Status:** Draft for review
**Date:** 2026-05-18
**Author:** nevindra
**Supersedes:** [2026-05-17-microkernel-migration-design.md](./2026-05-17-microkernel-migration-design.md) — microkernel approach abandoned after benchmark against Mastra and user/implementor DX analysis.

---

## 1. Context

The 2026-05-17 microkernel design proposed splitting Oasis into ~19 independently-versioned Go modules with a strict "core imports nothing under `oasis/*`" CI rule. After stress-testing the user/implementor DX implications and benchmarking against Mastra (24K stars, batteries-included core + pluggable backends), we concluded the microkernel optimizes for maintainer purity at significant user-DX cost without delivering benefits users actually experience.

**Mastra's empirical data point.** `@mastra/core` (v1.36) bundles agents, workflows, memory, MCP, processors, evals, voice, browser, server, observability — ~40 sub-folders in one package. They modularize only what carries unavoidable cost: 24 separate storage backends, cloud deployers, and integration packages. This is the validated shape for an opinionated agent framework with traction.

**The real problems Oasis has** (per the prior spec, accurate diagnosis even though the prescription was wrong):

1. Hundreds of accidental exports in the root package.
2. Root `go.mod` carries Docker SDK, OTEL full stack, pgx, sqlite, PDF reader, etc. — every user pays for everything regardless of what they import.
3. Recent absorptions (MCP, todo tooling, sandbox bindings, skill v2, deferred tool schemas) pushed core toward "Go re-implementation of one specific agent harness" rather than a general framework.
4. No enforced DX discipline per concern.

The hybrid architecture fixes all four without paying the microkernel tax (multi-module coordination, fragmented user imports, version compatibility matrix across 19 modules, per-extraction ceremony, lost one-liner ergonomics).

**Core insight driving this revision:** what the implementor enjoys about per-module isolation is **folder-level focus** with **clear boundaries**, not separate `go.mod` files. Go subpackages provide the first; `golangci-lint depguard` provides the second. Separate modules add coordination cost without adding the things implementors actually value.

---

## 2. Goals

1. **Lean user-side deps.** The base `import "github.com/nevindra/oasis"` pulls stdlib + minimal transitive deps. Heavy backends require explicit satellite import.
2. **Folder isolation for maintainers.** Each concern lives in its own subpackage with its own DX checklist (`doc.go`, `example_test.go`, focused tests, options pattern).
3. **Single import for the common path.** The 80% case is `import "github.com/nevindra/oasis"`; curated re-exports cover the discoverable API surface.
4. **Cheap cross-cutting refactor.** Primitives evolve together in one commit, not 9 PRs across 9 modules with a version-compatibility matrix.
5. **No regression in current user ergonomics.** One-liner setup paths preserved via re-export — the `oasis.NewLLMAgent(...)` shape stays.
6. **Bounded growth.** Adding a primitive = subpackage + lint rule + re-export line. Promoting to satellite reserved for genuinely heavy/optional dep weight.

---

## 3. Non-goals

- **Not extracting primitives as separate modules.** `compaction`, `guardrail`, `ratelimit`, `input`, `workflow`, `network`, `memory` (orchestration), `agent`, `processor` stay as subpackages.
- **Not per-primitive versioning.** Primitives version together with the root module. Satellites version independently because their dep churn differs.
- **Not "polyglot edge" infrastructure.** Deferred until a real driving need (per prior D3).
- **Not preserving the "kernel" framing.** This architecture is not a microkernel — it is a clean monolith for primitives with satellite extraction for heavy/optional deps. The framing in PHILOSOPHY.md needs to be updated alongside this design.
- **Not external-user migration support.** Pre-v1, no external users; break freely with CHANGELOG entries.

---

## 4. Core principles

### 4.1 The litmus test for satellite extraction

> **"Does this concern carry deps that a user choosing a different option would never need?"**

- ✅ Yes → satellite (separate `go.mod`)
- ❌ No → subpackage (in root `go.mod`)

This is concrete and defensible, replacing the fuzzy "is this a battery" test from the prior spec.

Applied:

| Concern | Dep weight | Verdict |
|---|---|---|
| `store/sqlite` | `modernc.org/sqlite` (~10MB, CGO baggage) | Satellite |
| `store/postgres` | `pgx` + ecosystem | Satellite |
| `provider/gemini` | stdlib (HTTP) — but isolates provider evolution | Satellite |
| `provider/openaicompat` | stdlib (HTTP) — same | Satellite |
| `observer` | OTEL full stack (~15 packages, heaviest dep in tree) | Satellite (critical) |
| `ingest` | PDF, DOCX, CSV extractors, chunkers, embedding clients | Satellite |
| `sandbox` | Docker SDK | Satellite |
| `rag` | embedding clients, vector libs | Satellite |
| `mcp` | JSON-RPC, transport, schema parsing | Satellite |
| `agent`, `workflow`, `network` | stdlib only | Subpackage |
| `compaction`, `guardrail`, `ratelimit` | stdlib only | Subpackage |
| `input` | stdlib only | Subpackage |
| `memory` (orchestration only) | stdlib only | Subpackage |
| `skills` (loader + asset embedding) | `embed.FS` only | Subpackage |
| `processor`, `tool`, types, `catalog` | stdlib only | Stay in root namespace or as small subpackages |

Total: **1 root module (primitives) + 9 satellites** = 10 `go.mod` files.

### 4.2 Per-subpackage DX checklist

Every public subpackage MUST satisfy:

1. ✅ `doc.go` — a real getting-started doc, not a one-liner
2. ✅ `example_test.go` — at least one `ExampleNewXxx` that runs in CI
3. ✅ Functional options pattern (`WithFoo(...)`) — no giant config structs
4. ✅ Generics where they prevent runtime errors (e.g. `Tool[In, Out]`)
5. ✅ Minimum exported surface (cut convenience exports)
6. ✅ Defaults runnable (zero-config constructors work)
7. ✅ Tests live next to the code (`<thing>_test.go` in same package or `<name>_test` external)

Same discipline as the prior spec's checklist; enforced as folder convention with periodic audit, not as module boundary.

### 4.3 Boundary enforcement via lint

Cross-subpackage imports are policed by `golangci-lint` with `depguard`:

```yaml
linters-settings:
  depguard:
    rules:
      compaction:
        files: ["compaction/**/*.go"]
        deny:
          - pkg: "github.com/nevindra/oasis/workflow"
          - pkg: "github.com/nevindra/oasis/network"
          - pkg: "github.com/nevindra/oasis/guardrail"
          # ... explicit deny list of subpackages this one must not depend on
      # one rule block per subpackage
```

This is softer than separate `go.mod` enforcement (a developer can bypass with `//nolint:depguard`), but in practice it is sufficient if CI fails on lint errors. If a primitive grows to need stronger boundary enforcement, that's a signal it may belong in `internal/` or be promoted to a satellite.

### 4.4 Re-export strategy

The root `oasis` package re-exports the curated public API. **This file is the public surface contract.**

```go
// oasis.go
package oasis

import (
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/compaction"
    "github.com/nevindra/oasis/guardrail"
    "github.com/nevindra/oasis/input"
    "github.com/nevindra/oasis/workflow"
    // ... other subpackages
)

// --- Agent primitives ---
type LLMAgent = agent.LLMAgent
type Option = agent.Option
var NewLLMAgent = agent.NewLLMAgent

// --- Wiring options ---
var WithTool = agent.WithTool
var WithTools = agent.WithTools
var WithProcessors = agent.WithProcessors
var WithSystemPrompt = agent.WithSystemPrompt

// --- Processors (common, curated) ---
var NewSlidingWindow = compaction.NewSlidingWindow
var NewInjectionGuard = guardrail.NewInjectionGuard

// --- HITL ---
type Handler = input.Handler
var AskUser = input.AskUser
```

Rules for the re-export file:

- **Aliases use `=` for types and `var` for funcs.** Zero runtime cost; godoc renders them inline.
- **Only commonly-used API.** Niche knobs require explicit subpackage import — this is a feature, not a bug. The re-export is curation.
- **Re-export grows deliberately.** New exports in a subpackage do NOT auto-re-export; they require an explicit add. This forces a thought: is this API meant for general use?
- **Documented at the file header.** A brief comment per section explains what's exposed and why.

**Satellites are NOT re-exported.** They live in a different `go.mod`; users import them explicitly.

### 4.5 Internal vs public subpackages

```
oasis/
├── (public subpackages — re-exportable, importable by satellites)
│   ├── agent/
│   ├── workflow/
│   ├── compaction/
│   ├── ...
└── internal/   (Go's hard-enforced privacy — satellites cannot reach in)
    ├── loop/
    ├── runtime/
    ├── execplan/
    └── ...
```

Anything that should never appear in user import paths goes in `internal/`. Go's `internal/` rule provides hard enforcement (no satellite can import `github.com/nevindra/oasis/internal/*`), filling the role the microkernel spec gave to a CI `go list -deps` script.

### 4.6 Execution mode: AI-native (retained from prior spec)

This design will be executed by a single AI agent on a long-running branch, not by multiple human authors coordinating through PRs. This means:

- One continuous branch (existing `migration/microkernel` or new `migration/hybrid`)
- No PR review cycles between intermediate states
- Tests run at logical checkpoints, not after every commit
- Each phase is an atomic operation rather than a sequence of staged commits
- Rollback is via git tag, not via reverting individual PRs

The hybrid design benefits from this more than the microkernel did — fewer modules means fewer per-module ceremony steps to absorb. The AI-native execution estimate (§8.2) is correspondingly shorter than the microkernel's.

---

## 5. Target architecture

### 5.1 Final repo layout

```
oasis/                              (single go.mod for primitives + re-exports)
├── go.mod                          (deps: stdlib + minimal)
├── oasis.go                        (re-export umbrella — the public surface)
├── doc.go                          (top-level getting-started)
│
├── agent/                          public subpackage
├── workflow/                       public subpackage
├── network/                        public subpackage
├── compaction/                     public subpackage
├── guardrail/                      public subpackage
├── ratelimit/                      public subpackage
├── input/                          public subpackage
├── memory/                         public subpackage (orchestration only)
├── skills/                         public subpackage (asset loader)
├── processor/                      public subpackage
├── tool/                           public subpackage
├── types/  (or stay at root)       protocol types
├── catalog/                        model vocabulary
│
├── internal/                       Go-enforced private
│   ├── loop/
│   ├── runtime/
│   └── execplan/
│
├── cmd/bot_example/                reference app — kept (cheap to maintain under hybrid)
│
└── (satellites — each its own go.mod)
    ├── store/sqlite/               github.com/nevindra/oasis/store/sqlite
    ├── store/postgres/             github.com/nevindra/oasis/store/postgres
    ├── provider/gemini/            github.com/nevindra/oasis/provider/gemini
    ├── provider/openaicompat/      github.com/nevindra/oasis/provider/openaicompat
    ├── observer/                   github.com/nevindra/oasis/observer
    ├── ingest/                     github.com/nevindra/oasis/ingest
    ├── sandbox/                    github.com/nevindra/oasis/sandbox
    ├── rag/                        github.com/nevindra/oasis/rag
    └── mcp/                        github.com/nevindra/oasis/mcp
```

**Module count: 10 total** (1 root + 9 satellites). Versus microkernel's 19. Versus current root-only's 1 (plus the in-flight extractions).

### 5.2 Dependency direction

```
              app code
                  │
                  ▼ imports
       ┌───────────────────┐
       │  oasis (root)     │  ← primitives + re-exports
       │  + subpackages    │
       └─────────┬─────────┘
                 │ uses
                 ▼
       ┌───────────────────┐
       │  internal/*       │  (Go-enforced private)
       └───────────────────┘

   Satellites depend on root (one-way):

       ┌─────────────────────┐
       │ store/sqlite        │──┐
       │ store/postgres      │  │ imports
       │ provider/gemini     │──┼───→  oasis (root)
       │ provider/openaicompat│ │
       │ observer            │──┤
       │ ingest              │  │
       │ sandbox             │  │
       │ rag                 │  │
       │ mcp                 │──┘
       └─────────────────────┘
```

Invariants enforced:

- Satellites import from root; root never imports from satellites.
- Satellites do not import each other (each is opt-in independently).
- Subpackages within root follow depguard rules (cross-import only where intentional).
- `internal/*` is reachable only from within the root module.

### 5.3 User-facing import shape

**Common path (80% of code):**
```go
import "github.com/nevindra/oasis"
```
Provides all primitives via re-export. Pulls stdlib + minimal deps. Discoverable via `oasis.<TAB>`.

**With backends:**
```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/openaicompat"
    "github.com/nevindra/oasis/store/sqlite"
)
```
Each satellite adds its own deps to the user's `go.sum`.

**Power user reaching past curated re-exports:**
```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/compaction"  // for niche compaction.WithCustomScorer
)
```
Subpackage import is always available — re-export is curation, not gating.

---

## 6. Implementor experience

### 6.1 Adding a new primitive (e.g. a `dedup` processor)

1. `mkdir dedup/`
2. Write `dedup/dedup.go` (`package dedup`)
3. Write `dedup/doc.go` with package-level getting-started doc
4. Write `dedup/example_test.go` with `ExampleNewDeduper`
5. Add `dedup/` rule block to `golangci-lint` depguard config
6. If exposing to user: add re-export line to `oasis.go`

**Total: 4 new files + 1 lint rule block + 1 re-export line. Single commit.**

### 6.2 Adding a new satellite (heavy dep justified)

1. `mkdir foo/`
2. `cd foo && go mod init github.com/nevindra/oasis/foo`
3. Add `replace github.com/nevindra/oasis => ../` directive
4. `go work use ./foo` from repo root
5. Write code + tests + `doc.go` + `example_test.go`
6. Update CI matrix
7. CHANGELOG entry
8. Tag `foo/v0.1.0`

**Total: full module ceremony.** Same as the microkernel plan, but reserved for cases where dep weight justifies it.

### 6.3 Refactor across primitives (e.g. change `Processor` signature)

1. Edit interface in `processor/`
2. Update all implementations in `compaction/`, `guardrail/`, `ratelimit/`, etc.
3. Update tests
4. Run `go test ./...` once at root
5. One commit, one root version bump if needed

**Versus microkernel:** 9 commits + 9 version bumps + 9 CHANGELOG updates + replace-directive coordination.

### 6.4 Cross-subpackage dependency

If `compaction` legitimately needs `processor.Pipeline`, that's an explicit `import "github.com/nevindra/oasis/processor"`. The depguard rule for `compaction/` must allow it. If the dep is circular, lift shared types up to `processor/` or `internal/`.

### 6.5 Daily navigation

`ls *.go` at root shows the small umbrella file + `oasis.go`. Working in a concern means `cd compaction/` — `ls` shows just that concern. Same folder-focus benefit as microkernel; no module ceremony to traverse.

---

## 7. User experience

### 7.1 First agent (full setup)

```go
package main

import (
    "context"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/openaicompat"
    "github.com/nevindra/oasis/store/sqlite"
)

func main() {
    provider := openaicompat.New(openaicompat.WithAPIKey("..."))
    store := sqlite.New("agent.db")

    agent := oasis.NewLLMAgent("assistant", "You are helpful.", provider,
        oasis.WithStore(store),
        oasis.WithProcessors(
            oasis.NewSlidingWindow(20),
            oasis.NewInjectionGuard(),
        ),
        oasis.WithTool(oasis.AskUser(myHandler)),
    )

    agent.Execute(context.Background(), task)
}
```

Three imports total. Primitives via `oasis.*` — no package-finding question per feature.

### 7.2 Discovery

`oasis.<TAB>` reveals: `NewLLMAgent`, `WithStore`, `WithTool`, `WithProcessors`, `NewSlidingWindow`, `NewInjectionGuard`, `AskUser`, etc. — the curated set.

For backends: users already mentally categorize "I need a store" and "I need a provider". The `oasis/store/*` and `oasis/provider/*` paths match that model directly.

For niche knobs: `import "github.com/nevindra/oasis/compaction"` and reach for advanced API. Documented in the subpackage's `doc.go`.

### 7.3 Bundle weight

Default `import "github.com/nevindra/oasis"`: stdlib only.

Add `store/sqlite`: + `modernc.org/sqlite`.
Add `observer`: + OTEL.
Add `sandbox`: + Docker SDK.

Each satellite is opt-in. Users pay for what they import — the current state's "every user gets Docker SDK" problem is solved.

### 7.4 Version coordination

Common case: `go get -u github.com/nevindra/oasis@latest`. Done. Primitives are one version.

If satellites pinned separately: `go get -u github.com/nevindra/oasis/store/sqlite@latest`. Independent because the satellite's dep churn is independent.

**No compatibility matrix.** Satellite `<sat>/vX.Y.Z` works with root `oasis/vM.N` if its `go.mod` declares that compatibility — same as any other Go library. No 19-way coordination.

---

## 8. Migration from current state

### 8.1 Current state inventory

**In the root `oasis` package (need relocation):**

| Files | Destination |
|---|---|
| `agent.go`, `llmagent.go`, `agentcore.go`, `handle.go` | `agent/` |
| `workflow*.go` (4 files) | `workflow/` |
| `network.go` | `network/` |
| `compaction*.go` | `compaction/` subpackage (already partly extracted as satellite — see Phase A) |
| `guardrail.go` | `guardrail/` subpackage (currently satellite) |
| `ratelimit.go` | `ratelimit/` subpackage (currently satellite) |
| `input.go` | `input/` (per existing extract plan, but as subpackage not satellite) |
| `memory.go`, `agentmemory.go` | `memory/` |
| `loop.go`, `suspend.go`, `batch.go`, `stream.go` | `internal/loop/` or `internal/runtime/` |
| `tool.go` | `tool/` |
| `processor.go` | `processor/` |
| `types.go` | stay at root or `types/` |
| `catalog.go` | stay at root or `catalog/` |
| `mcp_*.go` | `mcp/` satellite |
| `skill*.go` | `skills/` |
| `retriever.go`, `cosine.go` | `rag/` satellite |

**Subdirectories needing promotion to satellite (own `go.mod`):**

| Current path | Action |
|---|---|
| `store/sqlite/`, `store/postgres/` | Add `go.mod`, declare deps |
| `provider/gemini/`, `provider/openaicompat/` | Add `go.mod` |
| `observer/` | Add `go.mod`, move OTEL deps from root |
| `ingest/` | Add `go.mod`, move PDF/DOCX/embedding deps |
| `sandbox/` | Add `go.mod`, move Docker SDK |

**Already-extracted satellites needing DEMOTION to subpackage:**

| Current satellite | Reason |
|---|---|
| `ratelimit/` | stdlib only — no dep weight justifies satellite |
| `guardrail/` | stdlib only — same |
| `compaction/` | stdlib only — same |

### 8.2 Migration sequence

> **AI-native note:** sequence below describes logical work units. Under AI-native execution, each phase is an atomic operation.

**Phase A — Demote wrong-satellite modules back to subpackages (~3 days)**

For each of `ratelimit`, `guardrail`, `compaction`:
1. Delete `<name>/go.mod` and `<name>/go.sum`
2. Update test imports back to same-module paths
3. Remove from `go.work` `use` block
4. Add depguard rule block for the subpackage
5. Add re-export aliases to `oasis.go`
6. Verify `go test ./...` passes
7. CHANGELOG entry: "demoted from satellite to subpackage; same import path **is** the subpackage import path — no user-facing API change beyond the alias availability in root"

**Phase B — Move root files into subpackages (~1-2 weeks)**

In order of risk (low → high):
1. `processor/` — pure interface lift
2. `tool/` — interface + small helpers lift
3. `input/` — small, well-bounded (subsumes existing `docs/plans/2026-05-18-extract-input.md` but as subpackage)
4. `memory/` — orchestration only; storage backends already separate
5. `compaction/`, `guardrail/`, `ratelimit/` — already small subpackages from Phase A; just verify they have full DX checklist
6. `workflow/` + `network/` — larger but well-isolated
7. `skills/` — move root `skill*.go` files in
8. `agent/` — touches the most; do last

Per-move: `git mv`, update `package` declaration, update internal imports, add depguard rule, add re-exports, run tests.

**Phase C — Promote heavy subpackages to satellites (~1 week)**

For each of `store/{sqlite,postgres}`, `provider/{gemini,openaicompat}`, `observer`, `ingest`, `sandbox`:
1. `go mod init github.com/nevindra/oasis/<path>`
2. Add `replace github.com/nevindra/oasis => ../../` (depth depends on nesting)
3. Update root `go.mod`: drop heavy deps that now live in satellite
4. `go work use ./<path>`
5. Update any cross-package imports
6. CI matrix update
7. Run full workspace tests

**Phase D — Extract new satellites from root code (~2 weeks)**

- `mcp/` — extract MCP files from root into satellite. Largest surgery (~3000 LOC per prior spec §7.3).
- `rag/` — extract `retriever.go`, `cosine.go` into satellite. May need internal untangle for ~6000+ LOC.

**Phase E — Cleanup and ship (~1 week)**

1. Move `loop.go`, `suspend.go`, `handle.go`, `batch.go`, `stream.go` to `internal/loop/` and `internal/runtime/`
2. Final root `go.mod` audit — should only have stdlib + lightweight deps (uuid, slog handlers, etc.)
3. Final exports audit on `oasis.go` — every alias justified
4. Update PHILOSOPHY.md framing: drop "microkernel" language
5. Ship as `v0.17.0` with CHANGELOG describing the full reshape

**Total estimated time: ~5-6 weeks.** Versus microkernel's ~6-8 weeks AI-native estimate, with strictly better user-DX outcome.

### 8.3 Handling work already done

The in-flight extraction work (`ratelimit/`, `guardrail/`, `compaction/`) is NOT wasted:
- All the DX checklist items (`doc.go`, `example_test.go`, focused tests) carry directly into the subpackage form.
- All the interface design and option ergonomics carry over.
- The only "undone" work is the `go.mod` + replace-directive scaffolding, which is ~30 lines per module.

The `input` extraction plan (`docs/plans/2026-05-18-extract-input.md`) was not yet executed. The same kernel-surgery work (kill `WithInputHandler` auto-wiring) still happens; the destination is a subpackage of root rather than a separate module. The plan should be revised before execution.

---

## 9. Decision register

### 9.1 New decisions (this design)

| # | Decision | Rationale |
|---|---|---|
| H1 | Hybrid (subpackages + heavy satellites) over microkernel | Mastra empirical evidence (24K stars batteries-included); user-DX cost of microkernel not justified by maintainer benefit. The folder-focus benefit implementor values is delivered by subpackages, not by separate `go.mod` |
| H2 | Litmus test for satellite = "carries opt-out dep weight" | Concrete and defensible. Replaces fuzzy "is this a battery" test from prior spec §4.5 |
| H3 | Re-export via type alias in root `oasis.go` | Zero runtime cost; preserves one-import DX; root file becomes the curated public-surface contract |
| H4 | `golangci-lint` depguard for cross-subpackage boundary enforcement | Sufficient for monorepo discipline; avoids module-level coordination cost. If primitive grows to need stronger boundary, that's a satellite-promotion signal |
| H5 | `internal/` for things that should never be public | Hard Go enforcement; replaces "core imports nothing" CI rule from prior spec §4.1 |
| H6 | Primitives version with root | Per-primitive versioning is illusory benefit for stdlib-only concerns; YAGNI |
| H7 | Demote `ratelimit`/`guardrail`/`compaction` back to subpackage | Wrong call to extract them; stdlib-only deps; no weight to justify satellite cost |
| H8 | Promote `observer`, `ingest`, `sandbox` to satellites | Heavy/optional deps the prior spec missed (OTEL, PDF readers, Docker SDK each meet the H2 test cleanly) |
| H9 | `input` becomes subpackage, not satellite | D17 motivation (kill HITL auto-magic) is still valid — the auto-wiring removal is real cleanup. But satellite-with-own-go.mod is the wrong delivery mechanism; subpackage delivers the same architectural cleanup with better user DX |
| H10 | Keep `cmd/bot_example` as integration testbed | Microkernel spec deleted it (D13) because per-extraction CI was sufficient; under hybrid, one-module setup makes it cheap to keep, and it remains useful as a known-good end-to-end smoke check |
| H11 | Supersede prior spec, don't amend in place | Architectural about-face deserves its own document; prior spec preserved as history of considered-and-rejected decision |
| H12 | Drop "kernel" framing in PHILOSOPHY.md | "Microkernel" is no longer accurate; the architecture is "clean monolith for primitives + satellites for heavy deps". Re-state PHILOSOPHY.md in those terms during Phase E |

### 9.2 Decisions retained from prior spec

These survive the architectural revision unchanged:

- **D6** (Tool shape: Path B atomic + generics) — still right; implemented in current extracted modules; carries directly
- **D14** (delete 8 CC-shaped wrapper tool packages) — still right; the right reason was always that they belonged in owner modules or harness layer
- **D15** (catalog types stay in root namespace) — still right
- **D17 motivation** (kill auto-magic for HITL) — still right; implementation now via subpackage + explicit composition + `input.Setup()` bundle helper (see §10 open items)
- **D18 positioning** (kernel + curated batteries + harness) — **partly survives**: drop "kernel" word, "batteries" become subpackages (not separate modules), harness layer still deferred to Phase 3b

### 9.3 Decisions reversed from prior spec

| Prior decision | Status under hybrid |
|---|---|
| D1 (microkernel architecture) | **REVERSED** in favor of hybrid |
| D5 (Store/Memory not in core) | Memory orchestration moves to subpackage — still "not in the loop's main path" in the litmus-test sense, but lives in the root module |
| D9 (per-module versioning + per-module v1.0 promotions) | **REVERSED** for primitives (version with root); **retained** for satellites |
| D12 (AI-native per-module extraction) | **REVERSED**; per-subpackage move is cheaper; satellites still get full-module ceremony but only when justified |
| D13 (delete `cmd/bot_example`) | **REVERSED** per H10 |

---

## 10. Open items before execution

- [ ] Approve this design (supersede `2026-05-17-microkernel-migration-design.md`)
- [ ] Decide whether `types` and `catalog` get their own subpackages or stay at root namespace (default: stay at root for now; promote if either grows >500 LOC)
- [ ] Decide whether `skills` stays subpackage or becomes satellite (depends on whether asset weight when embedded exceeds the "lean root binary" goal — measure during Phase B)
- [ ] Settle on the bundle-helper pattern for input/skills/etc. The prior conversation surfaced the `input.Setup(handler) []oasis.Option` pattern that preserves one-liner DX while keeping composition explicit. Confirm and adopt as the standard satellite/subpackage idiom for "common case = ergonomic, power case = composable"
- [ ] Write the Phase A implementation plan (demote `ratelimit`, `guardrail`, `compaction` back to subpackages) — the cheapest first step that proves the pattern and immediately fixes the wrong-direction work
- [ ] Update PHILOSOPHY.md positioning statement (currently in line with the rejected microkernel framing)
- [ ] Update CLAUDE.md to reflect the new architecture (file/folder map currently describes pre-migration state)

---

## 11. Acceptance criteria

Final state criteria for the whole reshape:

1. ✅ Root `go.mod` deps: stdlib + lightweight only (no Docker SDK, no OTEL SDK, no DB drivers, no PDF readers)
2. ✅ `import "github.com/nevindra/oasis"` provides primitives via curated re-export
3. ✅ Each subpackage has `doc.go` + `example_test.go` that runs in CI
4. ✅ `golangci-lint` depguard config enforces declared cross-subpackage boundaries; CI fails on violation
5. ✅ 9 satellites each have own `go.mod`, `doc.go`, `example_test.go`, tests passing
6. ✅ `cmd/bot_example` builds and runs against the new structure
7. ✅ CHANGELOG documents the full reshape with migration notes for any consumers
8. ✅ Tagged as `v0.17.0` (root) + `<satellite>/v0.1.0` per satellite
9. ✅ PHILOSOPHY.md updated; "microkernel" language removed
10. ✅ Prior microkernel spec marked superseded, kept for history

---

## 12. Per-module versioning rules (post-migration)

| Module | Rules |
|---|---|
| Root `oasis` (primitives) | v1.0 once shape stable in ≥2 real apps for ≥1 month. After v1.0: new exports only via additive change; breaking changes require `oasis/v2` |
| Satellite modules | Each promotes to v1.0 independently when stable. No fixed schedule |

### 12.1 Module v1.0 promotion criteria (for satellites)

All must be true:

1. ✅ Used in 2+ different apps for non-trivial work
2. ✅ DX checklist passes (§4.2)
3. ✅ No pending design changes
4. ✅ User can write working code on first try using only `go doc` output

### 12.2 Backwards-compat policy post-v1.0

- **Allowed:** add new exports, add fields with safe zero values, add new options
- **Forbidden:** modify exported signatures, remove options, repurpose fields, rename exports
- **Deprecation cycle:** announce → leave deprecated for 2 minor releases → remove in next major
- **Breaking changes after v1.0:** require `/v2` module path
