# Engineering Principles

These principles shape how we think when writing code in Oasis — at both the framework and application level. This is not a checklist of patterns or a style guide (that's in [CONVENTIONS.md](CONVENTIONS.md)). This is the **mental model** behind every engineering decision.

## The Big Picture

Oasis is an AI agent framework designed to evolve as AI capabilities grow toward AGI. The primitives we design today — `Agent`, `Tool`, `Provider`, `Network` — must still make sense when agents can spawn sub-agents, manage their own memory, select their own tools, and negotiate with peers.

This means every design decision asks two questions:

1. **Does this solve today's problem well?**
2. **Will this still work when agents get 10x smarter?**

If the answer to #2 is "no" or "unclear," invest more design time. A primitive that breaks under increased capability is worse than no primitive at all.

## Framework Primitives vs Application Code

Oasis has two distinct engineering modes:

- **Framework primitives** (core interfaces, agent model, tool protocol) — designed for **composability, expressiveness, and longevity**. Correct primitives unlock patterns we haven't anticipated. Wrong primitives become expensive breaking changes. Invest in getting the design right.
- **Application code** (tool implementations, provider adapters, frontends) — designed for **simplicity and pragmatism**. Concrete first, refactor later. Working > perfect.

When designing a new interface or primitive, ask: "what patterns does this unlock for users — including future users with smarter agents?" not "what's the simplest way to implement this?" When implementing a feature on top of existing primitives, ask: "what's the simplest correct approach?"

## 1. Design for the Future, Don't Break the Past

AI capabilities grow in jumps. The framework must be ready for the next jump without breaking what already works.

**Future-readiness:**
- **Design interfaces that accommodate autonomy.** Today's agents follow explicit tool-calling loops. Tomorrow's agents may dynamically discover tools, spawn sub-agents, or negotiate task delegation with peers. Interfaces should not assume a fixed execution pattern.
- **Keep protocol types open for extension.** Prefer structs with optional fields over rigid signatures. Adding a field to `AgentTask.Context` is non-breaking. Adding a parameter to `Agent.Execute()` is breaking.
- **Don't hardcode today's limitations.** If current LLMs can't do X but the interface could support X without extra complexity, design for X. The framework should be ahead of the models, not behind.
- **Think in composability.** An Agent that contains Agents. A Tool that wraps an Agent. A Network that routes to Networks. Recursive composition is how simple primitives produce complex behavior — and how today's framework supports tomorrow's use cases.

**Forward-compatibility:**
- **Add, don't remove.** New methods extend interfaces. New fields extend structs. Existing signatures don't change.
- **Extend via composition, not modification.** Need a `Provider` that also does embeddings? That's a separate `EmbeddingProvider` interface, not extra methods on `Provider`. Need an `Agent` with streaming? That could be a `StreamingAgent` interface, not a modified `Agent.Execute()`.
- **Deprecate before deleting.** Mark deprecated with comments and a note on what replaces it. Remove in the next major version, not immediately.
- **Defaults preserve behavior.** When adding a new config field or struct field, the zero value must produce the same behavior as before the field existed.
- **Optional capabilities via interface assertion.** If only some implementations support a capability, use a separate interface and type-assert at runtime. Don't pollute the base interface.

```go
// Good: optional capability via separate interface
type StreamingAgent interface {
    Agent
    ExecuteStream(ctx context.Context, task AgentTask, ch chan<- string) (AgentResult, error)
}

// Check at runtime
if sa, ok := agent.(StreamingAgent); ok {
    return sa.ExecuteStream(ctx, task, ch)
}

// Bad: breaking change to base interface
type Agent interface {
    Execute(ctx context.Context, task AgentTask, ch chan<- string) (AgentResult, error)
    // ^^^ every existing implementation now broken
}
```

**Litmus test:** if a new AI capability drops tomorrow (e.g., agents that can fork/join, agents with persistent state, agents that learn from feedback), can the framework accommodate it without breaking existing code?

## 2. Earn Every Abstraction, Design for Composability

Abstraction is debt — but the right abstraction at the right boundary is an investment. Know the difference.

**For application code:** write concrete code first. Extract only when a pattern repeats 3x. Three similar lines are better than one premature abstraction. No `utils`, `helpers`, or `common` packages. A new interface needs at least 2 implementations that exist or are clearly imminent — one implementation means use a concrete type.

**For framework primitives:** core interfaces (`Provider`, `Tool`, `Agent`) and protocol types (`ChatRequest`, `ToolResult`) *are* the abstraction. They don't wait for 3x repetition — they're designed for **composability and expressiveness** from the start, because getting them wrong = expensive breaking changes. Ask: "what patterns does this primitive unlock?" not "what's the simplest way to solve today's use case?"

**Composability principles:**
- **Interfaces at natural boundaries.** Right place: between your system and an external service (LLM, database, messaging platform). Wrong place: between two internal functions that always change together.
- **Depend on behavior, not implementation.** Consumers shouldn't care whether storage is SQLite or Postgres — they care that `SearchChunks` returns top-K results.
- **Configuration, not conditionals.** If behavior needs to change, make it configurable. Don't hardcode then `if/else` later.
- **Composable primitives.** Agents with sub-agents. Tools that wrap agents. Structured or free-form output. Design for **composability**, not for one specific use case.
- **Expressiveness over simplicity at the framework layer.** If adding one field or method opens 5 new use cases without significant complexity, it's worth it. Simplicity that sacrifices expressiveness at the framework level = hidden technical debt.

**Test:** for application code, if you remove the abstraction and inline the code, does readability suffer? If not, it isn't needed. For framework primitives: can this primitive compose to solve use cases that haven't been imagined yet?

## 3. Optimize for the Reader

Code is written once, read many times — by humans AND by LLMs. Both audiences matter equally.

**For all readers:**
- Names explain *intent*, not *implementation*. `BuildContext` over `GetTop15FactsAndFormat`.
- Comments explain **why**, not **what**. If a comment just restates the code, delete it.
- Top-to-bottom flow. Early return for edge cases at the top, happy path below. Max 2 levels of nesting.
- One file, one concern. If you need to scroll extensively to understand a file, it's too large.
- A well-named short function beats a function with long comments.

**For LLM readers (code generators):**
- **Godoc on every exported symbol.** Not just "returns X" — explain the contract. What are valid inputs? What does it guarantee? When should you use this vs an alternative?
- **Interface contracts in comments.** Document invariants, thread-safety guarantees, and expected behavior for nil/zero inputs. LLMs use these to generate correct implementations.
- **Examples in test files.** `Example*` functions are both documentation and tests. They show LLMs the expected usage pattern.
- **Consistent patterns.** When every Tool follows the same Execute pattern, every Provider follows the same constructor pattern, and every test uses the same table-driven structure, LLMs generate correct code on the first try.

## 4. Make It Fast Where It Matters

Performance isn't about micro-optimizing everything. It's about **knowing where the bottlenecks are** and only optimizing there.

**Optimize:**
- **User-perceived latency** — if the user is waiting, it's a problem. Stream responses instead of buffering. Background work that doesn't need to block the user.
- **External API calls** — every HTTP call is expensive (100ms+). Batch when possible. Don't call APIs in loops when you can call once outside the loop.
- **Memory in hot paths** — if a function is called thousands of times, watch allocations. If called once at startup, don't bother.

**Don't optimize:**
- Startup time. 500ms boot delay doesn't matter.
- Code that runs once per request and is already fast. Don't optimize 1ms to 0.5ms.
- "Could be more efficient with X" — if the current approach is fast enough, don't refactor for marginal gain.

**Rule of thumb:** if you can't measure the difference, the optimization doesn't matter.

## 5. Fail Gracefully, Recover Automatically

A good system isn't one that never errors — it's one that **handles errors elegantly** and keeps functioning.

- **Never crash on recoverable errors.** If one subsystem fails, others must keep running. Memory extraction fails? Chat continues without memory. Embedding fails? Store the message without embedding.
- **Distinguish transient vs permanent.** Transient errors (429, 5xx, timeout) are retried with backoff. Permanent errors (400, 404, invalid input) return immediately. Never retry permanent errors.
- **Degrade, don't die.** Better to send a response without memory context than no response at all. Better to send plain text than crash on HTML formatting failure.
- **Users need direction, not details.** On error, give an actionable message ("please try again"), not a stack trace.

## 6. Own Your Dependencies

Every dependency you add is someone else's code that you maintain. Treat dependency addition like a hire — it needs strong justification.

**Questions before adding a dependency:**
1. Can the standard library solve this?
2. Is a hand-rolled solution under 200 lines?
3. Do we need more than 30% of the library's features?
4. Is the library actively maintained?
5. How many transitive dependencies does it pull?

If the answer to #1 or #2 is yes, don't add the dependency. 50 lines of your own code beats a 5000-line dependency you use 1% of.

**For external APIs: no SDKs.** SDKs add heavy coupling to specific versions, are often bloated, and hide what's happening at the wire level. Raw HTTP + JSON gives full control and visibility. When an API changes, you update one file — not upgrade a major SDK version.

## 7. Explicit Over Magic, Respect the User's Time

Clear, verbose code beats short, "magical" code. Every developer (human or LLM) who touches this code deserves clarity.

- **No hidden side effects.** If a function changes state, that must be obvious from its name and signature. `StoreMessage` clearly stores. `Process` is ambiguous.
- **Constructor injection, not service locator.** Dependencies must be visible in function signatures, not silently resolved from a global registry.
- **Prefer parameters over ambient state.** Pass timezone offset as a parameter, don't read from a global. Pass context explicitly, don't rely on goroutine-local storage.
- **Predictable config cascade.** defaults -> file -> env vars. No undocumented "magic overrides."
- **Misuse should be hard to write.** If a consumer can forget an important step, make the compiler or runtime catch it — not a comment.
- **Error messages must be actionable.** "invalid args" is useless. "invalid args: expected string for 'query' field" gives direction.
- **Sensible defaults.** A user who just cloned the repo should be able to run with minimal config. If 20 env vars must be set before anything works, onboarding has failed.
- **Living documentation.** Outdated docs are more dangerous than no docs. If you change behavior, update the docs. If you add a feature, document it now — not "later."

## 8. Ship With Confidence

Better to ship 3 small correct changes than 1 large possibly-wrong change. Testing gives you the confidence to ship fast.

**Shipping:**
- **One PR, one concern.** Don't mix refactoring with new features. Don't mix bug fixes with "improvements" elsewhere.
- **Every commit should be revertable.** If your commit is reverted, the system must still function.
- **No speculative refactoring.** "While I'm here, let me clean this up" during another feature = risk without value. Refactoring is a separate task.
- **Working > perfect — for application code.** Ship the 80% correct solution today, improve tomorrow. Don't block features on ideal architecture.
- **Right > fast — for framework primitives.** Core interfaces must be designed correctly from the start. Changing the `Provider` interface after 5 implementations exist is expensive. This isn't perfectionism — it's avoiding breaking changes that cascade.

**Testing:**
- **Test behavior, not implementation.** Test that the chunker produces correct output for given input. Don't test that the chunker internally calls `splitOnSentences`.
- **Pure functions first.** Functions without side effects are the easiest and most valuable to test. Prioritize these.
- **Don't mock unless forced.** If you need to mock 5 dependencies to test one function, that function does too much. Refactor, don't mock.
- **Edge cases > happy path.** Happy paths are usually obviously correct. The interesting ones: empty input, nil values, concurrent access, boundary conditions.
