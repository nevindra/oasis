---
name: high-impact-audit
description: Use when asked to audit, scan, or review specific directories in Oasis for major (3-10x) improvements in performance, memory safety, DX, bugs, or maintainability — and to verify code still matches PHILOSOPHY.md / ENGINEERING.md. Filters out minor nits; only surfaces changes with substantial payoff.
---

# High-Impact Audit

## Overview

Scan one or more directories the user names and report only changes worth making. The bar is **3-10x improvement** on at least one concrete axis. Anything smaller is dropped — no exceptions. This skill exists to prevent the common failure mode of producing 30 nits that bury the 5 changes that actually matter.

Also verifies that **PHILOSOPHY.md** and **ENGINEERING.md** still describe the code accurately. Drift in either direction is a finding: code violating a documented rule, or code embodying a useful convention not yet documented.

## When to Use

- User points at a directory or directories and asks for an audit, scan, review, or improvement pass
- After a major refactor, before tagging a release
- User mentions wanting to find perf / memory / DX / bugs / maintainability wins

**Do NOT use for:**
- One-off bug fixes the user already identified
- Reformatting, linter findings, or style nits
- Green-field design — use `superpowers:brainstorming` instead

## The 3-10x Threshold

A finding qualifies only if it meets ONE of these:

- **Performance** — removes a class of allocation in a hot path · eliminates reflection or JSON marshaling in the run loop · changes O(N) → O(1) at scale · removes a blocking call on the streaming path. Backed by a benchmark or a mechanical argument.
- **Memory** — fixes a leak (goroutine, channel, cache) that compounds over time · removes an unbounded growth source · adds a bound to something previously unbounded.
- **DX** — removes a class of misuse (API humans and LLMs consistently get wrong) · replaces a runtime "missing field" error with a compile-time signature · collapses ≥3 inconsistent shapes into one · removes `any` / `interface{}` at an exported boundary.
- **Bugs** — silent data loss · unhandled cancellation · unbounded retry · missing `Close()` on a resource that escapes scope · error swallowing in a code path that runs in production.
- **Maintainability** — ≥3 similar implementations that can collapse to one primitive · a file >800 lines doing >1 job · a pattern repeated ≥5 times that has earned its abstraction.
- **Philosophy drift** — code violates a rule in PHILOSOPHY.md / ENGINEERING.md AND the violation affects multiple call sites · docs claim a behavior the code no longer has · code embodies a useful convention not yet in the docs.

If a finding doesn't fit one of these, drop it. "Could be slightly faster" is not a finding. "Slightly clearer name" is not a finding.

## Process

1. **Read the contract.** `docs/PHILOSOPHY.md`, `docs/ENGINEERING.md`, `CLAUDE.md`. The audit is against this contract, in both directions.
2. **Map the directory.** Use codedb: `codedb_tree` for structure, `codedb_hot` for recent churn, `codedb_outline` on the largest files. Don't grep blindly.
3. **Sweep each axis** with targeted searches:
   - **Performance:** `reflect.`, `fmt.Sprintf` in hot paths, JSON marshal/unmarshal in the agent loop, unbuffered channels on streaming paths.
   - **Memory:** `make(map`, `make(chan` without bounds, slices that grow forever, goroutines without `select { case <-ctx.Done(): }`, missing `Close()` on resources that escape.
   - **DX:** exported signatures with `any` / `interface{}`, inconsistent verbs (`Get` vs `Find` vs `Lookup` for the same operation), required fields hidden inside option structs, magic strings instead of typed constants.
   - **Bugs:** `_ = err`, missing context propagation, `panic(` in library code, retry without backoff or cap.
   - **Maintainability:** file sizes (>800 lines is suspect), patterns repeated across packages.
   - **Philosophy:** cross-check every "Things to Never Do" entry against code. Cross-check every section of PHILOSOPHY against current behavior.
4. **Apply the threshold.** For each candidate, answer: "If I fix this, what is the user-visible or maintainer-visible payoff?" If the answer isn't 3-10x of *something* concrete, drop it.
5. **Verify before reporting.** Read the actual code at each location. No drive-by claims based on grep output alone.

## Output Format

For each finding (target **3-8 total**, max 10):

```
### <short imperative title>
**Axis:** Performance | Memory | DX | Bugs | Maintainability | Philosophy
**Location:** path:line(s)
**Impact:** <what improves, by how much, why this is 3-10x not 1.2x>
**Change:** <one-paragraph description of the fix>
**Risk:** <what could break, how to mitigate>
```

End with a **Recommended order** — which findings to address first. Default precedence: bugs → philosophy drift → DX → performance → memory → maintainability. Override only with explicit justification.

## Anti-Patterns

| Looks like a finding | Why it isn't |
|----------------------|--------------|
| "Could rename `X` to `Y`" | Style. Not 3-10x. |
| "Add a comment here" | Doc nit. Not 3-10x. |
| "Use `strings.Builder` for one concat" | Micro-opt outside a hot path. |
| "Extract this 3-line helper" | Abstraction not earned. |
| "Add error wrapping" | Doesn't change a class. |
| "Update godoc wording" | Editorial. |
| "Move this constant to a const block" | Tidiness, not impact. |

## Red Flags — Start Over

If your report has any of these, you're producing nits:

- More than 10 findings
- Any finding without a `path:line(s)`
- Any finding whose **Impact** uses "cleaner", "nicer", "more idiomatic", "modern"
- Any finding you didn't open and read the actual code for
- The same finding repeated across multiple files (collapse it into one)
- Zero findings on the Philosophy axis (you probably didn't read the docs)

## Common Rationalizations

| Excuse | Reality |
|--------|---------|
| "But this *is* a meaningful improvement" | If you can't name the 3-10x axis in one sentence, it isn't. |
| "I have to fill 10 slots" | You don't. 3 strong findings > 10 weak ones. |
| "I'll include the small ones too, just FYI" | No. The point of this skill is the filter. |
| "The user wants thoroughness" | The user wants signal. Nits are noise. |
| "Documentation issues are easy wins" | Only if the doc is *wrong*, not if it could be *better written*. |
