# Phase 1 — Type-Safety Release

**Date:** 2026-05-18
**Status:** Design — approved for implementation planning
**Source findings:** [2026-05-18-core-agent-review.md](../plans/2026-05-18-core-agent-review.md), [2026-05-18-dx-improvements-audit.md](../plans/2026-05-18-dx-improvements-audit.md)

## Theme

Every common authoring path becomes type-safe. Magic strings, raw byte-source ambiguity, and silent failures are removed at the API boundary. This is the first of several focused breaking releases — its narrative is *type safety*, not architecture or performance.

## Goals

1. Replace string-key metadata with typed struct fields on `AgentTask`.
2. Replace three-byte-source `Attachment` ambiguity with explicit constructors and one storage field.
3. Replace `string`-typed `Role` with a typed constant.
4. Add the missing `StreamingTool[In, Out]` generic interface.
5. Rename `Drain()` to the stdlib-conventional `Close() error`.
6. Fix three documentation contracts that contradict actual behavior.
7. Remove two pieces of dead/deprecated code.

## Non-goals (deferred to later phases)

- **Typed tool schemas (3.1).** Biggest single DX win in the source review, but design-heavy (reflection rules, supported types, escape hatches). Gets its own Phase 1.5 with a dedicated spec.
- **`core/` "don't import" gate (3.2).** Philosophical question about umbrella vs. importable foundation. Not type-safety; decide separately.
- **Store / Provider capability splits.** Phase 3 structural breaking work.
- **`loop.go` / `agentcore.go` file splits.** Phase 4 mechanical refactor.
- **AgentCore + network package unification.** Phase 5 architectural decision.
- **Performance items (4.1.a-g).** Bundle separately when motivated by a real benchmark.

## Out of scope, already shipped

- **Audit #2 (typed processors)** shipped in commit `ba9cbd7` on 2026-05-18. `WithProcessors(...any)` was replaced with three typed options (`WithPreProcessors`, `WithPostProcessors`, `WithPostToolProcessors`).

---

## Design decisions

Each decision below documents the chosen approach, the rationale, and the alternatives considered. Implementation order is suggested but not required — most changes touch disjoint files.

### 1. `AgentTask` metadata restructure

**Findings addressed:** 1.1.a (map mutation bug), 2.2.c (magic-string keys)

**Today:**

```go
type AgentTask struct {
    Input   string
    Context map[string]any  // mixed: reserved keys + app keys
}

const (
    ContextThreadID = "thread_id"
    ContextUserID   = "user_id"
    ContextChatID   = "chat_id"
)

func (t AgentTask) WithThreadID(id string) AgentTask {
    if t.Context == nil { t.Context = map[string]any{} }
    t.Context[ContextThreadID] = id  // BUG: mutates caller's map
    return t
}
// + WithUserID, WithChatID (same bug)
// + TaskThreadID, TaskUserID, TaskChatID accessors
```

**Designed:**

```go
type AgentTask struct {
    Input    string
    ThreadID string
    UserID   string
    ChatID   string
    Extra    map[string]any  // app-defined keys only (e.g., "tier", "region")
}

func (t AgentTask) WithThreadID(id string) AgentTask { t.ThreadID = id; return t }
func (t AgentTask) WithUserID(id string)   AgentTask { t.UserID = id;   return t }
func (t AgentTask) WithChatID(id string)   AgentTask { t.ChatID = id;   return t }
```

**Removed:**
- `Context map[string]any` field (replaced by typed fields + `Extra`)
- `ContextThreadID` / `ContextUserID` / `ContextChatID` constants
- `TaskThreadID()` / `TaskUserID()` / `TaskChatID()` accessor functions

**Why flat, not embedded `TaskMeta`:** the codebase already prefers flat structs (`ChatMessage` is flat with `Role`, `Content`, `ToolCalls` as peer fields). Embedding would introduce a pattern not used elsewhere just to keep a sub-type around. If a "pass IDs as a bundle" use case appears, add a `MetaFields()` method later — YAGNI today.

**Why preserve `Extra`:** `task.Context["tier"]` is actively used in dynamic-resolver tests (`network/network_test.go:125`, `agent/agent_test.go:684`). Dropping the extension surface would force every user doing app-level routing to invent their own replacement via `context.Context` values — net DX worse.

**Alternatives rejected:**
- Deep-copy `Context` map on every `With*` call — fixes 1.1.a but not 2.2.c; wastes the breaking-change budget.
- `TaskMeta` named sub-type — adds a type not used elsewhere; flat is more idiomatic.
- Drop the extension map entirely — breaks `task.Context["tier"]` callers.

### 2. `Attachment` overhaul

**Findings addressed:** 1.1.c (silent base64 error), 1.2.b (three byte sources, `Base64` half-deprecated), 3.10 (no constructor)

**Today:**

```go
type Attachment struct {
    MimeType string
    URL      string
    Data     []byte
    Base64   string  // Deprecated, but still read by InlineData()
}

func (a Attachment) InlineData() []byte {
    if len(a.Data) > 0 { return a.Data }
    if a.Base64 != "" {
        data, _ := base64.StdEncoding.DecodeString(a.Base64)  // silent swallow
        return data
    }
    return nil
}
```

**Designed:**

```go
type Attachment struct {
    MimeType string
    URL      string
    Data     []byte  // encoding/json transparently base64-encodes []byte on the wire
}

func NewAttachment(mime string, data []byte) Attachment
func NewAttachmentFromURL(mime, url string) Attachment
func NewAttachmentFromBase64(mime, encoded string) (Attachment, error)

func (a Attachment) InlineData() []byte  { return a.Data }    // infallible
func (a Attachment) HasInlineData() bool { return len(a.Data) > 0 }
```

**Removed:**
- `Base64 string` field

**Why drop `Base64`:** all production code already populates `Data` (see `ingest/image_embed.go`, `provider/gemini/gemini.go`). The field appears only in 3-4 test sites. Go's `encoding/json` already marshals `[]byte` as base64 on the wire, so no serialization functionality is lost. Forcing base64 input through a constructor surfaces decode errors at the right point (construction, not read).

**Why infallible `InlineData()`:** all decoding happens at construction time. After construction, the attachment either has valid `Data` or doesn't — no error path needed at read time. Every provider's `att.InlineData()` call site stays unchanged.

**Migration cost:** 3-4 test sites switch from `Attachment{Base64: "..."}` to `NewAttachmentFromBase64("mime", "...")`. Mechanical.

**Alternatives rejected:**
- Keep `Base64`, change `InlineData() []byte` → `InlineData() ([]byte, error)` — wider blast radius (every provider call site updates) for less benefit. Doesn't resolve 1.2.b.
- Add a sibling `InlineDataE() ([]byte, error)` method — adds surface in a direction the stdlib avoids (`XxxE` doubles).

### 3. Typed `Role`

**Finding addressed:** 1.2.c

**Today:**

```go
type ChatMessage struct {
    Role    string  // "system", "user", "assistant", "tool"
    Content string
    // ...
}
```

**Designed:**

```go
type Role string

const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

type ChatMessage struct {
    Role    Role
    Content string
    // ...
}
```

**Why `type Role string`, not `type Role int`:**
- JSON wire format preserved (no custom marshaler needed; tag-compat with every existing provider response).
- `msg.Role == "user"` comparisons keep compiling — Go allows comparing `Role` to untyped string literals.
- `int` form would require breaking JSON round-trips.

**Migration cost:** mostly source-compatible. New code uses constants for autocomplete and typo-safety. Old `Role: "user"` literals still compile.

**Pattern reference:** `CheckpointStatus` in `core/checkpoint.go` already uses this `type X string + typed constants` idiom in the codebase.

### 4. `StreamingTool[In, Out]` generic interface

**Finding addressed:** 1.2.i

**Today:** `Tool[In, Out]` exists for type-safe non-streaming tools. `StreamingAnyTool` exists for the erased streaming variant. There is no `StreamingTool[In, Out]` for type-safe streaming authoring — authors must drop down to `AnyTool` and lose type safety.

**Designed:**

```go
type StreamingTool[In, Out any] interface {
    Tool[In, Out]
    ExecuteStream(ctx context.Context, in In, ch chan<- StreamEvent) (Out, error)
}

func EraseStreaming[In, Out any](t StreamingTool[In, Out]) StreamingAnyTool
```

**Why this shape:** mirrors the existing `Tool[In, Out]` / `AnyTool` / `StreamingAnyTool` triangle. `EraseStreaming` is a separate function rather than overloading `Erase` because Go generics on interfaces can't easily branch on whether `T` implements streaming.

**Migration cost:** purely additive. Existing `Tool[In, Out]` authors are unaffected.

### 5. `Drain()` → `Close() error`

**Finding addressed:** 3.5

**Today:**

```go
func (c *AgentCore) Drain() { c.mem.Drain() }
```

**Designed:**

```go
func (c *AgentCore) Close() error { return c.mem.Close() }
```

`memory.Drain()` similarly renames to `memory.Close() error`. Returns `nil` today; signature is forward-compatible with future flush errors (remote stores, network drains).

**Why `error` return even though it's nil today:** adding an error return is itself a breaking change. Locking in the `io.Closer`-shaped signature now avoids a second breakage later.

**Migration cost:** find-replace `Drain()` → `Close()` per caller. Strict callers add an error check.

**Alternatives rejected:**
- Additive `Close()` alongside deprecated `Drain()` — two methods for one operation; requires a second breakage later to remove `Drain()`.
- Add `runtime.SetFinalizer` safety net — finalizers have GC-timing footguns and can spam warnings on process crash; not the right tool for lifecycle management.

### 6. `Provider.ChatStream` doc fix

**Finding addressed:** 1.1.b (doc contradicts actual contract)

**Today** (`core/types.go:33-36`):

```go
// ChatStream sends a chat request and streams the response.
// The channel is NOT closed by the provider — the caller owns its lifecycle.
```

But `agent/loop.go:262-273` does `for ev := range iterCh` after `ChatStream` returns. That loop only exits when the channel closes — meaning if a provider followed the documented contract literally, the agent loop would deadlock. Every existing provider implementation already closes the channel, matching reality.

**Designed:**

```go
// ChatStream sends a chat request and streams the response.
// Implementations MUST close ch before returning.
```

Zero behavior change. Aligns with the existing `StreamingAgent.ExecuteStream` contract in `core/agent.go:32-34`.

### 7. `ErrHalt` doc fix

**Finding addressed:** 1.2.j

**Today** (`core/processor.go:36-40`):

```go
// ErrHalt signals that a processor wants to stop agent execution.
// Return ErrHalt to short-circuit ...
func (e *ErrHalt) Error() string { ... }  // pointer receiver
```

The doc says "Return ErrHalt" but only `*ErrHalt` satisfies the `error` interface (pointer receiver on `Error()`). Returning a value `ErrHalt{...}` would not match in `errors.As`.

**Designed:**

```go
// ErrHalt signals that a processor wants to stop agent execution.
// To halt, return &ErrHalt{Response: "..."} (pointer, not value).
// errors.As(err, new(*ErrHalt)) will match.
```

Doc-only change. No code modification.

### 8. Remove `subAgentConfig` backcompat alias

**Finding addressed:** 2.2.f

**Today** (`agent/llm.go:367-368`):

```go
// subAgentConfig is an alias for SubAgentConfig for backward compatibility.
type subAgentConfig = SubAgentConfig
```

The alias predates the hybrid-architecture refactor. No external callers depend on it. The doc comment self-documents its obsolescence.

**Designed:** delete the two lines. Replace any internal `subAgentConfig` references with `SubAgentConfig` (likely none — the alias appears unused).

### 9. Remove dead `recover()` in `onceClose`

**Finding addressed:** 2.2.g

**Today** (`agent/agentcore.go:412-420`):

```go
func onceClose[T any](ch chan<- T) func() {
    var once sync.Once
    return func() {
        once.Do(func() {
            defer func() { recover() }()  // unreachable
            close(ch)
        })
    }
}
```

`sync.Once` guarantees `close(ch)` runs exactly once. Double-close-panic from within this helper is impossible. The `recover()` is unreachable defensive code.

**Designed:** delete the `defer func() { recover() }()` line. Keep `sync.Once` and `close(ch)`.

---

## Migration guide (user-facing)

This is the user migration story that will accompany the release notes. Written in the imperative voice users expect.

### `AgentTask`

```diff
- task := AgentTask{Input: "hi", Context: map[string]any{
-     agent.ContextThreadID: "t1",
-     "tier": "pro",
- }}
- threadID := agent.TaskThreadID(task)
+ task := AgentTask{
+     Input:    "hi",
+     ThreadID: "t1",
+     Extra:    map[string]any{"tier": "pro"},
+ }
+ threadID := task.ThreadID
```

`WithThreadID(id)` / `WithUserID(id)` / `WithChatID(id)` are unchanged.

### `Attachment`

```diff
- att := Attachment{MimeType: "image/png", Base64: encodedString}
+ att, err := NewAttachmentFromBase64("image/png", encodedString)
+ if err != nil { return err }
```

Or for raw bytes:

```diff
- att := Attachment{MimeType: "image/png", Data: bytes}
+ att := NewAttachment("image/png", bytes)
```

`InlineData()` signature is unchanged. `HasInlineData()` is unchanged.

### `Role`

```diff
- msg := ChatMessage{Role: "user", Content: "..."}
+ msg := ChatMessage{Role: RoleUser, Content: "..."}
```

String literals still compile — migration is best-effort, not forced.

### `Close()`

```diff
- defer core.Drain()
+ defer core.Close()
```

If you want to surface flush errors:

```go
defer func() {
    if err := core.Close(); err != nil {
        log.Error("agent close", "err", err)
    }
}()
```

### `StreamingTool[In, Out]`

Purely additive. Existing tools unaffected. New streaming tools:

```go
type MyStreamingTool struct{}

func (t *MyStreamingTool) Name() string { return "search" }
func (t *MyStreamingTool) Definition() ToolDefinition { ... }
func (t *MyStreamingTool) Execute(ctx context.Context, in SearchIn) (SearchOut, error) { ... }
func (t *MyStreamingTool) ExecuteStream(ctx context.Context, in SearchIn, ch chan<- StreamEvent) (SearchOut, error) { ... }

// Register via oasis.WithTools — StreamingAnyTool embeds AnyTool, so the
// existing WithTools(...AnyTool) signature accepts the erased streaming tool.
// The loop's runtime type-assertion to StreamingAnyTool picks up the streaming
// path automatically (see core/types.go ToolRegistry.Execute).
agent := oasis.NewLLMAgent(
    oasis.WithTools(
        oasis.EraseStreaming[SearchIn, SearchOut](&MyStreamingTool{}),
    ),
)
```

---

## Release sequencing

**Single breaking minor bump.** All changes ship together. One migration guide.

Suggested parallel implementation tracks (work runs concurrently on disjoint files):

| Track | Items | Primary files |
|---|---|---|
| A | `AgentTask` restructure | `core/agent.go`, all `task.Context[...]` and `TaskThreadID(...)` call sites |
| B | `Attachment` overhaul | `core/types.go`, `core/umbrella_types_test.go`, `network/network_test.go`, `agent/memory_integration_test.go` |
| C | Typed `Role` + `ChatStream` doc + `ErrHalt` doc | `core/types.go`, `core/processor.go` |
| D | `StreamingTool[In, Out]` + `Drain`→`Close` + alias + recover | `core/tool.go`, `agent/agentcore.go`, `agent/llm.go`, satellite memory backends |

Tracks B and C both touch `core/types.go` (different regions — B touches the `Attachment` block around line 235, C touches `ChatMessage.Role` around line 222 and `Provider.ChatStream` doc around line 33). Safe to parallelize if each agent is scoped to its own region; otherwise sequence B before C. Track A (`core/agent.go`) and Track D (`core/tool.go`, `agent/*`) have no file overlap with B or C.

**Estimated total effort:** 3-4 days for one developer; ~1-2 days with parallel tracks.

---

## Testing approach

- **Existing tests are the safety net.** Every changed surface (`AgentTask`, `Attachment`, `Role`, `Drain`/`Close`) has existing integration coverage in `agent/`, `network/`, `provider/gemini/`, `provider/openaicompat/`, and `store/{sqlite,postgres}/` satellites. All must stay green.
- **Add focused tests** for each new constructor (`NewAttachment`, `NewAttachmentFromURL`, `NewAttachmentFromBase64`) including the error path on `FromBase64`.
- **Migration tests:** for each changed call site in the test suite, port to the new API and assert behavioral equivalence — these double as living examples for the migration guide.
- **No new integration tests required.** The release is structural, not behavioral.
- **CI gate:** `go test ./...` from root and from every satellite `go.mod` must pass before merge. `golangci-lint run ./...` must pass.

---

## Open questions

None blocking implementation. Two notes for the implementation plan:

1. **`memory.Drain()` rename surface.** Need to enumerate satellite backends that implement the memory drainer (sqlite, postgres) and rename in lockstep. The implementation plan should list each implementor.
2. **Test data using `Base64`.** A grep for `Base64:` in test files turned up 4 sites. The implementation plan should confirm exact migration for each before merging.

---

## Approval

This design was approved interactively on 2026-05-18 following a round of brainstorming that surfaced and decided each design choice with explicit alternatives considered. The next step is to invoke the `superpowers:writing-plans` skill to produce an implementation plan from this design.
