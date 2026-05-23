---
name: simplify-and-consolidate
description: Use when asked to simplify, consolidate, dedupe, combine, or tighten code in specific Oasis directories or files. Focuses on improving DX, lowering maintenance burden, and shrinking surface area — without breaking the invariants the existing code preserves. Mandatory "read the project principles, then understand the concept" phase prevents naive collapses that erase design context or contradict stated framework opinions.
---

# Simplify & Consolidate

## Overview

Scan the files or directories the user names and find places where the code can shrink **or better honor the project's stated principles** without losing behavior. Two outcome bars qualify a finding:

1. **Concrete reduction** — lines removed, public symbols collapsed, shapes unified, or surfaces shrunk.
2. **DX or maintenance win** — a stated principle that the current code violates (a runtime stub that fails the "fail at compile time" rule, a builder that has silently drifted out of sync, a shape that contradicts a uniformity contract, etc.). DX bugs count even when line-count reduction is modest.

Cosmetic renames and aesthetic improvements still don't qualify.

Critical guardrail: **before proposing any simplification, read the project's principles and document the current concept and its invariants.** A simplification that breaks a documented `Why:` comment, an invariant the tests rely on, or a contract a caller depends on is worse than no simplification at all. Code that looks redundant often isn't — it preserves something subtle. The job is to spot the cases where the redundancy is real, not to flatten anything that rhymes.

This skill is the complement to `high-impact-audit`. That one asks "is this slow or broken?" with a 3-10× threshold. This one asks "is this gnarlier than it needs to be, and does it honor the stated design?" Both reject nits.

## When to Use

- User points at a directory or file and asks to **simplify**, **consolidate**, **combine**, **dedupe**, or **tighten**
- After a feature ships and the area accumulated "yet another option" or "another branch in the loop"
- When a file passes ~800 lines, an interface passes ~10 methods, or an option list passes ~15 entries — these are natural prompts to ask "did the abstraction earn this?"

**Do NOT use for:**
- Performance, memory, or correctness wins — use `high-impact-audit`
- Bug hunts — use `superpowers:systematic-debugging`
- Green-field design — use `superpowers:brainstorming`
- Style/format cleanup — `gofmt` / `golangci-lint` handle that
- Code review of a specific diff — use `code-review` or `superpowers:requesting-code-review`

## The Filter

A finding qualifies only if it produces ONE of these outcomes:

**Reduction outcomes:**
- **≥30 lines removed at one location** — a duplicated block collapsed, a 4-branch switch merged to 1, a copy-loop folded into a call.
- **≥1 exported symbol deleted** — a `WithFoo` option removed, a struct field unified, a type folded into another, a re-export retired.
- **≥3 inconsistent shapes collapsed to 1** — six `WithLogger` spellings becoming one convention; three "scoped option" patterns becoming one; four "configure timeout" knobs becoming a single typed config.
- **≥1 layer of indirection removed** — a `Config → struct` field-copy eliminated, a passthrough method deleted, a wrapper that adds nothing folded away.
- **≥1 dead-or-shadowed declaration deleted** — a package constant shadowed by a configurable knob, a field that's always nil, a code path that's unreachable, a comment-only TODO that no longer applies.

**DX / maintenance outcomes** (qualify even when line-count reduction is small):
- **Runtime-stub elimination** — a method, option, or interface conformance exists but always rejects/errors (`ExecuteWith` that returns "not yet supported" on every override; an option that's silently ignored). The capability is *promised* by the API but not delivered. Either implement it or remove it so the type-assertion fails honestly.
- **Drift fix between parallel constructors** — two builders (`BaseFoo` + `BuildFooFrom`) construct the same struct from different inputs, and one has silently dropped a field the other sets. Consolidating them fixes the regression and prevents the next drift.
- **Exported surface consolidation** — ≥5 sibling `With*` options (or setters, package-level constructors) for knobs of the *same subsystem* collapsed into one typed sub-config option. Example: seven `WithMaxIter` / `WithMaxSteps` / `WithMaxAttachmentBytes` / `WithMaxToolResultLen` / `WithMaxPlanSteps` / `WithMaxParallelDispatch` / `WithSuspendBudget` → one `WithLimits(Limits{...})`. The line-count win is modest, but the DX wins are real and load-bearing: discoverability (typing `Limits{` triggers IDE/LLM autocomplete for *every* knob; 7 separate names don't), reusability (a shared `Limits` value vs a helper returning `[]Option`), one godoc page per cluster instead of 7, symmetry between agent construction and per-call override surfaces, and a typed shape that LLM coding assistants generate correctly on the first try. **This is a top-tier finding, not a footnote** — never demote it because "the options aren't broken." Cite the project's "consolidate aggressively" / "codegen-friendly" / "fewer powerful primitives" principle when one exists.
- **Principle-violation fix** — code that contradicts a stated framework opinion (consistent shapes, fail-at-compile-time, recursive composition, codegen-friendliness). Cite the violated principle line.

If a finding doesn't fit one of these, drop it. **Quantify the win explicitly** — line count, symbol count, drift bug fixed, or principle cited. "Feels cleaner" is not an outcome.

## Process

### Phase 0 — Read the project principles (MANDATORY before Phase 1)

Before scanning a single Go file, read the project's principle documents in this order:

1. **`CLAUDE.md` / `AGENTS.md` / `GEMINI.md`** at the repo root — codebase-specific rules and conventions.
2. **Linked design documents** — Oasis points at `docs/PHILOSOPHY.md` (framework identity, API strategy, design opinions) and `docs/ENGINEERING.md` (coding standards). Other projects use ARCHITECTURE.md, DESIGN.md, etc.
3. **The pre-v1 / post-v1 phase signal.** PHILOSOPHY-style docs usually state explicitly whether breaking changes are allowed or require deprecation cycles. This **changes the recommended order** (see below) — pre-v1, principle-violation fixes that require breaking changes are *promoted*, not deferred.

Outcomes of Phase 0 you must record:
- Which principles the area you're scanning is governed by.
- Whether the project is pre-v1 (break-what-needs-breaking) or post-v1 (deprecation cycle required).
- Any phrases you'll cite when ranking findings (e.g. *"Fail at compile time, not runtime"*, *"Consolidate aggressively"*, *"Extract only when a pattern repeats 3x"*).

**You cannot skip Phase 0.** A simplification that looks safe by line-count but contradicts a stated principle is a bad finding. Equally, a finding that looks risky in the abstract but fixes a documented principle violation is a *priority* finding for that project, not a footnote.

### Phase 1 — Understand the concept (MANDATORY before any finding)

You may not propose a simplification you cannot defend. For each area you intend to touch:

1. **Read the contract.** What's exported? Which callers (in this repo and likely in user code) rely on the current shape? Use `codedb_callers` / `codedb_word` to find the actual call sites.
2. **Read every `Why:` comment in the area.** These are explicit design-decision records. They exist because someone hit the failure mode the comment describes. A simplification that erases a `Why:` without addressing what the comment said is a regression in disguise.
3. **Map the invariants.** Concurrency boundaries (which lock guards what state), lifecycle (who creates / who closes), ordering (does step A have to happen before step B), error semantics (what's recoverable, what's fatal, what's a business failure vs an infra error).
4. **Locate the tests.** A behavior covered by a test is part of the de facto contract. Note which tests would catch a break. **No tests covering the area is itself a finding** — surface it before proposing the simplification, not after.
5. **Identify what's load-bearing.** Code that looks duplicated across branches often differs in subtle ways: one path emits a stream event, another doesn't; one path applies a middleware, another bypasses it; one path is on the hot loop, another is the error tail. The simplifier MUST list these differences before proposing a collapse — otherwise the "collapse" silently changes behavior on one branch.

**Self-check before leaving Phase 1:** Can you describe, in one sentence each, (a) what this block does, (b) why it's structured this way, and (c) what would observably break if you naively merged the branches? If any of those three is fuzzy, read more — do not proceed to Phase 2 yet.

### Phase 2 — Find candidates

With Phase 0 + Phase 1 done, sweep the area for these specific patterns:

- **Duplicate blocks** — same structure and near-identical bodies across branches or files. Use `codedb_search` for the repeating idiom; eyeball the candidates to confirm the differences are cosmetic, not load-bearing.
- **Drift-prone parallel builders** — two functions that construct the same shape (`LoopConfig`, `Request`, `Snapshot`, etc.) from different inputs. Diff their field lists exhaustively. If one is missing a field the other sets, that's an active or latent bug, not just a duplicate — flag it as a drift fix.
- **Runtime stubs that promise capability** — methods that exist solely to error on every meaningful input (`ExecuteWith` that rejects all `opts.HasOverrides()`; options whose setter is wired but whose effect is never read). The type-assertion succeeds, the call fails. Either implement or remove.
- **Shape proliferation** — same conceptual operation spelled multiple ways across packages (`WithLogger` × 6, `WithRetrieverLogger`, `WithChunkerLogger`, etc.). Especially suspicious when "configure X" lacks a uniform name.
- **Ping-pong field copies** — struct A's job is to be assigned field-by-field into struct B, and the two structs always carry the same data. Often appears as `Config` → runtime-state with ~30 lines of `c.foo = cfg.foo`, or a method that hand-rebuilds a struct that's already embedded.
- **Dead or shadowed declarations** — package-level constants overridden by configurable knobs; struct fields that are written but never read (or vice versa); branches whose condition can never be true.
- **Surface bloat** — sibling exported options (`With*`, setters, constants, env vars) that describe knobs of *one subsystem*. **Hard rule: ≥5 entries qualifies as a top-tier finding under the "Exported surface consolidation" outcome above** — surface it even when nothing is broken. 3–4 entries is a judgement call (often worth surfacing if the cluster is growing or feeds a parallel RunOptions-style surface). Examples: seven `WithMax*` resource budgets → `WithLimits`; eight `WithToolFoo` knobs → `WithToolDispatch`; six `WithObserve*` → `WithObservability`. **Audit step:** group every `With*` in the target file by its semantic prefix or noun and count per group. A group of 5+ that you didn't surface as a consolidation finding is a miss — verify before submitting.
- **Special-case sprawl** — string-matched lists of "framework-special" names (`if name == "ask_user" || name == "execute_plan" || ...`) appearing in ≥2 files. These keep growing; consider promoting the concept to a registered capability.
- **Re-export drift** — when an umbrella package re-exports >200 symbols, ask whether the umbrella is still cheaper than direct subpackage imports for users.

### Phase 3 — Validate before reporting

For each candidate, you must be able to fill in all five sections of the output template (see below). If you cannot answer "what could break" or "what invariant is preserved," that's a sign Phase 1 was incomplete — go back and re-read.

The validation rules:
- If the simplification removes a `Why:` comment, the explanation in **Current concept** must address what that comment said, and **Invariants preserved** must explain why the concern no longer applies (or how the new shape addresses it differently).
- If no test covers the area, say so explicitly in **Risk** and propose what test to add. Do not silently assume "it'll be fine."
- If the simplification spans ≥2 files, you must read all of them in Phase 1, not just the headline file.

## Output Format

Target **3-7 findings**, max 10. Quality beats quantity — 3 strong findings with full concept analysis are worth more than 10 partial ones.

For each finding:

```
### <short imperative title>

**Location:** path:line(s) — multiple paths if cross-file.

**Current concept:**
<1-3 sentences. What the code does AND why it's structured this way.
Cite any "Why:" comment, invariant, or test that justifies the current shape.
This is the section that proves you understood before you simplified.>

**Proposed simplification:**
<1-3 sentences describing the change. Concrete enough to picture the diff —
"merge the four LLM-call branches into one helper parameterized by (streaming bool, hasTools bool)"
beats "deduplicate the iteration loop".>

**Reduction:**
<Concrete count. "872 lines → ~500 lines"; "removes WithFoo, WithBar, WithBaz";
"6 spellings of WithLogger → 1 convention". One bullet, no hedging.>

**Invariants preserved:**
<What the current code guarantees that this change must preserve. List them.
For each, name the mechanism by which the new shape still preserves it.
If something is NOT preserved, call it out — that's a breaking change, flag for user.>

**Risk:**
<What's the worst case if the simplification is subtly wrong.
Which tests cover this area (cite test names). What new tests are needed.
"No test coverage" is a valid answer and is itself a finding.>
```

End with **Recommended order**: which findings to do first. The default precedence depends on the project phase you recorded in Phase 0.

**Post-v1 (deprecation cycle required) — risk-ascending order:**

1. **Dead-code removal** (lowest risk — nothing was relying on it)
2. **Shape collapses with full test coverage** (mechanically safe)
3. **Block dedup within a single file** (local blast radius)
4. **Drift fixes between parallel constructors** (often fixes a latent bug as a side effect)
5. **Cross-file consolidation** (touches more callers)
6. **Surface-area shrinks that delete exported symbols** (highest risk — could break out-of-tree users; do last with a migration note)

**Pre-v1 ("break what needs breaking") — DX-first order:**

1. **Runtime-stub elimination** and **principle-violation fixes** (highest user-visible DX win; pre-v1 phase makes breakage acceptable with a migration note)
2. **Exported surface consolidation** — ≥5 same-subsystem `With*` collapsed to one typed sub-config (DX win cited against the project's "consolidate aggressively" / "codegen-friendly" principle; the migration is mechanical and authorized by the pre-v1 phase)
3. **Drift fixes between parallel constructors** (active or latent bugs)
4. **Block dedup within a single file** (local blast radius)
5. **Cross-file consolidation** (mechanical)
6. **Dead-code removal** (safe but lowest impact — do last as cleanup)

Override the order only with explicit justification — and always cite the principle and phase in the justification.

## Anti-Patterns

| Looks like a finding | Why it isn't |
|----------------------|--------------|
| "Rename `X` to `Y`" | Style. No reduction. |
| "Extract this 3-line helper" | Abstraction not earned. Often adds an indirection layer. |
| "Add a comment to clarify" | Doesn't reduce anything. |
| "Use generics here" | Only counts if it collapses ≥3 shapes; otherwise it's taste. |
| "Replace `if` chain with map lookup" | Only counts if the chain is ≥5 entries AND growing. |
| "Move this constant to a const block" | Tidiness, not impact. |
| "Inline this one-line function" | Micro-edit unless it appears in ≥3 places. |
| "Combine these two structs" | Only if they always travel together. Two structs that share 80% of fields but diverge in 1 lifecycle event are NOT the same struct. |

## Red Flags — Start Over

If your report has any of these, you're producing nits or planning to break things:

- You skipped Phase 0 — no project principles cited, no phase (pre-v1 / post-v1) recorded
- A finding without a **Current concept** section that names the invariant
- A finding whose **Reduction** is "feels cleaner" / "more idiomatic" instead of a count or a cited principle
- A finding that erases a `Why:` comment without addressing what the comment said
- A finding that proposes a new abstraction not earned by ≥3 existing repetitions of the pattern
- Any finding where you didn't read the surrounding tests
- A `Risk` section that says "low risk" without naming the test that proves it
- A DX-bug finding (runtime stub, drift, principle violation) demoted to last in a pre-v1 project — that's the wrong phase ordering
- More than 10 findings (you're including filler)
- Zero findings on the **Invariants preserved** line for any item (you didn't think about what could break)
- The target file has ≥5 sibling `With*` options for one subsystem and you produced zero "Exported surface consolidation" findings — count `With*` grouped by semantic prefix/noun before submitting; a group of 5+ that you didn't surface is a top-tier miss, not a judgement call

## Common Rationalizations

| Excuse | Reality |
|--------|---------|
| "This is obviously safe to collapse" | If it's obvious, find the test that proves it. If no test exists, the simplification needs one. |
| "The `Why:` comment is outdated" | Maybe — but confirm with the user before deleting design context. Stale comments are valuable archaeological evidence. |
| "I'll combine these two similar functions" | Similar ≠ same. List the differences first. They may be load-bearing (one streams events, the other doesn't). |
| "Fewer lines is always better" | Only when the contract is preserved. A 1-liner that violates an invariant is a bug, not a simplification. |
| "I have to fill the 10-slot quota" | You don't. 3 strong findings > 10 weak ones. |
| "The user wants thoroughness" | The user wants safe shrinkage. Nits and risky collapses are both noise. |
| "These options aren't broken, just numerous" | If ≥5 sibling `With*` configure one subsystem, the bar is "consolidate aggressively" (a stated principle, not a bug). Exported-surface consolidation is a top-tier DX win — never demote for "not broken." |
| "I'd be deleting working API — too risky to prioritize" | Pre-v1: explicitly authorized with a migration note (cite the phase). Post-v1: same finding becomes a *minor*-version-add of the sub-config option that deprecates the old ones — still ship it, schedule the removal. |
| "I already have 4-5 findings; the surface consolidation is borderline" | It isn't borderline. ≥5 same-subsystem options ≡ a stated principle being violated, which means it ranks above any "duplicate block" / "dead code" finding in your list. Drop the smallest existing finding instead. |
