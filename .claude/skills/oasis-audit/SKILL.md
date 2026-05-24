---
name: oasis-audit
description: Use when asked to improve, audit, scan, review, simplify, consolidate, dedupe, or tighten code in specific Oasis directories or files. Finds net-positive changes across all axes at once — shrinkage, performance, memory, correctness, DX, and principle-conformance — and rejects mono-axis wins that regress another axis. Returns "no findings" when the area is already good.
---

# Oasis Audit

## What "better" means here

A change is "better" when it is **net positive across every axis**, not when it maxes one axis at the cost of others. Mono-optimization is the failure mode this skill exists to prevent: a simplification that adds an allocation in a hot loop is not a win; a perf optimization that adds a layer of indirection and a runtime stub is not a win either. Every finding must show its math on every axis it touches.

The axes this skill weighs together:

- **Performance** — hot-path allocations, reflection, blocking calls on streams, O(N) → O(1) at scale
- **Memory** — bounded growth, no leaks (goroutine, channel, cache), `Close()` on escaping resources
- **Correctness** — silent data loss, unhandled cancellation, swallowed errors, missing context propagation
- **DX** — runtime stubs eliminated, ≥5 same-subsystem `With*` collapsed, drift between parallel constructors fixed, `any` / `interface{}` removed at exported boundaries
- **Shrinkage** — pure code reduction (lines, symbols, layers of indirection)
- **Principle conformance** — fixes a violation of PHILOSOPHY.md or ENGINEERING.md, or surfaces a useful convention not yet documented

It is valid — and expected — for a run to return **no findings**. "This area is already good" is a real outcome. Do not invent findings to fill a quota.

## When to Use

- User points at a directory or file and asks to **improve**, **audit**, **scan**, **review**, **simplify**, **consolidate**, **combine**, **dedupe**, or **tighten**
- After a feature ships and the area accumulated optionality or branches
- Before tagging a release
- When a file passes ~800 lines, an interface passes ~10 methods, or an option list passes ~15 entries

**Do NOT use for:**
- One-off bug fixes the user already identified — fix it directly
- Style / format cleanup — `gofmt` / `golangci-lint` handle that
- Green-field design — use `superpowers:brainstorming`
- Code review of a specific diff — use `code-review` or `superpowers:requesting-code-review`
- Debugging a specific failure — use `superpowers:systematic-debugging`

## Multi-area dispatch (use subagents)

If the audit target spans **>1 directory** OR **>10 files**, do NOT scan it all yourself — partition and dispatch parallel subagents. One sloppy hand-rolled subagent prompt loses the skill's discipline; a structured dispatch keeps it.

**When to dispatch:**
- Multiple directories (e.g. `agent/`, `network/`, `internal/`)
- Single large directory with >10 files
- An area where you already suspect distinct concerns per file cluster

**When NOT to dispatch (do it yourself):**
- Single directory with ≤10 files
- A single specific file
- A "look at this one function" request

**Partition rule:** one subagent per directory, OR one subagent per tightly-coupled file cluster within a giant directory. Never split a single file across subagents. Never overlap — each file belongs to exactly one subagent.

**Dispatch each subagent in parallel** (multiple `Agent` tool calls in one message). Use:

- `subagent_type`: `general-purpose` (or `claude`)
- `model`: `sonnet` by default; `opus` for architecturally dense areas (core types, agent loop); `haiku` only for trivial slices
- `description`: short, e.g. `Audit agent/ slice 1 of 2`
- `prompt`: the dispatch template below

### Subagent dispatch prompt template

```
Invoke the oasis-audit-subagent skill.

Target:
  - <file or dir path>
  - <file or dir path>
  - ...

Exclude (covered by sibling subagents):
  - <file path>
  - <file path>
  - ...

Priority hints (optional, omit if none):
  - <e.g. "agent/agent.go has ≥10 With* options — count by semantic prefix">
  - <e.g. "suspect drift between agent.New and Network.New config builders">
```

Send that prompt verbatim — substitution only, no paraphrasing. The subagent will read `.claude/skills/oasis-audit-subagent/SKILL.md` itself; do NOT inline the audit recipe into the prompt.

### After subagents return

Subagents return flat axis-tagged finding lists. **You** assemble the final report:

1. **Dedupe** — if two subagents flagged the same cross-file pattern, fold into one finding with both Locations listed
2. **Sort** — apply the Ranking precedence below
3. **Section** — Ship list / Tradeoff list / Converged (see Output Format)
4. **Cap** — 10 findings max across the merged result; cut the weakest if over
5. **Empty-result handling** — if all subagents reported "Converged on `<slice>`", produce one Converged section listing each slice

## The Unified Threshold

A finding qualifies only if **all three** are true:

1. **Meaningful win on at least one axis** — concrete and quantified, not "feels cleaner":
   - Performance: removes a class of allocation in a hot path, eliminates reflection in the run loop, changes O(N) → O(1), removes a blocking call on the stream path
   - Memory: fixes a leak that compounds over time, adds a bound to something previously unbounded
   - Correctness: fixes silent data loss, unhandled cancellation, unbounded retry, error swallowing
   - DX: eliminates a runtime stub, collapses ≥5 sibling `With*` options for one subsystem into one typed sub-config, fixes drift between parallel constructors, removes `any` / `interface{}` at an exported boundary
   - Shrinkage: ≥30 lines removed at one location, ≥1 exported symbol deleted, ≥3 inconsistent shapes collapsed to 1, ≥1 layer of indirection removed
   - Principle conformance: violates a stated rule in PHILOSOPHY.md / ENGINEERING.md AND the violation affects multiple call sites

2. **No unmitigated regression on another axis.** If the change costs something on another axis, the primary win must be ≥3× the cost AND the tradeoff must be called out in the `Costs on:` and `Net:` fields. Findings with hidden costs are dropped.

3. **Quantified.** Line count, allocation count, symbol count, drift bug fixed, principle line cited. "Cleaner" / "more idiomatic" / "nicer" are not quantities.

## Process

### Phase 0 — Read the project principles (MANDATORY before Phase 1)

Before scanning a single Go file, read in this order:

1. **`CLAUDE.md`** at the repo root — project-specific rules and pointers
2. **`docs/PHILOSOPHY.md`** — framework identity, API strategy, design opinions
3. **`docs/ENGINEERING.md`** — coding standards, performance rules, things to never do

Record before continuing:
- Which principles govern the area you're scanning
- Whether the project is pre-v1 (break-what-needs-breaking) or post-v1 (deprecation cycle required) — this changes the recommended order
- Specific phrases you'll cite when ranking findings (e.g. *"Fail at compile time, not runtime"*, *"Consolidate aggressively"*, *"Extract only when a pattern repeats 3x"*)

**You cannot skip Phase 0.** A change that looks safe by line-count but contradicts a stated principle is a bad finding; a change that looks risky in the abstract but fixes a documented violation is a *priority* finding for that project.

### Phase 1 — Understand the concept (MANDATORY before any finding)

For each area you intend to touch:

1. **Read the contract.** What's exported? Which callers depend on the current shape? Use `codedb_callers` / `codedb_word` to find real call sites.
2. **Read every `Why:` comment in the area.** These are design-decision records — someone hit the failure mode the comment describes. A change that erases a `Why:` without addressing what the comment said is a regression in disguise.
3. **Map the invariants.** Concurrency boundaries (which lock guards what), lifecycle (who creates / who closes), ordering (does A have to happen before B), error semantics (recoverable vs fatal vs business failure).
4. **Locate the tests.** A behavior covered by a test is part of the de facto contract. Note which tests would catch a break. **No tests covering the area is itself a finding** — surface it.
5. **Identify what's load-bearing.** Code that looks duplicated across branches often differs in subtle ways: one path emits a stream event, another doesn't; one path applies middleware, another bypasses it; one is the hot loop, another is the error tail. List these differences before proposing any collapse.

**Self-check before leaving Phase 1:** Can you describe in one sentence each (a) what this block does, (b) why it's structured this way, (c) what would observably break if you naively changed it? If any is fuzzy, read more.

### Phase 2 — Sweep BOTH lenses at once

Use codedb to map the area: `codedb_tree` for structure, `codedb_hot` for recent churn, `codedb_outline` on the largest files. Then sweep for both kinds of patterns in a single pass — do not run one lens, then the other.

**Shrinkage / DX patterns:**
- **Duplicate blocks** — same structure across branches/files; confirm differences are cosmetic, not load-bearing
- **Drift-prone parallel builders** — two functions constructing the same shape from different inputs; diff their field lists exhaustively. If one is missing a field the other sets, that's an active or latent bug
- **Runtime stubs that promise capability** — methods that exist solely to error on every meaningful input (`ExecuteWith` that rejects all `opts.HasOverrides()`; options whose setter is wired but whose effect is never read). Either implement or remove
- **Surface bloat** — sibling exported options (`With*`, setters) for knobs of *one subsystem*. **≥5 entries qualifies as a top-tier finding under "Exported surface consolidation"** — group every `With*` in the target file by semantic prefix/noun and count per group; a group of 5+ that you didn't surface is a miss
- **Shape proliferation** — same conceptual operation spelled multiple ways across packages (`WithLogger` × 6, `WithRetrieverLogger`, `WithChunkerLogger`)
- **Ping-pong field copies** — struct A's job is to be assigned field-by-field into struct B with identical data
- **Dead or shadowed declarations** — package constants overridden by configurable knobs; fields written but never read (or vice versa); unreachable branches
- **Special-case sprawl** — string-matched lists of "framework-special" names (`if name == "ask_user" || name == "execute_plan" || ...`) appearing in ≥2 files
- **Re-export drift** — umbrella package re-exports >200 symbols; ask whether the umbrella is still cheaper than direct imports

**Performance / memory / correctness patterns:**
- `reflect.*` in hot paths · `fmt.Sprintf` in the run loop · JSON marshal/unmarshal in the agent loop · unbuffered channels on streaming paths
- `make(map` / `make(chan` without bounds · slices that grow forever · goroutines without `select { case <-ctx.Done(): }` · missing `Close()` on resources that escape scope
- `_ = err` · missing context propagation · `panic(` in library code · retry without backoff or cap

**Principle drift (both directions):**
- Cross-check every "Things to Never Do" entry in ENGINEERING.md against current code
- Cross-check every section of PHILOSOPHY.md against current behavior
- Surface code that embodies a useful convention not yet in the docs — that's also drift

### Phase 3 — Score, filter, and sort

For each candidate, fill in the full template (below). If you cannot answer **Wins on**, **Costs on**, **Invariants preserved**, or **Risk**, Phase 1 was incomplete — go back and re-read.

Apply the unified threshold. Drop anything that:
- Has a regression on another axis without ≥3× win on the primary
- Has no concrete number in `Wins on`
- Has no test coverage AND you didn't propose what test to add
- Erases a `Why:` comment without addressing what it said
- Proposes a new abstraction not earned by ≥3 existing repetitions

Sort into the three output sections (Ship / Tradeoff / Converged — see Output Format).

**If no candidates survive the filter, the result is `Converged`. This is expected and welcomed. Do not invent findings to fill a quota.**

## Output Format

Target **3-8 findings total**, max 10. Quality beats quantity — 3 strong findings beat 10 partial ones. Zero findings (Converged) is a valid and welcomed outcome.

Structure the report as three sections:

### Ship list
Findings with `Costs on: none` or with clear net wins. Safe to apply.

### Tradeoff list
Findings with a real win AND a real cost on another axis where the primary win is ≥3× the cost. Surfaced for the user to decide — not auto-shipped.

### Converged
Used when no findings qualify. The report consists of a one-line summary per area scanned: e.g. "scanned `agent/loop.go`, `agent/options.go` — no findings worth shipping; principle alignment verified; no drift detected."

Per-finding template:

```
### <short imperative title>

Location:   path:line(s) — multiple paths if cross-file

Current:    <1-3 sentences — what the code does AND why it's structured this way.
            Cite any `Why:` comment, invariant, or test that justifies the current shape.
            This is the section that proves you understood before you changed.>

Change:     <1-3 sentences describing the change. Concrete enough to picture the diff —
            "merge the four LLM-call branches into one helper parameterized by
            (streaming bool, hasTools bool)" beats "deduplicate the iteration loop".>

Wins on:    <axis — concrete number. "Performance — removes 1 allocation per agent step";
            "Shrinkage — 872 lines → ~500 lines"; "DX — 7 With* options → 1 WithLimits()".>

Costs on:   <axis — concrete cost, or "none". "Memory — adds one 4KB buffer per agent";
            "DX — introduces one new typed Limits struct callers must construct";
            "none".>

Net:        <Why the win beats the cost. SKIP this line if Costs on: none.
            Quantify: "5× allocation drop vs 1 new typed struct"; "removes a runtime
            stub vs a one-line constructor change for ~3 in-tree callers".>

Invariants: <What the current code guarantees that this change must preserve. List them.
            For each, name the mechanism by which the new shape still preserves it.
            If something is NOT preserved, call it out — flag it as a breaking change.>

Risk:       <What's the worst case if the change is subtly wrong.
            Which tests cover this area (cite test names). What new tests are needed.
            "No test coverage" is a valid answer and is itself a finding.>
```

## Ranking

Default precedence inside the Ship list:

1. **Correctness / bugs** (always first — they're never net-negative)
2. **Principle-violation fixes** with `Costs on: none`
3. **Free wins on perf / memory / DX / shrinkage** (`Costs on: none`)
4. **DX consolidations** — especially ≥5 same-subsystem `With*` → typed sub-config — when the project is pre-v1; the migration is mechanical and authorized by the phase

In the Tradeoff list: sort by net impact (the ratio of win to cost).

For post-v1 projects, demote breaking-surface findings unless paired with a deprecation cycle plan; pre-v1 projects keep them in their natural rank.

Override the order only with explicit justification — and always cite the principle and phase in the justification.

## Anti-Patterns

| Looks like a finding | Why it isn't |
|----------------------|--------------|
| "Rename `X` to `Y`" | Style. No reduction, no axis win. |
| "Extract this 3-line helper" | Abstraction not earned; often adds an indirection layer. |
| "Add a comment to clarify" | Doesn't move any axis. |
| "Use generics here" | Only counts if it collapses ≥3 shapes; otherwise it's taste. |
| "Replace `if` chain with map lookup" | Only counts if the chain is ≥5 entries AND growing. |
| "Move this constant to a const block" | Tidiness, not impact. |
| "Inline this one-line function" | Micro-edit unless it appears in ≥3 places. |
| "Use `strings.Builder` for one concat" | Micro-opt outside a hot path. |
| "Add error wrapping" | Doesn't change a class. |
| "Combine these two structs" | Only if they always travel together. 80% field overlap with one lifecycle divergence is NOT the same struct. |
| "Update godoc wording" | Editorial. |

## Red Flags — Start Over

If your report has any of these, you're producing nits or planning to break things:

- You skipped Phase 0 — no project principles cited, no phase (pre-v1 / post-v1) recorded
- A finding without a **Current** section that names the invariant
- A finding whose **Wins on** is "cleaner" / "more idiomatic" / "modern" instead of a count or a cited principle
- A finding without a **Costs on** line (every finding has costs to consider, even if the answer is "none")
- A finding that erases a `Why:` comment without addressing what the comment said
- A finding that proposes a new abstraction not earned by ≥3 existing repetitions
- Any finding where you didn't read the surrounding tests
- A `Risk` section that says "low risk" without naming the test that proves it
- A DX-bug finding (runtime stub, drift, principle violation) demoted to last in a pre-v1 project — that's the wrong phase ordering
- More than 10 findings (you're including filler)
- Empty `Invariants preserved` for any item (you didn't think about what could break)
- The same finding repeated across multiple files (collapse it into one)
- Zero findings on Phase 0 / principle drift across the whole report (you probably didn't read the docs)
- The target file has ≥5 sibling `With*` options for one subsystem and you produced zero "Exported surface consolidation" findings — count `With*` grouped by semantic prefix/noun before submitting

## Common Rationalizations

| Excuse | Reality |
|--------|---------|
| "This is obviously safe to collapse" | If it's obvious, find the test that proves it. If no test exists, the change needs one. |
| "The `Why:` comment is outdated" | Maybe — but confirm with the user before deleting design context. Stale comments are valuable archaeology. |
| "I'll combine these two similar functions" | Similar ≠ same. List the differences first. They may be load-bearing (one streams events, the other doesn't). |
| "Fewer lines is always better" | Only when no other axis regresses. A 1-liner that violates an invariant is a bug. |
| "Faster is always better" | Only when no other axis regresses. A 5% speedup that adds a runtime stub is net-negative. |
| "I have to fill the 10-slot quota" | You don't. 3 strong findings > 10 weak ones. Zero is fine. |
| "But this *is* a meaningful improvement" | If you can't name the axis AND quantify the win in one sentence, it isn't. |
| "Documentation issues are easy wins" | Only if the doc is *wrong*, not if it could be *better written*. |
| "These options aren't broken, just numerous" | ≥5 sibling `With*` configuring one subsystem violates "consolidate aggressively." Top-tier DX win, not a footnote. |
| "I'd be deleting working API — too risky to prioritize" | Pre-v1 phase explicitly authorizes it with a migration note. Post-v1: ship as a minor-version-add of the typed sub-config that deprecates the old options; still schedule the removal. |
| "The cost on the other axis is tiny" | Then say so in `Costs on:`, quantify it, and show the win is ≥3×. Don't hide it. |
| "I already have 4-5 findings; this borderline one fits" | If it's borderline, it doesn't ship. Lower count is fine. Zero is fine. |
