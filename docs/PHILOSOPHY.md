# Philosophy

Oasis is *the* Go framework for AI agent systems that have to run in production and scale as models get smarter.

The framework is the product. It supplies primitives and contracts so humans and AI coding assistants can build agent applications on top of it. It is not an app shell, not a harness, not a runtime — those are downstream of the framework, not part of it.

This document defines how we think. For concrete implementation rules, see [ENGINEERING.md](ENGINEERING.md).

> **Current phase: pre-v1.0.0** — breaking changes are expected and encouraged when they improve the final API surface. Each breaking change requires a migration note in the PR description.

---

## The Four Constraints

Oasis is shaped by four constraints. They are not ranked. They apply simultaneously.

- **Fast.** Performance is the moat. The run loop, tool dispatch, and streaming paths are hot. Speed translates directly into agent throughput, lower cost, and lower latency for the user.
- **Best-in-class DX.** Building an agent should take ten lines, not a hundred. APIs are designed for humans *and* for LLM coding assistants writing code with no prior Oasis context.
- **Future-Ready.** Agents today follow tool-calling loops. Tomorrow they spawn sub-agents, negotiate delegation, discover tools at runtime. The framework must not foreclose any of this.
- **Safe and Recoverable.** Autonomous agents can't page a human on every failure. Errors must be observable, recovery must be fast, and shutdown must drain in flight — not drop.

**If a design forces a trade-off between these four, the design is wrong — not the constraints.** A fast API that's painful to use is a failure. A safe API that's slow is a failure. A clean API that locks out tomorrow's capabilities is a failure. When the four pull in different directions, that's the signal to rethink the design — not to pick a winner.

---

## API Strategy

Every exported symbol is a permanent contract. The API surface must earn its place.

1. **Consolidate aggressively.** If two interfaces overlap or can become one without losing expressiveness, merge them. Fewer, more powerful primitives beat many narrow ones.
2. **Every export is a commitment.** New exported symbols require a design doc in `docs/plans/`. The bar: does this *need* to be public, or can it stay internal?
3. **Pre-v1: break what needs breaking.** Rename for clarity, change signatures for consistency, restructure packages for coherence. The cost of a breaking change now is a migration note. The cost of a wrong API in v1 is forever.
4. **Post-v1: standard semver, with deprecation cycles.**
   - **Patch (`x.y.Z`):** bug fixes only. No new exports.
   - **Minor (`x.Y.0`):** add, never break. New types, new fields with safe zero values, new optional-capability interfaces.
   - **Major (`X.0.0`):** breaking changes allowed, but each one requires a deprecation cycle — at least one prior minor release marks the old API deprecated, plus a migration note in the changelog. No surprise breaks within a major. No removal without prior deprecation.

---

## Designing for the Next Leap

Every design decision asks: **will this still work — and work fast — when agents are 10x more capable?**

- **Recursive composition.** An Agent that contains Agents. A Tool that wraps an Agent. A Network that routes to Networks. Recursive composition is how simple primitives produce emergent behavior — and how today's framework keeps working when tomorrow's agents need to spawn, delegate, and supervise.
- **Open protocol types.** Prefer structs with optional fields over rigid signatures. Adding a field is non-breaking; adding a parameter is breaking.
- **Optional capabilities via interface assertion.** Don't force every implementation to satisfy every future capability. Check at runtime instead.
- **Don't foreclose dynamic behavior.** Sub-agent spawning, dynamic tool discovery, runtime negotiation — these are coming. Interfaces should not assume a fixed execution pattern.

```go
// Optional capability via separate interface
type StreamingAgent interface {
    Agent
    ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error)
}

// Check at runtime — existing code never breaks
if sa, ok := agent.(StreamingAgent); ok {
    return sa.ExecuteStream(ctx, task, ch)
}
```

---

## Opinions Where They Earn Their Keep

Oasis is opinionated about *correctness invariants* and *what makes hot paths fast*. It is unopinionated about *what your app looks like*.

**Opinionated about the internals:**

- The execution loop, memory pipeline, suspend/resume, and message assembly are framework-owned. These pieces are optimized as a unit — crossing internal boundaries for flexibility would compromise performance and reliability.
- Goroutine discipline, channel safety, context propagation, memory bounding — non-negotiable. The framework enforces them so users don't have to remember them.

**Headless about the app:**

- Agent topology, supervision strategy, memory strategy, tool shape — your call. The framework doesn't dictate how many agents you run, how they coordinate, or what your domain looks like.
- Integration points are open interfaces — Provider, Tool, Processor, Store, SkillProvider. Bring any LLM. Bring any persistence. Bring any guardrail.

**Why this split:** the core changes together and is optimized together. The edges vary per deployment and must be swappable. Forcing composability on the core adds indirection that costs performance. Forcing opinions on the edges limits adoption.

**The rules:**

- Interfaces at integration boundaries — between the framework and external services, not between internal components that always change together.
- Depend on behavior, not implementation. Consumers shouldn't care whether storage is SQLite or Postgres.
- Explicit dependencies. Constructor injection, not service locator. Dependencies visible in function signatures. No hidden side effects.
- Earn every abstraction. Write concrete code first. Extract only when a pattern repeats 3x. No `utils`, `helpers`, or `common` packages.

---

## Codegen-Friendly by Default

The implicit user of every Oasis API is an LLM coding assistant with zero prior Oasis context. If an LLM consistently misuses an API, that's a DX bug — not a documentation problem.

- **Consistent shapes.** Every Tool has the same `Execute` signature. Every Provider, the same `Complete` signature. When patterns are uniform, an LLM generates correct code on the first try.
- **Predictable names.** The same verb means the same thing across the surface. `Get` returns or errors. `Find` returns or nil. `List` returns a (possibly empty) slice.
- **Fail at compile time, not runtime.** Required fields are function arguments, not struct fields with runtime "missing field" errors. Optional fields use functional options.
- **No `any` at the boundary.** Codegen tooling can't reason about empty interfaces. If you need polymorphism in an exported API, expose a typed interface or a tagged union.
- **Godoc that reads as a spec.** First sentence states the contract. Document what nil/zero behavior means, what is thread-safe, what panics, what returns errors versus business failures.

---

## Fail Gracefully, Recover Fast

An autonomous agent can't page a human every time something goes wrong.

- Never crash on recoverable errors. Memory extraction fails? Chat continues.
- **Recovery must be fast.** A graceful failure that takes 30 seconds to recover is a performance bug. Design recovery paths with the same rigor as happy paths.
- Error messages must be actionable. `"invalid args: expected string for 'query' field"`, not `"invalid args"`.
- **Errors must be observable.** Developers who weren't there when it happened must be able to reconstruct what went wrong from logs alone. Structured logging, clear context, no swallowed errors.
- `ToolResult` is not an error. `Tool.Execute` always returns nil Go error. Business failures go in `ToolResult.Error`. Go errors = infrastructure failure only.

---

## Code for Dual Readers — Human and Agent

Code is read by humans and will be read, extended, and generated by AI agents.

- Names explain *intent*, not *implementation*. `BuildContext` over `GetTop15FactsAndFormat`.
- Comments explain **why**, not **what**. If a comment restates the code, delete it.
- **Capture design context in code.** Every non-obvious design decision must explain why it exists — not just what it does. Without this, future contributors (human or agent) will "fix" correct code or repeat mistakes because the reasoning wasn't visible.
- Consistent patterns. When every Tool follows the same Execute pattern, agents generate correct code on the first try.

---

## Optimize for the First 15 Minutes

A framework that's powerful but painful to use is a framework nobody uses. Developer experience is a design constraint, not a feature.

- **Simple things must be simple.** An agent with one tool and one provider should take fewer than 20 lines of code.
- **Progressive disclosure.** Beginners see the simple path. Power users discover depth without it getting in the way. The 10-line agent and the production-grade agent use the same primitives.
- **Defaults must be sensible.** Zero-config should produce a working, reasonable agent. Every option should have a justifiable default.
- **Own your dependencies.** Can stdlib or <200 lines hand-rolled solve it? Don't add the dep. For external APIs: no SDKs — raw HTTP + JSON gives full control and fewer surprises.

---

## What Oasis Does Not Do

Staying in lane is how a framework stays sharp.

- **Not a bot framework.** No platform integrations, no message routers, no command parsers — those belong above Oasis.
- **Not an LLM abstraction layer.** No SDK dependencies. Providers speak raw HTTP. Adding a Provider means writing one file, not learning a vendor SDK.
- **Not a workflow engine.** Oasis has a `workflow` package, but it exists to orchestrate agents — not to be a general-purpose DAG runner.
- **Not a vector database.** Storage and retrieval are integration points; the framework doesn't ship its own indexes or embeddings.
- **Not an opinionated app architecture.** No prescribed agent topology, no required memory strategy, no mandatory supervisor pattern. You design the app; the framework supplies the bricks.

If a feature request would push Oasis into any of these spaces, the answer is: build it in user code or in a satellite. The core stays focused.
