# DX / Maintainability / Separation-of-Concerns Audit

**Date:** 2026-05-18
**Status:** Audit findings — not yet a plan
**Goals reviewed against:**
1. Best DX for framework users
2. Easiness to maintain
3. Clear separation of concerns

**Scope:** Post-hybrid-architecture migration (phases 0–4 complete, phase 5 skipped, phase 6 partially done). Looked at public surface, option API, extension points, and the developer "first 5 minutes" experience.

**Filter:** Only substantial wins (~2-5x improvements). Minor cleanups excluded.

---

## 1. Unify the history-shrinking mechanisms (narrowed from broader regrouping)

**Impact:** DX, ~1.5-2x
**Priority:** ~~Medium~~ — **SHIPPED 2026-05-18 (commit `1af0e14`)**

`WithHistory(...history.Option)` + `WithGeneration(Generation{...})` landed. Original `WithCompressModel`, `WithCompressThreshold`, `WithCompaction`, `WithSemanticTrimming`, and the 4 generation params have been folded into the grouped forms. Section below preserved for context.

> **Revised 2026-05-18 after revalidation.** The original framing ("collapse 31 options into 8 grouped configs") was overreach. Audit of the 31 options shows only ~4 genuinely confuse users; the other 27 are independent concerns that don't benefit from grouping and would lose clarity under struct configs (zero-value ambiguity, harder discoverability per field). Realistic gain is closer to 1.5x — useful, but smaller than items #2 and #3 below. Scope narrowed to the two changes that actually pay back.

### Today

`agent/agent.go` exposes 31 `With*` options. Categorizing them:

| Category | Count | Genuine confusion? |
|---|---|---|
| Generation tuning (`Temperature`, `TopP`, `TopK`, `MaxTokens`) | 4 | No — independent dials |
| Limits (`MaxIter`, `MaxAttachmentBytes`, `SuspendBudget`) | 3 | No |
| **History shrinking (`CompressModel`, `CompressThreshold`, `Compaction`, `SemanticTrimming`)** | **4** | **Yes — three overlapping mechanisms** |
| Memory (`ConversationMemory`, `UserMemory`) | 2 | Mild |
| Dynamic resolvers (`DynamicPrompt`, `DynamicModel`, `DynamicTools`) | 3 | No — share a shape, distinct purpose |
| Spawn (`SubAgentSpawning`, `MaxSpawnDepth`, `DenySpawnTools`) | 3 | No — already scoped via nested options |
| Skills (`Skills`, `ActiveSkills`) | 2 | No |
| Capabilities (`Sandbox`, `PlanExecution`, `ResponseSchema`) | 3 | No |
| Observability (`Tracer`, `Logger`) | 2 | No |
| Extension (`Processors`, `InputHandler`) | 2 | See item #2 |
| Core (`Tools`, `Prompt`, `Agents`) | 3 | No |

The real DX problem is the **history shrinking** row: three mechanisms with overlapping semantics:

- `WithCompressModel` + `WithCompressThreshold` — raw rune-count compression
- `WithCompaction(c, threshold)` — per-thread compactor, lives inside `WithConversationMemory`
- `WithSemanticTrimming(emb, opts...)` — relevance-based trimming, also inside `WithConversationMemory`

A user reading godoc has to figure out which one they need, and combinations aren't obvious.

### Proposed

**Change A — unify history shrinking under `WithHistory`:**

Use nested functional options (the pattern already used by `WithConversationMemory` and `WithSubAgentSpawning`):

```go
oasis.WithHistory(
    history.Store(store),
    history.MaxHistory(30),
    history.Compaction(c, 0.8),               // replaces WithCompaction + WithCompressModel/Threshold
    history.SemanticTrim(emb, history.KeepRecent(10)),
)
```

Removes 4 top-level options. `WithCompressModel`, `WithCompressThreshold`, `WithCompaction`, and `WithSemanticTrimming` move into `history.*` sub-options. `WithConversationMemory` collapses into `WithHistory` (since "memory store + history limits + shrinking strategy" all live together now).

**Why nested functional options, not a struct config:**

1. Pattern already exists in the framework (`WithConversationMemory`, `WithSubAgentSpawning`)
2. No zero-value ambiguity — `MaxHistory(0)` is unambiguous in a way `History{MaxHistory: 0}` isn't
3. Optionality is visible at the call site — every option named is one you enabled
4. Each sub-option has its own godoc entry
5. Adding a future strategy (e.g. `history.SlidingWindow`) is one new func, no struct rewrite

**Change B — group the 4 generation params into `WithGeneration`:**

```go
oasis.WithGeneration(oasis.Generation{
    Temperature: 0.5, TopP: 0.9, MaxTokens: 1000,
})
```

Struct is fine here (not nested options) because:
- Users almost always set these together
- They're well-known industry concepts (no per-field doc needed)
- Zero is a legitimate value for most, with documented defaults

Removes 4 top-level options.

### Out of scope (deliberately)

The audit originally proposed grouping into `WithLimits`, `WithResolvers`, `WithSpawn`. **Drop those.** Reasons:
- `WithLimits`: the 3 limit options are independent; grouping is cosmetic and brings struct zero-value problems.
- `WithResolvers`: 3 dynamic resolvers share a function shape but are otherwise independent — grouping saves 2 lines and loses per-resolver godoc.
- `WithSpawn`: already correctly grouped via `WithSubAgentSpawning(SubAgentOption...)`. The audit double-counted this.

### Outcomes

- Public surface drops from 31 → ~24 options (not 31 → 8).
- The genuine point of confusion (3 history-shrinking mechanisms) becomes one composable namespace.
- Generation params get the small ergonomic win of a struct.
- Nothing else changes — most of the option API was already fine.
- Estimated effort: ~1-2 days. Migration guide for v0.x bump.

---

## 2. Kill `WithProcessors(...any)` — typed extension points

**Impact:** DX, ~3x
**Priority:** ~~High~~ — **SHIPPED 2026-05-18 (commit `ba9cbd7`)**

Three typed options (`WithPreProcessors`, `WithPostProcessors`, `WithPostToolProcessors`) replaced the `any`-typed registration. `processor.Chain.Add(any)` removed in favor of `AddPre`/`AddPost`/`AddPostTool`. Type checking now happens at compile time. Section below preserved for context.

### Today

`WithProcessors(processors ...any)` accepts anything. `processor/chain.go:Add(p any)` type-switches at registration and **panics** if `p` doesn't satisfy `core.PreProcessor`, `core.PostProcessor`, or `core.PostToolProcessor`. A unit test (`TestChainAddPanicsOnInvalidType`) codifies the runtime panic.

This is the framework's main extension point — used for guardrails, compaction, logging hooks, content guards. It has zero compile-time safety.

### Proposed

Three typed registrations:

```go
oasis.WithPreProcessors(p1, p2)
oasis.WithPostProcessors(p3)
oasis.WithPostToolProcessors(p4)
```

Or a single `Processor` interface with a `Stage()` accessor / multiple optional methods. Implementations that satisfy multiple stages can be registered to each.

### Outcomes

- Compile-time enforcement; no startup-panic path.
- Type-aware autocomplete in editors.
- The runtime type-switch in `Chain.Add` goes away.

---

## 3. Ship one working `cmd/example` — the 5-minute experience is currently zero

**Impact:** DX (first-run), ~5x
**Priority:** High (cheapest of the five — pure additive)

### Today

`CLAUDE.md` line 25 references "Reference apps (Telegram bot, etc.) are demos" but `cmd/bot_example/` is **empty**. The only end-to-end material is six per-subpackage `example_test.go` files of 30–90 lines each, each demonstrating one concept in isolation.

A user wanting to see "agent + provider + memory + a tool + streaming" wired up together has to assemble it themselves from godoc.

### Proposed

One ~200-line `cmd/example/main.go` that wires:
- Provider (e.g., openaicompat)
- LLMAgent with a couple of tools
- Conversation memory (sqlite store)
- Streaming output
- Optionally: a second agent and a Network to demonstrate multi-agent

Heavy inline comments explaining each block. Compiles, runs against a real provider, demonstrates the framework's identity.

### Outcomes

- New users have something to copy-paste-and-run.
- Acts as living documentation that breaks loudly when APIs drift.

---

## 4. ~~Split `NetworkOption` from `AgentOption`~~ — **Skip (premise invalidated 2026-05-18)**

**Original impact claim:** Separation of concerns, ~2x
**Revised verdict:** Not worth doing. The "leak" the audit identified is largely fictional.

### Original argument (preserved for context)

`network.NewNetwork(name, desc, router, opts ...agent.AgentOption)`. Network reuses LLMAgent's 31 options.

The original audit claimed ~10 options were LLMAgent-only and didn't apply to Network:
`WithPlanExecution`, `WithSubAgentSpawning`, `WithResponseSchema`, `WithCompressModel`, `WithCompressThreshold`, `WithSandbox`, `WithSkills`, `WithActiveSkills`, `WithInputHandler`, `WithDynamicTools`.

Proposed fix: extract a `Common` base struct; give `agent.Option` and `network.Option` separate types composing `Common` plus type-specific options.

### Verification (2026-05-18) — the premise is wrong

Walked the actual code. `network.NewNetwork` (network.go:32) calls `agent.InitCore`, the same shared init `LLMAgent` uses. `InitCore` (agentcore.go:60-122) copies essentially every config field into `AgentCore`. `Network.buildLoopConfig` (network.go:81) then calls `BaseLoopConfig` (agentcore.go:210-237), which wires `responseSchema`, `compressModel`, `compressThreshold`, `generationParams` into the loop just like for LLMAgent.

Audit list vs. reality:

| Option | Audit claim | Reality |
|---|---|---|
| `WithPlanExecution` | LLMAgent-only | **Used** — network.go:112, 125 (`DispatchBuiltins`, spawn config) |
| `WithSubAgentSpawning` | LLMAgent-only | **Used** — network.go:117; `spawn_test.go:61` exercises it on Network |
| `WithResponseSchema` | LLMAgent-only | **Used** — `BaseLoopConfig` → loop.go:232 attaches to every `ChatRequest` |
| `WithCompressModel` | LLMAgent-only | **Used** — `BaseLoopConfig` → loop.go:577 |
| `WithCompressThreshold` | LLMAgent-only | **Used** — `BaseLoopConfig` → loop.go:484 |
| `WithSandbox` | LLMAgent-only | **Used** — network.go:34 reads `cfg.Sandbox`/`SandboxTools` directly |
| `WithActiveSkills` | LLMAgent-only | **Used** — network.go:83 (`n.ActiveSkillInstructions` injected into prompt) |
| `WithInputHandler` | LLMAgent-only | **Used** — network.go:112 (`n.Handler` for `ask_user` dispatch) |
| `WithDynamicTools` | LLMAgent-only | **Used** — network.go:56, 91 (resolves per-call dynamic toolset) |
| `WithSkills` | LLMAgent-only | **Currently no-op** — network.go:42 has a TODO'd commented block from phase 2.4; the only real no-op on Network |

**9 of 10 claimed "leaks" are real Network features.** The 10th is a known-disabled block scheduled for re-enable. Network's option surface that legitimately applies is closer to **~30/31**, not the audit's claimed 12/31.

### Real-world misuse data

Only 6 `NewNetwork` callers exist in the repo (1 example + 5 tests). Options actually passed: `WithAgents`, `WithSubAgentSpawning`, `WithDynamicPrompt`, `WithDynamicModel`, `WithTools`. **Every one is a legitimate Network feature.** Zero actual misuse — the footgun is theoretical.

### Revised recommendation

- **As a standalone change: skip.** The pain it cures barely exists. Large breaking refactor for ~1 currently-disabled option.
- **As a prerequisite for #1: also skip.** Since Network and LLMAgent legitimately share ~30/31 options, grouped configs (`Generation`, `Limits`, `History`, …) should be designed on the *shared* base — which is the natural shape of the code today. Splitting first would actively complicate #1 rather than enable it.
- **Worth doing in the spirit of #4:** re-enable or delete the commented-out skill-provider block at network.go:42 (10-line cleanup, not a refactor).

---

## 5. Get `cmd/ix` + `internal/ixd` (2280 LOC) out of the framework's mental model

**Impact:** Separation of concerns (cognitive), ~2x
**Priority:** Low (one-line fix, or larger move)

### Today

Repo contains an unrelated server (`cmd/ix/main.go` + `internal/ixd/`, ~2280 LOC, with browser/file/shell/pinchtab adapters). It's a separate tool that happens to share the repo.

`CLAUDE.md` doesn't mention it. A user cloning the repo to learn the framework reads through it trying to figure out how it relates — answer: it doesn't.

### Proposed

Either:
- **Option A** (clean): move to its own repo.
- **Option B** (cheap): add one sentence to `CLAUDE.md` partitioning it explicitly: "`cmd/ix` and `internal/ixd` are unrelated dev tooling; framework users can ignore them."

### Outcomes

- Framework users stop wondering whether `ixd` is something they need to understand.
- Either route is fine; B is a 30-second fix.

---

## Explicitly skipped (not 2-5x)

- **Re-export style (`var WithX = agent.WithX`):** the umbrella godoc is weaker than wrapper functions would give, but this is consistent with the PHILOSOPHY ("curated surface, power users import subpackage directly"). Not a substantial win.
- **`batch.go` at root:** real impurity (only non-umbrella file in root `package oasis`), but it's a Phase 6 cleanup line-item, not a structural improvement. `BatchProvider` arguably belongs in `core/` next to `Provider`.
- **Module / satellite topology:** the hybrid layout (root re-exports + leaf `core/` + satellite go.mods for heavy deps) is well-thought-out and the recent `CLAUDE.md` rewrite documents it clearly. No change recommended.

---

## Suggested ordering

Updated after revalidation of #1 and #4. The big wins are #3 and #2; #1 is a smaller targeted change; #4 is dropped (premise invalidated — see section).

1. **#3 (example app)** — pure-additive, biggest first-impression win, easiest to ship.
2. **#5 option B** (CLAUDE.md sentence) — 30-second fix.
3. **#2 (typed processors)** — small, focused, removes a real footgun (runtime panic on bad type).
4. **#1 (history unification + generation grouping)** — targeted ~1.5-2x DX win, breaking change. Design grouped configs on the *shared* `AgentCore`/`InitCore` base — Network and LLMAgent legitimately consume ~30/31 of the same options.
5. ~~#4 (split `NetworkOption` from `AgentOption`)~~ — **skipped.** See section 4 for verification.

Optional cleanup, not in the main sequence: re-enable or delete the commented skill-provider block at `network/network.go:42` (the only real Network/Agent option drift today).

Items 1, 2, 3 ship without breaking changes. Item 4 (#1 in this list) is breaking — ship in a v0.x minor bump with a migration guide.
