# Phase 2 Coherence Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship Phase 2 of the core/agent review as one bundled breaking minor: tool-result paging, history cascade unification, AgentHandle.State()/Sync() split, plus 6 mechanical fixes (embedding conflict, typed Sandbox, double-close, EventMaxIterReached, three new With* knobs, maxIter default raise 10→25).

**Architecture:** 9 findings grouped into 6 tracks across 4 waves. Wave coordination exists primarily to serialize edits to `agent/loop.go` (Tracks A, D, F all touch it) and `agent/agentcore.go` (Tracks E, F). All other tracks can run in parallel.

**Tech Stack:** Go 1.24, stdlib only in `core/` (no new deps). Existing `Compactor` interface gets a new `Scope` field. New `core.ToolResultStore` and `core.Sandbox` interfaces. Built-in `read_full_result` tool registered automatically via Phase 1.5's typed `Tool[In, Out]` schema.

**Source spec:** [docs/superpowers/specs/2026-05-20-phase-2-coherence-design.md](../specs/2026-05-20-phase-2-coherence-design.md)

---

## File Map

### Wave 1 (parallel — no file overlap)

| Track | Files touched | Owner of `loop.go` edits |
|---|---|---|
| **A — tool-result paging** | Create: `core/tool_result_store.go`, `agent/read_full_result.go`. Modify: `agent/loop.go` (truncation site, lines 469-473), `agent/agent.go` (knobs), `oasis.go` (re-exports) | yes |
| **B — State()/Sync() split** | Modify: `agent/handle.go` only | no |
| **C — mechanical small fixes** | Create: `core/sandbox.go`. Modify: `agent/agent.go` (Sandbox field type, BuildConfig conflict check, WithMaxParallelDispatch, WithMaxPlanSteps), `agent/llm.go` (read maxPlanSteps from config), `oasis.go` (re-exports) | no |

### Wave 2 (parallel — D touches loop.go after A)

| Track | Files touched | Owner of `loop.go` edits |
|---|---|---|
| **D — history cascade** | Modify: `core/compactor.go` (add Scope field + constants), `agent/loop.go` (refactor `compressMessages`), `docs/concepts/memory.md` (cascade docs) | yes |
| **E — double-close fix** | Modify: `agent/agentcore.go` (investigate bypass path in `forwardSubagentStream`, remove recover from `onceClose`) | no |

### Wave 3 (sequential single — F runs after both A and D)

| Track | Files touched | Owner of `loop.go` edits |
|---|---|---|
| **F — EventMaxIterReached + maxIter raise** | Modify: `core/stream.go` (new event const), `agent/loop.go` (emit at line ~491), `agent/agentcore.go` (defaultMaxIter 10→25) | yes |

### Wave 4 (verification)

- Modify: `CHANGELOG.md`, `docs/concepts/memory.md` (if not done in D), `docs/api/options.md`
- Verify: full repo + 9 satellites

---

## Task Decomposition

### Wave 1

#### Task 1: Track C1 — Reject conflicting embedding providers at BuildConfig time

**Files:**
- Modify: `agent/agent.go:374` (BuildConfig function)
- Test: `agent/agent_test.go` (new test in existing file)

- [ ] **Step 1: Write the failing test**

Add to `agent/agent_test.go`:

```go
func TestBuildConfigPanicsOnConflictingEmbedding(t *testing.T) {
    em1 := &fakeEmbeddingProvider{name: "em1"}
    em2 := &fakeEmbeddingProvider{name: "em2"}
    mem := &fakeMemoryStore{}

    defer func() {
        r := recover()
        if r == nil {
            t.Fatal("expected panic for conflicting embedding providers, got nil")
        }
        msg, _ := r.(string)
        if msg == "" {
            if e, ok := r.(error); ok {
                msg = e.Error()
            }
        }
        if !strings.Contains(fmt.Sprint(r), "conflicting embedding providers") {
            t.Errorf("expected 'conflicting embedding providers' in panic, got: %v", r)
        }
    }()

    _ = BuildConfig([]AgentOption{
        WithUserMemory(mem, em1),
        WithHistory(history.CrossThreadSearch(em2)),
    })
}

func TestBuildConfigAllowsMatchingEmbedding(t *testing.T) {
    em := &fakeEmbeddingProvider{name: "em"}
    mem := &fakeMemoryStore{}

    // Must not panic.
    _ = BuildConfig([]AgentOption{
        WithUserMemory(mem, em),
        WithHistory(history.CrossThreadSearch(em)),
    })
}

type fakeEmbeddingProvider struct{ name string }
func (f *fakeEmbeddingProvider) Embed(ctx context.Context, text string) ([]float32, error) {
    return []float32{0}, nil
}
type fakeMemoryStore struct{}
// Stub the MemoryStore interface methods as needed for compilation.
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run "TestBuildConfig(Panics|Allows)" -v
```

Expected: FAIL — TestBuildConfigPanicsOnConflictingEmbedding sees nil panic (BuildConfig is silent today).

- [ ] **Step 3: Add the conflict check to BuildConfig**

In `agent/agent.go`, in `BuildConfig` (after the options loop), add the validation:

```go
// Conflict: two memory features configured with different embedding providers.
// Use panic instead of error-returning because NewLLMAgent's signature would
// otherwise need a breaking change beyond the scope of Phase 2. Misconfigured
// embedding providers are a developer-time error, not a runtime condition.
if c.embedding != nil && c.trimmingEmbedding != nil && c.embedding != c.trimmingEmbedding {
    panic(fmt.Sprintf(
        "oasis: conflicting embedding providers — WithUserMemory uses %T, "+
            "history.CrossThreadSearch uses %T; use the same provider for both, or pick one",
        c.embedding, c.trimmingEmbedding))
}
```

(Add `"fmt"` to the imports if not already present.)

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./agent/ -run "TestBuildConfig(Panics|Allows)" -v
```

Expected: PASS.

- [ ] **Step 5: Run the full agent test suite to catch regressions**

```bash
go test ./agent/ -race
```

Expected: PASS — no existing tests broken by the added panic.

- [ ] **Step 6: Commit**

```bash
git add agent/agent.go agent/agent_test.go
git commit -m "feat(agent)!: panic on conflicting embedding providers at build time

WithUserMemory and history.CrossThreadSearch now panic from BuildConfig
if they reference different EmbeddingProvider instances. Previously
the last-writer-wins silently picked one. Panic (not error return)
preserves NewLLMAgent's existing signature; the condition is a
developer-time misconfiguration, not a runtime one. Closes finding 1.2.g."
```

---

#### Task 2: Track C2 — Define `core.Sandbox` interface and type `agentConfig.Sandbox`

**Files:**
- Create: `core/sandbox.go`
- Modify: `agent/agent.go:42` (field type) and `agent/agent.go:216` (`WithSandbox` signature)
- Test: `core/sandbox_test.go`

- [ ] **Step 1: Write the failing compile-time check test**

Create `core/sandbox_test.go`:

```go
package core_test

import (
    "context"

    "github.com/nevindra/oasis/core"
)

// Compile-time check: a minimal Sandbox implementation satisfies the interface.
type stubSandbox struct{}

func (stubSandbox) Exec(ctx context.Context, lang, code string) (string, string, error) {
    return "", "", nil
}
func (stubSandbox) Close() error { return nil }

var _ core.Sandbox = stubSandbox{}
```

- [ ] **Step 2: Run test to verify compilation fails**

```bash
go build ./core/...
```

Expected: COMPILE ERROR — `undefined: core.Sandbox`.

- [ ] **Step 3: Define the interface**

Create `core/sandbox.go`:

```go
package core

import "context"

// Sandbox executes code in an isolated environment. Implementations live in
// satellite packages (e.g. github.com/nevindra/oasis/sandbox) so the framework
// core stays free of heavy dependencies like Docker SDKs.
//
// Used by WithSandbox to enable the built-in code execution tool. Pass any
// implementation that satisfies this interface.
type Sandbox interface {
    // Exec runs the provided code in the given language and returns stdout,
    // stderr, and any execution error. ctx cancellation should terminate
    // the running code.
    Exec(ctx context.Context, lang, code string) (stdout, stderr string, err error)

    // Close releases any resources held by the sandbox (containers, processes,
    // etc.). Safe to call multiple times.
    Close() error
}
```

- [ ] **Step 4: Update `agent/agent.go` field and option signature**

In `agent/agent.go`, change line 42 from:
```go
sandbox any // sandbox.Sandbox — typed as any to avoid the satellite import
```
to:
```go
sandbox core.Sandbox
```

In `agent/agent.go:216`, change `WithSandbox` from:
```go
func WithSandbox(sb any, tools ...AnyTool) AgentOption {
```
to:
```go
func WithSandbox(sb core.Sandbox, tools ...AnyTool) AgentOption {
```

Also update any internal type assertions on `c.sandbox` — they should now be direct method calls (search for `c.sandbox.(`).

```bash
grep -rn "c.sandbox.(" agent/
grep -rn "cfg.Sandbox.(" agent/
```

Replace each with direct method call (e.g. `c.sandbox.Exec(...)` instead of `c.sandbox.(sandbox.Sandbox).Exec(...)`).

- [ ] **Step 5: Run test to verify it passes**

```bash
go test ./core/ -count=1 -v
go test ./agent/ -race
```

Expected: PASS.

- [ ] **Step 6: Verify the satellite still compiles**

```bash
cd sandbox && go build ./... && cd ..
```

Expected: clean build. The `sandbox.Sandbox` type already has `Exec` and `Close` methods, so it satisfies the new interface automatically.

- [ ] **Step 7: Commit**

```bash
git add core/sandbox.go core/sandbox_test.go agent/agent.go
git commit -m "feat(core)!: add core.Sandbox interface; WithSandbox now requires it

Replaces 'sandbox any' field with typed core.Sandbox. The sandbox/
satellite's existing Sandbox type already implements the interface;
no satellite changes needed. Custom sandbox implementations must
satisfy Exec(ctx, lang, code) and Close(). Closes finding 1.2.k."
```

---

#### Task 3: Track C3 — Add `WithMaxParallelDispatch`, `WithMaxPlanSteps`, `WithMaxToolResultLen` knobs

**Files:**
- Modify: `agent/agent.go` (three new option functions + three new fields on `agentConfig`)
- Modify: `agent/loop.go:133` (read knob instead of const)
- Modify: `agent/llm.go:160-173` (read knob instead of const)
- Modify: `oasis.go` (re-export the three options)
- Test: `agent/options_test.go` (new file or existing options test file)

- [ ] **Step 1: Write the failing tests**

Add to `agent/options_test.go`:

```go
func TestWithMaxParallelDispatchSetsConfig(t *testing.T) {
    c := BuildConfig([]AgentOption{WithMaxParallelDispatch(3)})
    if c.maxParallelDispatch != 3 {
        t.Errorf("expected 3, got %d", c.maxParallelDispatch)
    }
}

func TestWithMaxPlanStepsSetsConfig(t *testing.T) {
    c := BuildConfig([]AgentOption{WithMaxPlanSteps(7)})
    if c.maxPlanSteps != 7 {
        t.Errorf("expected 7, got %d", c.maxPlanSteps)
    }
}

func TestWithMaxToolResultLenSetsConfig(t *testing.T) {
    c := BuildConfig([]AgentOption{WithMaxToolResultLen(50_000)})
    if c.maxToolResultLen != 50_000 {
        t.Errorf("expected 50000, got %d", c.maxToolResultLen)
    }
}

func TestDefaultMaxParallelDispatch(t *testing.T) {
    c := BuildConfig(nil)
    if c.maxParallelDispatch != 10 {
        t.Errorf("expected default 10, got %d", c.maxParallelDispatch)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./agent/ -run "TestWith(MaxParallelDispatch|MaxPlanSteps|MaxToolResultLen)|TestDefaultMaxParallelDispatch" -v
```

Expected: FAIL — fields undefined.

- [ ] **Step 3: Add fields to `agentConfig` + option functions**

In `agent/agent.go`, in the `agentConfig` struct, add:

```go
maxParallelDispatch int
maxPlanSteps        int
maxToolResultLen    int
```

In `BuildConfig`, after the loop applies options, add defaults:

```go
if c.maxParallelDispatch == 0 {
    c.maxParallelDispatch = 10
}
if c.maxPlanSteps == 0 {
    c.maxPlanSteps = 50
}
if c.maxToolResultLen == 0 {
    c.maxToolResultLen = 100_000
}
```

Add option functions in `agent/agent.go`:

```go
// WithMaxParallelDispatch caps the number of concurrent tool call goroutines.
// Default is 10. Set higher when tools are I/O-bound and can tolerate fan-out.
func WithMaxParallelDispatch(n int) AgentOption {
    return func(c *agentConfig) {
        if n > 0 {
            c.maxParallelDispatch = n
        }
    }
}

// WithMaxPlanSteps caps the number of steps in a single execute_plan call.
// Default is 50. The LLM gets an error if it submits a plan with more steps.
func WithMaxPlanSteps(n int) AgentOption {
    return func(c *agentConfig) {
        if n > 0 {
            c.maxPlanSteps = n
        }
    }
}

// WithMaxToolResultLen sets the inline budget for tool results in the
// conversation history (in runes). Results larger than this are stored in
// the ToolResultStore (if configured) and replaced with a paging marker.
// Default is 100_000 runes (~25K tokens).
func WithMaxToolResultLen(n int) AgentOption {
    return func(c *agentConfig) {
        if n > 0 {
            c.maxToolResultLen = n
        }
    }
}
```

- [ ] **Step 4: Plumb knobs into runtime**

In `agent/loop.go`, find `maxParallelDispatch = 10` constant (line 133). Either remove the const and add `maxParallelDispatch int` to `LoopConfig`, or keep the const as the type's zero-value default. Recommended: add to `LoopConfig`, populated from `agentConfig.maxParallelDispatch`. Replace usages in `dispatchParallel` to read from `LoopConfig`.

In `agent/llm.go:160`, remove `const maxPlanSteps = 50`. Plumb `maxPlanSteps` through the execute_plan tool's config (the tool likely lives in `agent/llm.go` — pass `cfg.maxPlanSteps` to it at registration time, or read from `agentConfig` directly).

In `agent/loop.go:120`, remove `const maxToolResultMessageLen = 100_000`. Add `maxToolResultLen int` to `LoopConfig`. Replace usages at `loop.go:471` and any other call sites.

- [ ] **Step 5: Run tests to verify they pass + no regressions**

```bash
go test ./agent/ -race
```

Expected: PASS — all old tests pass, all new tests pass.

- [ ] **Step 6: Re-export from `oasis.go`**

In `oasis.go`, add:

```go
var (
    WithMaxParallelDispatch = agent.WithMaxParallelDispatch
    WithMaxPlanSteps        = agent.WithMaxPlanSteps
    WithMaxToolResultLen    = agent.WithMaxToolResultLen
)
```

(Match the style of existing re-exports in the file.)

- [ ] **Step 7: Commit**

```bash
git add agent/agent.go agent/loop.go agent/llm.go agent/options_test.go oasis.go
git commit -m "feat(agent): expose WithMaxParallelDispatch, WithMaxPlanSteps, WithMaxToolResultLen

Three previously hardcoded constants are now configurable. Defaults
unchanged (10, 50, 100_000). Closes finding 3.8."
```

---

#### Task 4: Track B — Split `AgentHandle.State()` and add `Sync()`

**Files:**
- Modify: `agent/handle.go:146-155`
- Test: `agent/handle_test.go` (existing) — add new tests

- [ ] **Step 1: Write the failing test**

Add to `agent/handle_test.go`:

```go
func TestStateNonBlocking(t *testing.T) {
    // Build a handle whose loop hangs indefinitely. State() should still
    // return without blocking.
    blockCh := make(chan struct{}) // never closed
    h := newTestHandle(t, func(ctx context.Context) (AgentResult, error) {
        h.state.Store(int32(StateCompleted)) // simulate terminal but not done
        <-blockCh                            // hang
        return AgentResult{}, nil
    })

    done := make(chan struct{})
    go func() {
        _ = h.State()
        close(done)
    }()

    select {
    case <-done:
        // Pass — State() returned promptly.
    case <-time.After(100 * time.Millisecond):
        t.Fatal("State() blocked when it should not")
    }
}

func TestSyncEstablishesBarrier(t *testing.T) {
    h := newTestHandle(t, func(ctx context.Context) (AgentResult, error) {
        return AgentResult{Output: "done"}, nil
    })
    <-h.Done()
    h.Sync()
    result, _ := h.Result()
    if result.Output != "done" {
        t.Errorf("expected 'done' after Sync, got %q", result.Output)
    }
}

// newTestHandle helper exists in handle_test.go or needs to be added.
```

If `newTestHandle` doesn't exist, write a minimal version that spawns an agent with the given execute function via `Spawn`.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run "TestStateNonBlocking|TestSyncEstablishesBarrier" -v
```

Expected: TestStateNonBlocking FAILS (timeout); TestSyncEstablishesBarrier FAILS (`Sync` undefined).

- [ ] **Step 3: Modify `State()` and add `Sync()`**

In `agent/handle.go`, replace lines 146-155 with:

```go
// State returns the current execution state without blocking. If the state
// is terminal, the returned value reflects the atomic snapshot but writes
// by the agent loop may not yet be visible to this goroutine. Call Sync
// before reading Result to establish a happens-before barrier.
func (h *AgentHandle) State() AgentState {
    return AgentState(h.state.Load())
}

// Sync blocks until the agent loop's writes are visible. Returns immediately
// if the agent has not yet reached a terminal state (nothing to synchronize)
// or if Done has already been observed by this goroutine.
//
// Use Sync between a terminal State() check and reading Result:
//
//	if h.State().IsTerminal() {
//	    h.Sync()
//	    result, err := h.Result()
//	    ...
//	}
func (h *AgentHandle) Sync() {
    select {
    case <-h.done:
    default:
        // Not yet terminal — nothing to wait for. State() returned a
        // non-terminal value or a terminal value that's still racing.
        // We block until done if and only if state is now terminal.
        if AgentState(h.state.Load()).IsTerminal() {
            <-h.done
        }
    }
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run "TestStateNonBlocking|TestSyncEstablishesBarrier" -v -race
```

Expected: PASS.

- [ ] **Step 5: Sweep for callers that need migration**

```bash
grep -rn "\.State()\.IsTerminal()" agent/ oasis_test.go workflow/ network/ skills/
grep -rn "\.State()$" agent/ oasis_test.go workflow/ network/ skills/
```

For each hit, audit whether the caller reads `Result()` afterward. If yes, insert `h.Sync()` between `State()` check and `Result()` call.

- [ ] **Step 6: Run full agent test suite with race detector**

```bash
go test ./agent/ -race
go test ./... -race
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/handle.go agent/handle_test.go
git commit -m "feat(agent)!: split AgentHandle.State() into non-blocking State + Sync

State() now returns immediately even when the state is terminal.
Callers reading Result() after a terminal State() must call Sync()
between the two to establish a happens-before barrier. Closes
finding 3.4.

Migration: search for State().IsTerminal() followed by Result(),
insert Sync() between them."
```

---

#### Task 5: Track A1 — Define `core.ToolResultStore` interface

**Files:**
- Create: `core/tool_result_store.go`
- Test: `core/tool_result_store_test.go`

- [ ] **Step 1: Write the failing test for interface satisfaction**

Create `core/tool_result_store_test.go`:

```go
package core_test

import (
    "context"
    "testing"

    "github.com/nevindra/oasis/core"
)

type stubResultStore struct {
    data map[string]string
}

func (s *stubResultStore) Put(ctx context.Context, content string) (string, error) {
    id := "id1"
    s.data[id] = content
    return id, nil
}

func (s *stubResultStore) Get(ctx context.Context, id string, offset, length int) (string, int, error) {
    c, ok := s.data[id]
    if !ok {
        return "", 0, core.ErrToolResultNotFound
    }
    runes := []rune(c)
    if offset >= len(runes) {
        return "", len(runes), nil
    }
    end := offset + length
    if end > len(runes) {
        end = len(runes)
    }
    return string(runes[offset:end]), len(runes), nil
}

var _ core.ToolResultStore = (*stubResultStore)(nil)

func TestToolResultStoreInterface(t *testing.T) {
    s := &stubResultStore{data: map[string]string{}}
    id, err := s.Put(context.Background(), "hello world")
    if err != nil {
        t.Fatal(err)
    }
    slice, total, err := s.Get(context.Background(), id, 0, 5)
    if err != nil {
        t.Fatal(err)
    }
    if slice != "hello" || total != 11 {
        t.Errorf("got slice=%q total=%d, want %q 11", slice, total, "hello")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./core/ -run TestToolResultStoreInterface -v
```

Expected: FAIL — `undefined: core.ToolResultStore`, `undefined: core.ErrToolResultNotFound`.

- [ ] **Step 3: Create the interface and error**

Create `core/tool_result_store.go`:

```go
package core

import (
    "context"
    "errors"
)

// ErrToolResultNotFound is returned by ToolResultStore.Get when the id is
// unknown or has expired.
var ErrToolResultNotFound = errors.New("tool result not found or expired")

// ToolResultStore holds full tool results when their content exceeds the
// inline budget set by WithMaxToolResultLen. The LLM retrieves slices via
// the auto-registered read_full_result built-in tool.
//
// Implementations must be safe for concurrent use. The default in-memory
// implementation (NewInMemoryToolResultStore) is bounded by total bytes
// and per-entry TTL with LRU eviction.
type ToolResultStore interface {
    // Put stores the full content and returns an opaque id. The id is
    // embedded in the truncation marker handed to the LLM.
    Put(ctx context.Context, content string) (id string, err error)

    // Get returns a slice of the stored content starting at offset runes,
    // up to length runes. total is the full rune count of the stored
    // content (so the LLM can tell whether more remains).
    // Returns ErrToolResultNotFound if the id is unknown or expired.
    // If offset >= total, returns empty content with no error.
    Get(ctx context.Context, id string, offset, length int) (content string, total int, err error)
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./core/ -run TestToolResultStoreInterface -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/tool_result_store.go core/tool_result_store_test.go
git commit -m "feat(core): add ToolResultStore interface

New optional capability for paging large tool results past the
inline rune budget. Implementations must be concurrent-safe and
return ErrToolResultNotFound for unknown/expired ids. The default
in-memory store comes in a follow-up commit. Part of finding 3.7."
```

---

#### Task 6: Track A2 — In-memory `ToolResultStore` implementation

**Files:**
- Modify: `core/tool_result_store.go` (add `inMemoryStore` + constructor + options)
- Modify: `core/tool_result_store_test.go` (add behavior tests)

- [ ] **Step 1: Write the failing tests**

Append to `core/tool_result_store_test.go`:

```go
func TestInMemoryStorePutGetRoundTrip(t *testing.T) {
    s := core.NewInMemoryToolResultStore()
    id, err := s.Put(context.Background(), "the quick brown fox")
    if err != nil { t.Fatal(err) }

    slice, total, err := s.Get(context.Background(), id, 4, 5)
    if err != nil { t.Fatal(err) }
    if slice != "quick" || total != 19 {
        t.Errorf("got slice=%q total=%d, want quick/19", slice, total)
    }
}

func TestInMemoryStoreOffsetPastEnd(t *testing.T) {
    s := core.NewInMemoryToolResultStore()
    id, _ := s.Put(context.Background(), "abc")
    slice, total, err := s.Get(context.Background(), id, 10, 5)
    if err != nil { t.Fatal(err) }
    if slice != "" || total != 3 {
        t.Errorf("got slice=%q total=%d, want empty/3", slice, total)
    }
}

func TestInMemoryStoreUnknownID(t *testing.T) {
    s := core.NewInMemoryToolResultStore()
    _, _, err := s.Get(context.Background(), "no-such-id", 0, 10)
    if !errors.Is(err, core.ErrToolResultNotFound) {
        t.Errorf("expected ErrToolResultNotFound, got %v", err)
    }
}

func TestInMemoryStoreTTLEviction(t *testing.T) {
    s := core.NewInMemoryToolResultStore(core.WithToolResultTTL(50 * time.Millisecond))
    id, _ := s.Put(context.Background(), "hello")
    time.Sleep(80 * time.Millisecond)
    _, _, err := s.Get(context.Background(), id, 0, 5)
    if !errors.Is(err, core.ErrToolResultNotFound) {
        t.Errorf("expected expired entry to return ErrToolResultNotFound, got %v", err)
    }
}

func TestInMemoryStoreLRUEviction(t *testing.T) {
    // Small cap forces eviction.
    s := core.NewInMemoryToolResultStore(core.WithToolResultMaxBytes(10))

    id1, _ := s.Put(context.Background(), "0123456789")  // 10 bytes — fills cap
    id2, _ := s.Put(context.Background(), "abcdefghij")  // 10 bytes — evicts id1

    _, _, err := s.Get(context.Background(), id1, 0, 10)
    if !errors.Is(err, core.ErrToolResultNotFound) {
        t.Errorf("expected id1 evicted, got %v", err)
    }
    slice, _, err := s.Get(context.Background(), id2, 0, 10)
    if err != nil || slice != "abcdefghij" {
        t.Errorf("expected id2 retained, got slice=%q err=%v", slice, err)
    }
}

func TestInMemoryStoreConcurrentPut(t *testing.T) {
    s := core.NewInMemoryToolResultStore()
    var wg sync.WaitGroup
    ids := make([]string, 100)
    for i := range ids {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            id, err := s.Put(context.Background(), fmt.Sprintf("payload-%d", i))
            if err != nil { t.Error(err); return }
            ids[i] = id
        }(i)
    }
    wg.Wait()
    seen := map[string]bool{}
    for _, id := range ids {
        if seen[id] {
            t.Errorf("duplicate id: %s", id)
        }
        seen[id] = true
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./core/ -run "TestInMemoryStore" -v
```

Expected: FAIL — all symbols undefined.

- [ ] **Step 3: Implement in-memory store**

Append to `core/tool_result_store.go`:

```go
import (
    "crypto/rand"
    "encoding/base32"
    "sync"
    "time"
)

// InMemoryToolResultStoreOption configures the default in-memory ToolResultStore.
type InMemoryToolResultStoreOption func(*inMemoryStore)

// WithToolResultMaxBytes sets the total byte cap across all stored entries.
// When exceeded, oldest entries (by last access) are evicted. Default is 10 MiB.
func WithToolResultMaxBytes(n int64) InMemoryToolResultStoreOption {
    return func(s *inMemoryStore) { s.maxBytes = n }
}

// WithToolResultTTL sets the per-entry expiration window. Expired entries are
// removed lazily on the next Get or Put. Default is 5 minutes.
func WithToolResultTTL(d time.Duration) InMemoryToolResultStoreOption {
    return func(s *inMemoryStore) { s.ttl = d }
}

// NewInMemoryToolResultStore returns a bounded in-memory ToolResultStore.
// Default cap: 10 MiB total, 5 min TTL per entry, LRU eviction on overflow.
func NewInMemoryToolResultStore(opts ...InMemoryToolResultStoreOption) ToolResultStore {
    s := &inMemoryStore{
        entries:  map[string]*storeEntry{},
        order:    []string{},
        maxBytes: 10 * 1024 * 1024,
        ttl:      5 * time.Minute,
    }
    for _, opt := range opts {
        opt(s)
    }
    return s
}

type storeEntry struct {
    content    string
    bytes      int64
    expiresAt  time.Time
    lastAccess time.Time
}

type inMemoryStore struct {
    mu         sync.Mutex // simpler than RWMutex; Put-Get-evict mix makes upgrades painful
    entries    map[string]*storeEntry
    order      []string // FIFO of ids; we evict from the front
    totalBytes int64
    maxBytes   int64
    ttl        time.Duration
}

func (s *inMemoryStore) Put(ctx context.Context, content string) (string, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    s.expireExpiredLocked()

    id := newResultID()
    entry := &storeEntry{
        content:    content,
        bytes:      int64(len(content)),
        expiresAt:  time.Now().Add(s.ttl),
        lastAccess: time.Now(),
    }
    s.entries[id] = entry
    s.order = append(s.order, id)
    s.totalBytes += entry.bytes

    s.evictUntilUnderCapLocked()
    return id, nil
}

func (s *inMemoryStore) Get(ctx context.Context, id string, offset, length int) (string, int, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    s.expireExpiredLocked()

    entry, ok := s.entries[id]
    if !ok {
        return "", 0, ErrToolResultNotFound
    }
    entry.lastAccess = time.Now()

    runes := []rune(entry.content)
    total := len(runes)
    if offset >= total {
        return "", total, nil
    }
    end := offset + length
    if end > total {
        end = total
    }
    return string(runes[offset:end]), total, nil
}

func (s *inMemoryStore) expireExpiredLocked() {
    now := time.Now()
    kept := s.order[:0]
    for _, id := range s.order {
        e, ok := s.entries[id]
        if !ok {
            continue
        }
        if now.After(e.expiresAt) {
            s.totalBytes -= e.bytes
            delete(s.entries, id)
            continue
        }
        kept = append(kept, id)
    }
    s.order = kept
}

func (s *inMemoryStore) evictUntilUnderCapLocked() {
    for s.totalBytes > s.maxBytes && len(s.order) > 0 {
        id := s.order[0]
        s.order = s.order[1:]
        if e, ok := s.entries[id]; ok {
            s.totalBytes -= e.bytes
            delete(s.entries, id)
        }
    }
}

func newResultID() string {
    var b [8]byte
    _, _ = rand.Read(b[:])
    return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./core/ -run "TestInMemoryStore" -v -race
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/tool_result_store.go core/tool_result_store_test.go
git commit -m "feat(core): in-memory ToolResultStore with TTL and LRU eviction

NewInMemoryToolResultStore returns a bounded store with 10 MiB total
cap and 5-minute per-entry TTL by default. Options WithToolResultMaxBytes
and WithToolResultTTL configure the limits. Eviction is lazy (on Get/Put)
to avoid background goroutines. Part of finding 3.7."
```

---

#### Task 7: Track A3 — Built-in `read_full_result` tool

**Files:**
- Create: `agent/read_full_result.go`
- Test: `agent/read_full_result_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/read_full_result_test.go`:

```go
package agent_test

import (
    "context"
    "encoding/json"
    "strings"
    "testing"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

func TestReadFullResultTool(t *testing.T) {
    store := core.NewInMemoryToolResultStore()
    id, _ := store.Put(context.Background(), "the quick brown fox jumps over the lazy dog")

    tool := agent.NewReadFullResultTool(store)
    if tool.Name() != "read_full_result" {
        t.Errorf("unexpected name: %s", tool.Name())
    }

    argsJSON, _ := json.Marshal(map[string]any{
        "id":     id,
        "offset": 4,
        "length": 5,
    })

    result, err := tool.Execute(context.Background(), argsJSON)
    if err != nil {
        t.Fatalf("execute failed: %v", err)
    }
    if !strings.Contains(result.Content, "quick") {
        t.Errorf("expected 'quick' in content, got %q", result.Content)
    }
    if !strings.Contains(result.Content, "more remaining") && !strings.Contains(result.Content, "of 43 runes") {
        t.Errorf("expected continuation marker, got %q", result.Content)
    }
}

func TestReadFullResultUnknownID(t *testing.T) {
    store := core.NewInMemoryToolResultStore()
    tool := agent.NewReadFullResultTool(store)
    argsJSON, _ := json.Marshal(map[string]any{"id": "no-such-id", "offset": 0, "length": 10})

    result, err := tool.Execute(context.Background(), argsJSON)
    if err == nil && !strings.Contains(result.Content, "not found") {
        t.Errorf("expected error or 'not found' content, got result=%+v err=%v", result, err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run "TestReadFullResult" -v
```

Expected: FAIL — `NewReadFullResultTool` undefined.

- [ ] **Step 3: Implement the built-in tool using Phase 1.5's typed schema**

Create `agent/read_full_result.go`:

```go
package agent

import (
    "context"
    "errors"
    "fmt"

    "github.com/nevindra/oasis/core"
)

// ReadFullResultIn is the input schema for the read_full_result built-in tool.
type ReadFullResultIn struct {
    ID     string `json:"id" describe:"the opaque id from a truncation marker"`
    Offset int    `json:"offset" describe:"starting rune offset"`
    Length int    `json:"length" describe:"max runes to return (recommend 50000)"`
}

// ReadFullResultOut is the output schema.
type ReadFullResultOut struct {
    Content string `json:"content"`
    Total   int    `json:"total"`
    More    bool   `json:"more"`
}

type readFullResultTool struct {
    store core.ToolResultStore
}

// NewReadFullResultTool returns the read_full_result tool bound to the given
// store. The tool is auto-registered on every agent that has a ToolResultStore
// configured (which is the default).
func NewReadFullResultTool(store core.ToolResultStore) core.AnyTool {
    return core.Erase[ReadFullResultIn, ReadFullResultOut](&readFullResultTool{store: store})
}

func (t *readFullResultTool) Name() string {
    return "read_full_result"
}

func (t *readFullResultTool) Description() string {
    return "Retrieve a slice of a previously-truncated tool result. " +
        "Use the id from a [truncated at N runes of M total. Use read_full_result(...)] marker."
}

func (t *readFullResultTool) Execute(ctx context.Context, in ReadFullResultIn) (ReadFullResultOut, error) {
    if in.Length <= 0 {
        in.Length = 50_000
    }
    content, total, err := t.store.Get(ctx, in.ID, in.Offset, in.Length)
    if errors.Is(err, core.ErrToolResultNotFound) {
        return ReadFullResultOut{}, fmt.Errorf("result id %q not found or expired", in.ID)
    }
    if err != nil {
        return ReadFullResultOut{}, err
    }
    more := in.Offset+len([]rune(content)) < total
    out := ReadFullResultOut{
        Content: content,
        Total:   total,
        More:    more,
    }
    if more {
        out.Content += fmt.Sprintf("\n\n[%d of %d runes returned, more remaining — call read_full_result(id=%q, offset=%d) for the next chunk]",
            in.Offset+len([]rune(content)), total, in.ID, in.Offset+len([]rune(content)))
    }
    return out, nil
}
```

(`core.Erase[In, Out]` is the existing Phase 1.5 type-eraser. If the exact function name differs, check `core/tool.go` and adjust.)

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run "TestReadFullResult" -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/read_full_result.go agent/read_full_result_test.go
git commit -m "feat(agent): add read_full_result built-in tool

Typed Tool[ReadFullResultIn, ReadFullResultOut] that retrieves slices
from a ToolResultStore. Returns a continuation marker when more content
remains. Auto-registered by the agent runtime when a store is configured
(default). Part of finding 3.7."
```

---

#### Task 8: Track A4 — Integrate ToolResultStore into the loop's truncation site

**Files:**
- Modify: `agent/loop.go:120` (remove const), lines 467-473 (truncation site)
- Modify: `agent/agent.go` (add `toolResultStore` field + `WithToolResultStore` option)
- Modify: `agent/agentcore.go` (wire store into LoopConfig + auto-register read_full_result tool)
- Test: `agent/loop_test.go` or new `agent/tool_result_store_integration_test.go`

- [ ] **Step 1: Write the failing integration test**

Create `agent/tool_result_store_integration_test.go`:

```go
package agent_test

import (
    "context"
    "strings"
    "testing"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

func TestOversizeToolResultStored(t *testing.T) {
    bigOutput := strings.Repeat("x", 200_000)
    tool := newFakeTool("big_tool", bigOutput)
    provider := newFakeProviderReturning("call big_tool")

    store := core.NewInMemoryToolResultStore()
    a := oasis.NewLLMAgent("test", "", provider,
        oasis.WithTools(tool),
        oasis.WithToolResultStore(store),
        oasis.WithMaxToolResultLen(100_000),
    )

    result, err := a.Execute(context.Background(), oasis.AgentTask{Input: "go"})
    if err != nil { t.Fatal(err) }

    // The message handed to the LLM should contain a paging marker with an id.
    foundMarker := false
    for _, step := range result.Steps {
        if strings.Contains(step.Content, "Use read_full_result(id=") {
            foundMarker = true
            break
        }
    }
    if !foundMarker {
        t.Error("expected paging marker in tool result, got none")
    }

    // The id from the marker should be retrievable from the store.
    // (Test the cross-check by listing entries via Get with various ids — skipped here.)
}

func TestNoStoreFallsBackToLegacyMarker(t *testing.T) {
    bigOutput := strings.Repeat("y", 200_000)
    tool := newFakeTool("big_tool", bigOutput)
    provider := newFakeProviderReturning("call big_tool")

    a := oasis.NewLLMAgent("test", "", provider,
        oasis.WithTools(tool),
        oasis.WithToolResultStore(nil), // explicit opt-out
        oasis.WithMaxToolResultLen(100_000),
    )

    result, _ := a.Execute(context.Background(), oasis.AgentTask{Input: "go"})

    foundLegacy := false
    for _, step := range result.Steps {
        if strings.Contains(step.Content, "[output truncated") {
            foundLegacy = true
            break
        }
    }
    if !foundLegacy {
        t.Error("expected legacy truncation marker when store is nil")
    }
}
```

(`newFakeTool` and `newFakeProviderReturning` should already exist as test helpers in `agent/` — match the existing pattern. If not, write minimal versions.)

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run "TestOversizeToolResultStored|TestNoStoreFallsBackToLegacyMarker" -v
```

Expected: FAIL — `WithToolResultStore` undefined.

- [ ] **Step 3: Add `WithToolResultStore` option and `toolResultStore` field**

In `agent/agent.go`:

```go
// Add to agentConfig struct:
toolResultStore        core.ToolResultStore
toolResultStoreSet     bool // distinguishes "default" from "explicitly nil"

// Add option function:
//
// WithToolResultStore overrides the default in-memory tool-result store.
// Pass nil to disable result paging (oversize results get the legacy
// truncation marker with no id; the read_full_result tool is not registered).
func WithToolResultStore(s core.ToolResultStore) AgentOption {
    return func(c *agentConfig) {
        c.toolResultStore = s
        c.toolResultStoreSet = true
    }
}
```

In `BuildConfig`, after applying options:

```go
if !c.toolResultStoreSet {
    c.toolResultStore = core.NewInMemoryToolResultStore() // default on
}
```

- [ ] **Step 4: Wire into LoopConfig and runtime**

In `agent/loop.go`, add to `LoopConfig`:

```go
toolResultStore  core.ToolResultStore
maxToolResultLen int
```

In `agent/agentcore.go`'s `InitCore` (or wherever `LoopConfig` is built), populate:

```go
cfg.toolResultStore = c.toolResultStore
cfg.maxToolResultLen = c.maxToolResultLen
```

In `agent/loop.go:469-475` (truncation site), replace:

```go
if utf8.RuneCountInString(msgContent) > maxToolResultMessageLen {
    msgContent = TruncateStr(msgContent, maxToolResultMessageLen) + "\n\n[output truncated — original was longer]"
}
```

with:

```go
if utf8.RuneCountInString(msgContent) > cfg.maxToolResultLen {
    inline := TruncateStr(msgContent, cfg.maxToolResultLen)
    total := utf8.RuneCountInString(msgContent)
    if cfg.toolResultStore != nil {
        id, putErr := cfg.toolResultStore.Put(iterCtx, msgContent)
        if putErr == nil {
            msgContent = inline + fmt.Sprintf("\n\n[truncated at %d runes of %d total. Use read_full_result(id=%q, offset=%d, length=50000) for more]",
                cfg.maxToolResultLen, total, id, cfg.maxToolResultLen)
        } else {
            cfg.logger.Warn("tool result store put failed, falling back to legacy marker",
                "agent", cfg.name, "error", putErr)
            msgContent = inline + "\n\n[output truncated — original was longer]"
        }
    } else {
        msgContent = inline + "\n\n[output truncated — original was longer]"
    }
}
```

Remove the `const maxToolResultMessageLen = 100_000` declaration at `agent/loop.go:120`.

- [ ] **Step 5: Auto-register `read_full_result` when store is configured**

In `agent/agentcore.go`'s tool registration setup (near `CacheBuiltinToolDefs` or equivalent), add:

```go
if c.toolResultStore != nil {
    c.tools = append(c.tools, NewReadFullResultTool(c.toolResultStore))
}
```

Locate the exact insertion point by searching for existing built-in tool registration patterns.

- [ ] **Step 6: Run integration tests**

```bash
go test ./agent/ -run "TestOversizeToolResultStored|TestNoStoreFallsBackToLegacyMarker" -v -race
```

Expected: PASS.

- [ ] **Step 7: Run full agent suite to catch regressions**

```bash
go test ./agent/ -race
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add agent/agent.go agent/agentcore.go agent/loop.go agent/tool_result_store_integration_test.go
git commit -m "feat(agent)!: integrate ToolResultStore into the agent loop

Oversize tool results now get stored in a ToolResultStore (default
in-memory, 10MiB/5min) and the LLM receives a paging marker with an
id and offset/length hint. Auto-registers the read_full_result tool
when a store is configured. WithToolResultStore(nil) opts out to
the legacy truncate-with-marker behavior. Closes finding 3.7."
```

---

#### Task 9: Track A5 — Re-export ToolResultStore types from `oasis.go`

**Files:**
- Modify: `oasis.go`

- [ ] **Step 1: Add re-exports**

In `oasis.go`, add (matching the existing re-export block style):

```go
type (
    ToolResultStore = core.ToolResultStore
)

var (
    NewInMemoryToolResultStore  = core.NewInMemoryToolResultStore
    WithToolResultMaxBytes      = core.WithToolResultMaxBytes
    WithToolResultTTL           = core.WithToolResultTTL
    ErrToolResultNotFound       = core.ErrToolResultNotFound
    WithToolResultStore         = agent.WithToolResultStore
)
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Verify the public API surface**

```bash
go doc ./... | grep -i toolresult
```

Expected: types/functions appear under `oasis` package.

- [ ] **Step 4: Commit**

```bash
git add oasis.go
git commit -m "feat: re-export ToolResultStore types from oasis umbrella

Surface ToolResultStore, NewInMemoryToolResultStore, the two option
helpers, ErrToolResultNotFound, and WithToolResultStore from the
curated public API. Part of finding 3.7."
```

---

### Wave 2

#### Task 10: Track D1 — Add `CompactRequest.Scope` field + constants

**Files:**
- Modify: `core/compactor.go`
- Test: `core/compactor_test.go` (new or existing)

- [ ] **Step 1: Write the failing test**

Create or append to `core/compactor_test.go`:

```go
func TestCompactRequestDefaultScopeIsFull(t *testing.T) {
    var req core.CompactRequest
    if req.Scope != core.ScopeFull {
        t.Errorf("expected ScopeFull as zero value, got %v", req.Scope)
    }
}

func TestScopeToolResultsOnlyDistinctFromFull(t *testing.T) {
    if core.ScopeFull == core.ScopeToolResultsOnly {
        t.Error("ScopeFull and ScopeToolResultsOnly must be distinct")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./core/ -run "TestCompactRequest|TestScope" -v
```

Expected: FAIL — `Scope`/`ScopeFull`/`ScopeToolResultsOnly` undefined.

- [ ] **Step 3: Add the field and constants**

In `core/compactor.go`, before `CompactRequest`:

```go
// CompactScope tells the Compactor what subset of messages to summarize.
type CompactScope int

const (
    // ScopeFull instructs the Compactor to summarize the full message slice
    // into a structured synopsis. This is the default and preserves
    // pre-Phase-2 behavior.
    ScopeFull CompactScope = iota

    // ScopeToolResultsOnly instructs the Compactor to compress only the
    // tool-result messages in the slice and leave user/assistant messages
    // intact. Used by the per-turn rune-count compression path.
    ScopeToolResultsOnly
)
```

Add the field to `CompactRequest`:

```go
type CompactRequest struct {
    // ... existing fields above ...

    // Scope tells the Compactor what subset of Messages to summarize.
    // Zero value is ScopeFull (today's behavior).
    Scope CompactScope
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./core/ -run "TestCompactRequest|TestScope" -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/compactor.go core/compactor_test.go
git commit -m "feat(core): add CompactRequest.Scope + ScopeFull/ScopeToolResultsOnly

Lets Compactor implementations distinguish full-thread synopsis from
per-turn tool-result compression. Zero value is ScopeFull so existing
Compactors keep working. Part of finding 1.2.f."
```

---

#### Task 11: Track D2 — Default `inlineCompactor` implementation handling both scopes

**Files:**
- Create: `agent/inline_compactor.go`
- Test: `agent/inline_compactor_test.go`

- [ ] **Step 1: Write the failing tests**

Create `agent/inline_compactor_test.go`:

```go
func TestInlineCompactorFullScope(t *testing.T) {
    provider := newFakeProviderReturning("[FULL_SUMMARY]")
    c := agent.NewInlineCompactor(provider)
    result, err := c.Compact(context.Background(), core.CompactRequest{
        Messages: []core.ChatMessage{
            core.UserMessage("hello"),
            core.AssistantMessage("world"),
        },
        Scope: core.ScopeFull,
    })
    if err != nil { t.Fatal(err) }
    if !strings.Contains(result.SummaryText, "FULL_SUMMARY") {
        t.Errorf("expected FULL_SUMMARY in result, got %q", result.SummaryText)
    }
}

func TestInlineCompactorToolResultsOnly(t *testing.T) {
    provider := newFakeProviderReturning("[TOOL_RESULTS_SUMMARY]")
    c := agent.NewInlineCompactor(provider)

    // Mixed input: user + tool result + assistant + tool result
    msgs := []core.ChatMessage{
        core.UserMessage("question"),
        core.ToolResultMessage("call1", "huge tool output 1"),
        core.AssistantMessage("intermediate"),
        core.ToolResultMessage("call2", "huge tool output 2"),
    }
    result, err := c.Compact(context.Background(), core.CompactRequest{
        Messages: msgs,
        Scope:    core.ScopeToolResultsOnly,
    })
    if err != nil { t.Fatal(err) }
    if !strings.Contains(result.SummaryText, "TOOL_RESULTS_SUMMARY") {
        t.Errorf("expected TOOL_RESULTS_SUMMARY in result, got %q", result.SummaryText)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./agent/ -run "TestInlineCompactor" -v
```

Expected: FAIL — `NewInlineCompactor` undefined.

- [ ] **Step 3: Implement `inlineCompactor`**

Create `agent/inline_compactor.go`:

```go
package agent

import (
    "context"
    "fmt"
    "strings"

    "github.com/nevindra/oasis/core"
)

// NewInlineCompactor returns a Compactor that delegates to the given LLM
// provider using a default prompt. Handles both ScopeFull (full-thread
// synopsis) and ScopeToolResultsOnly (tool-result compression). This is the
// default Compactor when none is configured via WithCompactor.
func NewInlineCompactor(provider core.Provider) core.Compactor {
    return &inlineCompactor{provider: provider}
}

type inlineCompactor struct {
    provider core.Provider
}

func (c *inlineCompactor) Compact(ctx context.Context, req core.CompactRequest) (core.CompactResult, error) {
    if c.provider == nil {
        return core.CompactResult{}, fmt.Errorf("inlineCompactor: no provider configured")
    }
    summarizer := req.SummarizerProvider
    if summarizer == nil {
        summarizer = c.provider
    }

    var prompt string
    switch req.Scope {
    case core.ScopeToolResultsOnly:
        prompt = "Summarize the following tool execution results concisely. " +
            "Preserve key facts, data values, decisions, and errors. Omit redundant details."
    default: // ScopeFull
        prompt = "Summarize the following conversation, preserving key decisions, " +
            "facts, and intermediate results. Be concise but complete."
    }

    var b strings.Builder
    for _, m := range req.Messages {
        b.WriteString(string(m.Role))
        b.WriteString(": ")
        b.WriteString(m.Content)
        b.WriteString("\n")
    }

    resp, err := summarizer.Chat(ctx, core.ChatRequest{
        Messages: []core.ChatMessage{
            core.SystemMessage(prompt),
            core.UserMessage(b.String()),
        },
    })
    if err != nil {
        return core.CompactResult{}, err
    }

    return core.CompactResult{
        SummaryText:   resp.Content,
        Sections:      map[string]string{"summary": resp.Content},
        SourceTokens:  resp.Usage.InputTokens,
        SummaryTokens: resp.Usage.OutputTokens,
    }, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run "TestInlineCompactor" -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/inline_compactor.go agent/inline_compactor_test.go
git commit -m "feat(agent): inlineCompactor handles ScopeFull and ScopeToolResultsOnly

Default Compactor used when WithCompactor is not configured. Replaces
the inline English prompt previously embedded in compressMessages.
Users can swap via WithCompactor(custom) to localize or customize
the summarization behavior. Part of findings 1.2.f and 3.9."
```

---

#### Task 12: Track D3 — Refactor `compressMessages` to use Compactor with Scope

**Files:**
- Modify: `agent/loop.go:574-...` (replace `compressMessages` body)
- Test: `agent/loop_compress_test.go` (existing or new)

- [ ] **Step 1: Write the failing test**

Add to `agent/loop_compress_test.go`:

```go
func TestCompressMessagesRoutesThroughCompactor(t *testing.T) {
    recorded := []core.CompactScope{}
    fake := &fakeCompactor{
        compact: func(ctx context.Context, req core.CompactRequest) (core.CompactResult, error) {
            recorded = append(recorded, req.Scope)
            return core.CompactResult{SummaryText: "compacted"}, nil
        },
    }

    cfg := agent.LoopConfig{} // populate minimally — needs cfg.compactor = fake
    // ... build cfg via internal helpers, set cfg.compactor = fake

    msgs := []core.ChatMessage{
        core.UserMessage("user"),
        core.ToolResultMessage("call1", strings.Repeat("x", 50_000)),
        core.AssistantMessage("intermediate"),
        core.ToolResultMessage("call2", strings.Repeat("y", 50_000)),
    }
    // Invoke the package-internal compressMessages via an exported test hook
    // or by triggering the loop with a deliberately small threshold.
    // ...

    if len(recorded) != 1 || recorded[0] != core.ScopeToolResultsOnly {
        t.Errorf("expected one Compact call with ScopeToolResultsOnly, got %+v", recorded)
    }
}

type fakeCompactor struct {
    compact func(ctx context.Context, req core.CompactRequest) (core.CompactResult, error)
}

func (f *fakeCompactor) Compact(ctx context.Context, req core.CompactRequest) (core.CompactResult, error) {
    return f.compact(ctx, req)
}
```

If `compressMessages` is unexported and there's no test hook, add an exported `TestCompressMessages` wrapper in `agent/export_test.go`:

```go
package agent

func TestCompressMessagesExternal(ctx context.Context, cfg LoopConfig, task AgentTask, msgs []ChatMessage, preserve, runes int) ([]ChatMessage, int) {
    return compressMessages(ctx, cfg, task, msgs, preserve, runes)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./agent/ -run TestCompressMessagesRoutesThroughCompactor -v
```

Expected: FAIL — compactor field absent or compressMessages doesn't route through it.

- [ ] **Step 3: Refactor `compressMessages`**

Locate `compressMessages` in `agent/loop.go` (line 574). The current implementation does an inline LLM call with a hardcoded prompt. Replace with:

```go
func compressMessages(
    ctx context.Context,
    cfg LoopConfig,
    task AgentTask,
    messages []ChatMessage,
    preserveIters, currentRuneCount int,
) ([]ChatMessage, int) {
    // Identify the slice of old tool-result messages to compact.
    // (Preserve the last preserveIters iterations of messages.)
    oldEnd := len(messages) - preserveIters*2 // heuristic — match existing behavior
    if oldEnd <= 0 {
        return messages, currentRuneCount
    }
    oldSlice := messages[:oldEnd]

    // Pick the Compactor: prefer cfg.compactor, fall back to a default inline
    // compactor backed by cfg.compressModel or cfg.provider.
    compactor := cfg.compactor
    if compactor == nil {
        provider := cfg.provider
        if cfg.compressModel != nil {
            provider = cfg.compressModel(ctx, task)
        }
        compactor = NewInlineCompactor(provider)
    }

    // Issue the compaction with the new Scope.
    result, err := compactor.Compact(ctx, CompactRequest{
        Messages: oldSlice,
        Scope:    ScopeToolResultsOnly,
    })
    if err != nil {
        cfg.logger.Warn("compaction failed, leaving messages untouched",
            "agent", cfg.name, "error", err)
        return messages, currentRuneCount
    }

    summaryMsg := SystemMessage("[Summary of earlier tool results]\n" + result.SummaryText)
    compressed := make([]ChatMessage, 0, len(messages)-oldEnd+1)
    compressed = append(compressed, summaryMsg)
    compressed = append(compressed, messages[oldEnd:]...)

    newRuneCount := utf8.RuneCountInString(summaryMsg.Content)
    for _, m := range messages[oldEnd:] {
        newRuneCount += utf8.RuneCountInString(m.Content)
    }
    return compressed, newRuneCount
}
```

(Exact `preserveIters` arithmetic should match the existing `compressMessages` logic to preserve regression equivalence — read the existing function first and translate behavior, don't reinvent.)

Add `compactor core.Compactor` to `LoopConfig` if not already present. Populate from `agentConfig.compactor` in `InitCore`.

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./agent/ -run TestCompressMessages -v
```

Expected: PASS.

- [ ] **Step 5: Run full loop tests to catch regressions**

```bash
go test ./agent/ -race
```

Expected: PASS. The default `inlineCompactor` should preserve existing output approximately enough that any regression test asserting "summary contains key X" still passes.

- [ ] **Step 6: Commit**

```bash
git add agent/loop.go agent/loop_compress_test.go agent/export_test.go
git commit -m "refactor(agent): route compressMessages through Compactor with ScopeToolResultsOnly

The hardcoded English compression prompt is removed; compressMessages
now builds a CompactRequest with ScopeToolResultsOnly and dispatches
to cfg.compactor (or a default inlineCompactor). Users can customize
compression by providing a Compactor via WithCompactor that handles
both ScopeFull and ScopeToolResultsOnly. Closes findings 1.2.f and 3.9."
```

---

#### Task 13: Track E1 — Write regression test that exposes the double-close

**Files:**
- Modify: `agent/agentcore_test.go` (existing) — add a regression test

- [ ] **Step 1: Identify the bypass path**

Read `agent/agentcore.go:332-410` (`forwardSubagentStream`). Look for any `close(...)` call that doesn't go through the `safeClose` returned by `onceClose`. Search:

```bash
grep -n "close(subCh)\|close(ch)" agent/agentcore.go
```

Document which line is the bypass: ____________

- [ ] **Step 2: Write a regression test that double-closes the channel**

Add to `agent/agentcore_test.go`:

```go
func TestForwardSubagentStreamDoubleCloseSafe(t *testing.T) {
    // This test exercises both close paths in forwardSubagentStream:
    // (a) the deferred safeClose at function exit, and
    // (b) any other close inside the function or its goroutines.
    //
    // Without the fix, removing the recover() in onceClose causes a
    // "close of closed channel" panic. With the fix, both paths go
    // through the same sync.Once and no panic occurs.

    // Construct a subagent that completes immediately so the deferred
    // close fires, AND set up timing such that the goroutine's drain
    // close also fires. Exact harness depends on the bypass site.
    // ...
}
```

(This test will be fleshed out in Task 14 once the bypass site is identified. For now, write a placeholder that panics.)

- [ ] **Step 3: Temporarily remove the recover, confirm panic**

In `agent/agentcore.go:423-431`, remove the `defer func() { recover() }()` line:

```go
func onceClose[T any](ch chan<- T) func() {
    var once sync.Once
    return func() {
        once.Do(func() {
            close(ch)
        })
    }
}
```

Then run:

```bash
go test ./agent/ -run TestLLMAgentExecuteStreamNoTools -v -race
```

Expected: PANIC with "close of closed channel". This confirms the bypass path exists. Record the panic stack trace — it points to the second close site. Note the file/line: ____________

- [ ] **Step 4: Restore the recover (we'll remove it for good after Task 14)**

Restore the original `onceClose` body. Commit a no-op so we have a clean state:

```bash
git diff agent/agentcore.go    # should be empty after restore
```

(No commit yet — Task 14 fixes the real bug.)

---

#### Task 14: Track E2 — Route the bypass close through `onceClose`

**Files:**
- Modify: `agent/agentcore.go` (the bypass site identified in Task 13)
- Modify: `agent/agentcore.go:423` (`onceClose`) — remove `recover()`
- Modify: `agent/agentcore_test.go` — finalize the regression test

- [ ] **Step 1: Route the bypass through `safeClose`**

Based on the bypass site from Task 13 Step 3:

- If the bypass is `close(subCh)` inside `forwardSubagentStream`, replace it with the `safeClose` closure already in scope.
- If the bypass is in a goroutine spawned by `forwardSubagentStream`, ensure the goroutine receives a reference to the same `safeClose` and uses it instead of raw `close`.
- If `safeClose` is not yet in scope at that line, hoist its initialization earlier.

Concretely (likely shape):

```go
// In forwardSubagentStream, near the top:
safeClose := onceClose(ch)
defer safeClose()

// ... later in a goroutine or branch:
// REPLACE: close(ch)
// WITH:    safeClose()
```

Read the surrounding code carefully — the exact change depends on the structure found in Task 13.

- [ ] **Step 2: Remove `recover()` from `onceClose`**

In `agent/agentcore.go:423-431`:

```go
func onceClose[T any](ch chan<- T) func() {
    var once sync.Once
    return func() {
        once.Do(func() {
            close(ch)
        })
    }
}
```

Also update the comment above to remove the "Why the recover" paragraph; replace with:

```go
// onceClose returns a function that closes the given channel exactly once.
// Safe to call multiple times; subsequent calls are no-ops. Accepts send-only
// channels (close is valid on chan<- T per Go spec).
```

- [ ] **Step 3: Flesh out the regression test**

Update the placeholder test from Task 13:

```go
func TestForwardSubagentStreamDoubleCloseSafe(t *testing.T) {
    // Drive the agent via ExecuteStream and verify that BOTH the inner
    // close path and the deferred safeClose path run without panic.
    a := newFakeAgent(t)
    task := AgentTask{Input: "hello"}

    out := make(chan StreamEvent, 64)
    err := a.ExecuteStream(context.Background(), task, out)
    if err != nil { t.Fatal(err) }

    // Drain out to completion.
    for range out {}
    // No panic = pass.
}
```

- [ ] **Step 4: Run tests with race detector**

```bash
go test ./agent/ -run "TestForwardSubagentStreamDoubleCloseSafe|TestLLMAgentExecuteStreamNoTools" -v -race
go test ./agent/ -race
```

Expected: PASS, no panics, no race warnings.

- [ ] **Step 5: Commit**

```bash
git add agent/agentcore.go agent/agentcore_test.go
git commit -m "fix(agent): route forwardSubagentStream's bypass close through onceClose

The bypass close path at [LINE FROM TASK 13] now goes through the
same sync.Once that protects the deferred close at function exit.
This was previously masked by a recover() in onceClose, which is
now removed. Closes finding 2.2.g."
```

---

### Wave 3

#### Task 15: Track F — Add `EventMaxIterReached`, emit it, raise `defaultMaxIter`

**Files:**
- Modify: `core/stream.go` (new event constant + payload struct)
- Modify: `agent/loop.go:491-494` (emit before forced synthesis)
- Modify: `agent/agentcore.go:16` (`defaultMaxIter` 10 → 25)
- Test: `agent/loop_test.go` (event emission test) + `agent/agentcore_test.go` (default test)

- [ ] **Step 1: Write the failing tests**

Add to `agent/loop_test.go`:

```go
func TestEventMaxIterReachedEmitted(t *testing.T) {
    // Agent that always wants to call a tool — will hit maxIter every time.
    provider := newFakeProviderAlwaysCallingTool("loop_tool")
    a := oasis.NewLLMAgent("test", "", provider,
        oasis.WithTools(newFakeTool("loop_tool", "still going")),
        oasis.WithMaxIter(3), // force max-iter quickly
    )

    out := make(chan core.StreamEvent, 64)
    _ = a.ExecuteStream(context.Background(), oasis.AgentTask{Input: "loop"}, out)
    var saw bool
    for ev := range out {
        if ev.Type == core.EventMaxIterReached {
            saw = true
            if ev.Content == "" {
                t.Error("EventMaxIterReached content should carry iter/maxIter JSON")
            }
        }
    }
    if !saw {
        t.Error("expected EventMaxIterReached, got none")
    }
}

func TestDefaultMaxIterIs25(t *testing.T) {
    cfg := agent.BuildConfig(nil)
    // Note: defaultMaxIter is applied in InitCore, not BuildConfig. Use the
    // effective default by calling InitCore (or expose via test helper).
    core := agent.InitCore(cfg) // adjust based on actual API
    if core.MaxIter != 25 {
        t.Errorf("expected defaultMaxIter 25, got %d", core.MaxIter)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./agent/ -run "TestEventMaxIterReachedEmitted|TestDefaultMaxIterIs25" -v
```

Expected: FAIL — `core.EventMaxIterReached` undefined; default still 10.

- [ ] **Step 3: Add the event constant**

In `core/stream.go`, after `EventFileAttachment`:

```go
// EventMaxIterReached signals that the agent loop hit its iteration limit
// and is about to force a synthesis LLM call. Content carries a JSON object
// {"iter":N,"max_iter":M}. Emitted exactly once per execution that hits the
// cap.
EventMaxIterReached StreamEventType = "max-iter-reached"
```

- [ ] **Step 4: Emit the event in `runLoop`**

In `agent/loop.go:491-494`, before the "max iterations — force synthesis" log, add:

```go
// Surface the max-iter hit so UIs can show the forced-synthesis cost.
if ch != nil {
    payload, _ := json.Marshal(map[string]int{
        "iter":     cfg.maxIter,
        "max_iter": cfg.maxIter,
    })
    select {
    case ch <- StreamEvent{
        Type:    EventMaxIterReached,
        Name:    cfg.name,
        Content: string(payload),
    }:
    case <-ctx.Done():
        safeCloseCh()
        return AgentResult{Usage: totalUsage}, ctx.Err()
    }
}
```

- [ ] **Step 5: Raise `defaultMaxIter` to 25**

In `agent/agentcore.go:16`:

```go
const defaultMaxIter = 25
```

- [ ] **Step 6: Run tests to verify they pass**

```bash
go test ./agent/ -run "TestEventMaxIterReachedEmitted|TestDefaultMaxIterIs25" -v -race
```

Expected: PASS.

- [ ] **Step 7: Run full suite**

```bash
go test ./agent/ -race
go test ./... -race
```

Expected: PASS. Any test that asserted `maxIter == 10` either needs updating (if testing the default) or already uses `WithMaxIter(...)` explicitly.

- [ ] **Step 8: Commit**

```bash
git add core/stream.go agent/loop.go agent/agentcore.go agent/loop_test.go agent/agentcore_test.go
git commit -m "feat: emit EventMaxIterReached, raise default maxIter 10→25

UIs can now observe when the loop forces a synthesis LLM call due to
hitting the iteration cap. Default cap raised because real tool-using
workflows commonly need 15-20 iterations; users wanting old behavior
set WithMaxIter(10). Closes findings 3.6 and 3.8 (maxIter portion)."
```

---

### Wave 4

#### Task 16: Document the history cascade in `docs/concepts/memory.md`

**Files:**
- Modify: `docs/concepts/memory.md`

- [ ] **Step 1: Add a "History shrinking" section documenting the cascade**

Add (or restructure existing content into) a section after the current memory discussion:

```markdown
## History shrinking strategies

Oasis offers three independent mechanisms for keeping conversation history under control. They cascade — each runs when its own threshold is exceeded, in increasing order of cost:

| Stage | Mechanism | Trigger | Effect |
|---|---|---|---|
| 1 | Semantic trimming (`WithSemanticTrimming`) | `MaxTokens` exceeded | Drops semantically distant messages first (cosine similarity to current query) |
| 2 | Tool-result compression (`WithCompressThreshold` + `compressModel`) | Per-turn rune count > threshold | Summarizes old tool-result messages via the configured Compactor with `ScopeToolResultsOnly` |
| 3 | Full-thread compaction (`WithCompactor` + `compactThreshold`) | Thread-level rune count > threshold | Summarizes the whole conversation via the configured Compactor with `ScopeFull` |

You can enable any combination. They are layered, not alternatives — Stage 1 culls before Stage 2 summarizes; Stage 3 is the heaviest fallback.

Internally, Stages 2 and 3 both dispatch to the `Compactor` interface (`core.Compactor`). The default `inlineCompactor` handles both `ScopeFull` and `ScopeToolResultsOnly`. Custom Compactors can implement domain-specific summarization, localization, or per-agent customization by switching on `req.Scope`.
```

- [ ] **Step 2: Verify markdown renders cleanly**

```bash
grep -c "ScopeFull\|ScopeToolResultsOnly" docs/concepts/memory.md
```

Expected: ≥2 occurrences (in the table and the trailing paragraph).

- [ ] **Step 3: Commit**

```bash
git add docs/concepts/memory.md
git commit -m "docs(memory): document the three-stage history-shrink cascade

Explains the order in which semantic trim, tool-result compression,
and full-thread compaction run, and how Stages 2 and 3 both route
through the Compactor interface. Closes the doc gap for finding 1.2.f."
```

---

#### Task 17: Update CHANGELOG with Phase 2 entries

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add Phase 2 section under `[Unreleased]`**

Add (under `[Unreleased]` — adjust if user has tagged a version already):

```markdown
## [Unreleased]

### Breaking

- **`AgentHandle.State()` no longer blocks.** Callers that read `Result()` after `State().IsTerminal()` must insert `h.Sync()` between the two. Migration hint: `grep -n 'State().IsTerminal' your-project/` and add `Sync()` calls. (#3.4)
- **Conflicting embedding providers rejected at build time.** `WithUserMemory(em1, ...)` and `WithHistory(history.CrossThreadSearch(em2, ...))` with non-equal embeddings now return an error from agent construction. Pass the same `EmbeddingProvider` to both, or pick one. (#1.2.g)
- **`WithSandbox(any)` is now `WithSandbox(core.Sandbox)`.** The `sandbox/` satellite's existing type already implements the interface — no satellite changes needed. Custom sandbox types must implement `Exec(ctx, lang, code) (stdout, stderr, error)` and `Close() error`. (#1.2.k)

### Changed

- **Default `maxIter` raised 10 → 25.** Real tool-using workflows commonly need 15-20 iterations. Set `WithMaxIter(10)` to restore the old default. (#3.6)
- **`compressMessages` now routes through the `Compactor` interface** instead of an inline English prompt. Users with custom `Compactor` implementations should handle both `ScopeFull` and `ScopeToolResultsOnly` (default `inlineCompactor` does both). (#1.2.f, #3.9)

### Added

- **`core.ToolResultStore` interface** + default in-memory implementation (`core.NewInMemoryToolResultStore`) for paging large tool results. Auto-enabled with 10 MiB total cap and 5-minute TTL per entry; opt out with `WithToolResultStore(nil)`. (#3.7)
- **`read_full_result` built-in tool** for the LLM to retrieve slices of stored results. Auto-registered when a `ToolResultStore` is configured. (#3.7)
- **`core.Sandbox` interface** — see breaking note above. (#1.2.k)
- **`CompactRequest.Scope`** field with `ScopeFull` and `ScopeToolResultsOnly` constants. (#1.2.f)
- **`AgentHandle.Sync()`** — see breaking note above. (#3.4)
- **`core.EventMaxIterReached`** stream event emitted before forced synthesis. (#3.6)
- **Three new options:** `WithToolResultStore`, `WithMaxToolResultLen`, `WithMaxParallelDispatch`, `WithMaxPlanSteps`. (#3.7, #3.8)

### Fixed

- **`forwardSubagentStream` double-close** routed through a single `sync.Once`. The `recover()` in `onceClose` is removed; the real bypass path is fixed. (#2.2.g)

### Removed

- Inline English compression prompt in `agent/loop.go` (replaced by `inlineCompactor`).
```

- [ ] **Step 2: Verify the structure**

```bash
head -80 CHANGELOG.md
```

Expected: `[Unreleased]` block contains all six subsections (Breaking, Changed, Added, Fixed, Removed).

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): Phase 2 coherence release entries"
```

---

#### Task 18: Final repo-wide verification

**Files:**
- Read-only — runs verification

- [ ] **Step 1: Run root tests with race detector**

```bash
go test ./... -race
```

Expected: PASS (root module, all packages).

- [ ] **Step 2: Run all 9 satellites**

```bash
for sat in mcp store/sqlite store/postgres provider/gemini provider/openaicompat observer ingest sandbox rag; do
  echo "=== $sat ==="
  (cd "$sat" && go test ./... -race) || echo "FAIL: $sat"
done
```

Expected: PASS for every satellite.

- [ ] **Step 3: Run linter**

```bash
golangci-lint run ./...
```

Expected: clean.

- [ ] **Step 4: Search for stale references**

```bash
# These constants should no longer exist as unexported package-level consts.
grep -rn "const maxToolResultMessageLen" agent/
grep -rn "const maxParallelDispatch" agent/
grep -rn "const maxPlanSteps" agent/

# These should NOT appear (old recover comment, old hardcoded prompts).
grep -rn "Summarize the following tool execution results" agent/loop.go
grep -rn "absorbs that race" agent/agentcore.go
```

Expected: all greps return zero matches.

- [ ] **Step 5: Verify the public API surface**

```bash
go doc github.com/nevindra/oasis | grep -E "ToolResultStore|Sandbox|Sync|MaxIter|EventMaxIterReached|MaxToolResultLen|MaxParallelDispatch|MaxPlanSteps"
```

Expected: all new symbols appear under the umbrella.

- [ ] **Step 6: Commit verification notes (if needed)**

If the prior commits left an empty workspace, no commit. If any verification triggered a fix, commit it separately:

```bash
git status                                       # confirm clean working tree
git log --oneline migration/microkernel..HEAD   # review Phase 2 commits
```

---

## Self-Review Checklist (run after writing each task)

- [ ] Every step has actual content (no "TBD", "implement details", "add error handling")
- [ ] File paths are absolute or unambiguous relative paths
- [ ] Every code block has expected commands and expected output
- [ ] Type/method names match between tasks (e.g., `ScopeFull` not `FullScope`)
- [ ] Every spec finding has a corresponding task

**Spec coverage check:**
- 1.2.f (history mechanisms) → Tasks 10, 11, 12, 16
- 1.2.g (embedding conflict) → Task 1
- 1.2.k (Sandbox interface) → Task 2
- 2.2.g (double-close) → Tasks 13, 14
- 3.4 (State/Sync split) → Task 4
- 3.6 (EventMaxIterReached + maxIter raise) → Task 15
- 3.7 (tool-result paging) → Tasks 5, 6, 7, 8, 9
- 3.8 (three knobs) → Task 3 (knobs), Task 15 (maxIter portion)
- 3.9 (Compactor unification) → Tasks 11, 12, 16

All 9 findings covered.

---

## Execution notes

- Tasks 1, 2, 3, 4 in Wave 1 can run fully in parallel — different files.
- Tasks 5, 6, 7 are sequential within Track A (build the store before integrating it).
- Task 8 must come after Task 7 (uses `NewReadFullResultTool`).
- Task 9 must come after Task 8 (re-exports the runtime option).
- Tasks 10, 11, 12 are sequential within Track D (add field → use field).
- Tasks 13, 14 are sequential within Track E (locate then fix).
- Task 15 (Wave 3) should run after Tasks 8 and 12 to avoid `loop.go` conflicts.
- Tasks 16, 17, 18 are sequential within Wave 4.

**Recommended dispatch order if running serially:** 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12 → 13 → 14 → 15 → 16 → 17 → 18.

**Recommended dispatch order if parallelizing:**
- Round 1 (parallel): 1, 2, 3, 4, 5
- Round 2 (after 5): 6, 10, 13
- Round 3 (after 6, 10): 7, 11
- Round 4 (after 7, 11): 8, 12, 14
- Round 5 (after 8, 12): 9, 15
- Round 6: 16, 17
- Round 7: 18
