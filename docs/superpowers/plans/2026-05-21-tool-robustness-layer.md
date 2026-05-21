# Tool Robustness Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the three biggest Tool-System gaps in Oasis (input coercion, output schema publication, per-tool timeout+retry policy) without violating leaf-package invariants or PHILOSOPHY constraints.

**Architecture:**
- Three enrichments to the existing `Tool[In, Out]` / `AnyTool` shape, layered into the existing `Erase` adapter (`core/`) and the existing dispatch chain (`agent/`).
- All primitives (coercion, retry, schema) live in `core/` and depend only on stdlib (`encoding/json`, `bytes`, `errors`, `net`, `context`, `time`). Agent options + dispatch wrap live in `agent/`.
- Two new options: `agent.WithToolPolicy(name, ToolPolicy)` and `agent.WithToolPolicyMatch(matcher, ToolPolicy)`. ServeMux-style precedence (exact → matcher → none). Streaming tools bypass the policy wrapper unconditionally.
- One breaking contract change: `erasedTool.ExecuteRaw` now returns the original Go error from `Tool.Execute` (in addition to setting `ToolResult.Error`) so the policy wrapper can inspect typed errors. Behavior at the LLM-visible layer is unchanged because `toolResultToDispatch` already prioritizes Go error over `ToolResult.Error`.

**Tech Stack:** Go 1.24, stdlib only in `core/`. `log/slog` for the streaming-bypass-warned-once log in `agent/`. Existing `testing` package (no testify in `core/`).

**Source spec:** `docs/superpowers/specs/2026-05-21-tool-robustness-layer-design.md`

---

## File Structure

### Files created

| Path | Purpose | Approx. LOC |
|---|---|---|
| `core/coerce.go` | `coerceArgs(json.RawMessage) json.RawMessage` — null→{} and stringified-JSON unwrap. Pure function, zero alloc on happy path. | ~50 |
| `core/coerce_test.go` | Coverage of every spec test case. | ~120 |
| `core/retry.go` | `ToolPolicy` struct, `Retryable` interface, `RetryableError` wrapper, `DefaultRetryOn` predicate. | ~80 |
| `core/retry_test.go` | DefaultRetryOn matrix + RetryableError wrapping behavior. | ~120 |
| `agent/tool_policy.go` | `runWithPolicy` retry loop, `resolveToolPolicy` ServeMux-style resolver. | ~80 |
| `agent/tool_policy_test.go` | Retry-budget, timeout, backoff-cancel, precedence, streaming-bypass. | ~200 |

### Files modified

| Path | Change |
|---|---|
| `core/types.go` | `ToolDefinition.OutputSchema json.RawMessage` field; `OutSchemaProvider` interface; `ToolRegistry.IsStreamingTool(name string) bool` method. |
| `core/erase.go` | Apply `coerceArgs` before unmarshal; derive `OutputSchema` once at `Erase`/`EraseStreaming` time; honor `OutSchemaProvider`; propagate the Go error from `tool.Execute` (not the unmarshal/marshal errors). |
| `core/erase_test.go` | Output-schema derivation, override interface, coercion integration, propagated Go error. |
| `core/types_test.go` (NEW or extend `tool_registry_test.go`) | `IsStreamingTool` true/false/unknown name. |
| `agent/agent.go` | `Config.toolPolicies` (exact map) + `Config.toolPolicyMatchers` (ordered slice); `WithToolPolicy`, `WithToolPolicyMatch` options. |
| `agent/agent_test.go` (or new `agent/tool_policy_options_test.go`) | Re-register-overwrites, matcher ordering. |
| `agent/dispatch.go` | `StandardDispatchConfig.ResolvePolicy` + `IsStreamingTool` hooks; dispatch closure applies policy wrap on non-streaming path, bypasses for streaming tools, warns once. |
| `agent/llm.go` | `LLMAgent.makeDispatch` passes the policy resolver + streaming checker. |
| `oasis.go` | Re-export `ToolPolicy`, `Retryable`, `RetryableError`, `DefaultRetryOn`, `OutSchemaProvider`. (`WithToolPolicy`/`WithToolPolicyMatch` are not re-exported by name — they remain accessible via `agent.With…` consistent with existing pattern; verify by reading existing re-export lines.) |
| `CHANGELOG.md` | `[Unreleased]` entry. |

### Files audited but unchanged

- `agent/iteration.go`, `agent/loop.go` — they consume `DispatchResult`, not raw `(ToolResult, error)`. `toolResultToDispatch` already prioritizes the Go error path. No update needed.
- `mcp/tool_wrapper.go` — `toolWrapper.ExecuteRaw` already swallows Go errors into `ToolResult.Error` and returns `(result, nil)`. It is a tool-author surface, not the erased dispatch layer; the contract change does not affect it. Adopting `core.RetryableError` for transient MCP errors is left as follow-up work.

---

## Task 1: Add `ToolDefinition.OutputSchema` field and `OutSchemaProvider` interface

**Files:**
- Modify: `core/types.go:366-370` (extend `ToolDefinition` struct)
- Modify: `core/types.go` (append `OutSchemaProvider` interface near other tool interfaces)
- Test: `core/erase_test.go` (later in Task 4)

This task is additive and compile-only. It introduces the type surface that Task 4 will fill in.

- [ ] **Step 1: Extend `ToolDefinition` with `OutputSchema`**

Edit `core/types.go`, replacing the existing `ToolDefinition` block:

```go
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`            // JSON Schema for the input.
	// OutputSchema is the JSON Schema for the tool's successful result. It is
	// derived at registration time by Erase/EraseStreaming via DeriveSchema[Out].
	// Tools that need richer constraints than reflection produces may implement
	// OutSchemaProvider to override the derived schema. Provider implementations
	// decide whether to forward this field to the LLM in the tool spec.
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}
```

- [ ] **Step 2: Add the `OutSchemaProvider` opt-in interface**

In `core/types.go`, append (immediately after `ToolDefinition`):

```go
// OutSchemaProvider is the opt-in override for the reflection-based output
// schema derivation performed by Erase. Tool implementations may implement
// this to supply a custom JSON Schema (enum values, format hints, min/max)
// that reflection cannot express.
//
// When a Tool[In, Out] also implements OutSchemaProvider, Erase uses the
// override and discards the schema derived from Out.
type OutSchemaProvider interface {
	OutSchema() json.RawMessage
}
```

- [ ] **Step 3: Verify it compiles**

Run:

```bash
go build ./core/...
```

Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add core/types.go
git commit -m "feat(core): add ToolDefinition.OutputSchema + OutSchemaProvider"
```

---

## Task 2: Input coercion pipeline (`core/coerce.go`)

**Files:**
- Create: `core/coerce.go`
- Create: `core/coerce_test.go`

Pure, allocation-free on the happy path. Two information-preserving transforms:
1. `null` or empty/whitespace bytes → `{}`.
2. Stringified JSON whose value parses as an object or array → unwrap one level.

- [ ] **Step 1: Write failing tests in `core/coerce_test.go`**

```go
package core

import (
	"encoding/json"
	"testing"
)

func TestCoerceArgs_NullAndEmpty(t *testing.T) {
	cases := map[string]json.RawMessage{
		"empty bytes":    nil,
		"zero length":    json.RawMessage(""),
		"literal null":   json.RawMessage("null"),
		"padded null":    json.RawMessage("  null \n"),
		"whitespace only": json.RawMessage("   "),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := coerceArgs(in)
			if string(got) != "{}" {
				t.Errorf("coerceArgs(%q) = %q, want {}", in, got)
			}
		})
	}
}

func TestCoerceArgs_StringifiedObject(t *testing.T) {
	in := json.RawMessage(`"{\"x\":1}"`)
	got := coerceArgs(in)
	if string(got) != `{"x":1}` {
		t.Errorf("coerceArgs(%q) = %q, want {\"x\":1}", in, got)
	}
}

func TestCoerceArgs_StringifiedArray(t *testing.T) {
	in := json.RawMessage(`"[1,2,3]"`)
	got := coerceArgs(in)
	if string(got) != `[1,2,3]` {
		t.Errorf("coerceArgs(%q) = %q, want [1,2,3]", in, got)
	}
}

func TestCoerceArgs_StringifiedWithWhitespace(t *testing.T) {
	in := json.RawMessage(`"  {\"x\":1}  "`)
	got := coerceArgs(in)
	if string(got) != `{"x":1}` {
		t.Errorf("coerceArgs(%q) = %q, want {\"x\":1}", in, got)
	}
}

func TestCoerceArgs_PlainStringPassesThrough(t *testing.T) {
	in := json.RawMessage(`"hello"`)
	got := coerceArgs(in)
	if string(got) != `"hello"` {
		t.Errorf("coerceArgs(%q) = %q, want unchanged", in, got)
	}
}

func TestCoerceArgs_AlreadyObjectPassesThrough(t *testing.T) {
	in := json.RawMessage(`{"x":1}`)
	got := coerceArgs(in)
	if string(got) != `{"x":1}` {
		t.Errorf("coerceArgs(%q) = %q, want unchanged", in, got)
	}
}

func TestCoerceArgs_MalformedPassesThrough(t *testing.T) {
	in := json.RawMessage(`{"x":`) // broken; let unmarshal report
	got := coerceArgs(in)
	if string(got) != `{"x":` {
		t.Errorf("coerceArgs(%q) = %q, want unchanged", in, got)
	}
}

func TestCoerceArgs_StringifiedInvalidJSONPassesThrough(t *testing.T) {
	// A quoted string whose contents look like JSON but aren't valid →
	// pass through; unmarshal reports the real error.
	in := json.RawMessage(`"{not json}"`)
	got := coerceArgs(in)
	if string(got) != `"{not json}"` {
		t.Errorf("coerceArgs(%q) = %q, want unchanged", in, got)
	}
}
```

- [ ] **Step 2: Run tests — should fail with "undefined: coerceArgs"**

```bash
go test ./core/ -run TestCoerceArgs -v
```

Expected: compile error `undefined: coerceArgs`.

- [ ] **Step 3: Implement `core/coerce.go`**

```go
package core

import (
	"bytes"
	"encoding/json"
)

// coerceArgs applies structural, information-preserving transforms before
// json.Unmarshal in the erased adapters. Two transforms:
//
//  1. null, empty bytes, or whitespace-only input → {}
//     (LLMs occasionally send literal "null" or an empty body for an absent
//     object argument.)
//  2. A single JSON string whose value parses as an object or array →
//     unwrap one level. (LLMs occasionally send stringified JSON for tools
//     with a JSON-shaped argument.)
//
// All other inputs pass through unchanged so the existing json.Unmarshal
// failure path reports the real problem. Coercion never errors. On the
// happy path (already an object or array) this function performs zero heap
// allocations: it only takes sub-slices of the input.
func coerceArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	// bytes.TrimSpace returns a sub-slice; no heap alloc.
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}")
	}
	// Stringified JSON object/array unwrap. Only triggered when the input
	// starts with a quote — preserves zero-alloc happy path for objects
	// and arrays.
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			inner := bytes.TrimSpace([]byte(s))
			if len(inner) > 0 && (inner[0] == '{' || inner[0] == '[') && json.Valid(inner) {
				return json.RawMessage(inner)
			}
		}
	}
	return raw
}
```

- [ ] **Step 4: Run tests — should pass**

```bash
go test ./core/ -run TestCoerceArgs -v
```

Expected: all 8 cases PASS.

- [ ] **Step 5: Commit**

```bash
git add core/coerce.go core/coerce_test.go
git commit -m "feat(core): add input-arg coercion pipeline"
```

---

## Task 3: Wire coercion into `Erase` and `EraseStreaming`

**Files:**
- Modify: `core/erase.go:33-49` (`erasedTool.ExecuteRaw`)
- Modify: `core/erase.go:74-108` (`erasedStreamingTool.ExecuteRaw` and `ExecuteStream`)
- Modify: `core/erase_test.go` (append integration tests)

- [ ] **Step 1: Add failing tests in `core/erase_test.go`** (append to existing file)

```go
func TestErase_CoercesNullArgs(t *testing.T) {
	tool := &echoTool{}
	erased := Erase[echoInput, echoOutput](tool)
	res, err := erased.ExecuteRaw(context.Background(), json.RawMessage("null"))
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if res.Error != "" {
		t.Fatalf("expected no ToolResult.Error, got %q", res.Error)
	}
	// echoTool with zero In ("") should still echo the empty message back.
	if !bytes.Contains([]byte(res.Content), []byte(`"echoed":""`)) {
		t.Errorf("expected echoed empty string, got %q", res.Content)
	}
}

func TestErase_CoercesStringifiedJSONArgs(t *testing.T) {
	tool := &echoTool{}
	erased := Erase[echoInput, echoOutput](tool)
	in := json.RawMessage(`"{\"message\":\"hi\"}"`)
	res, err := erased.ExecuteRaw(context.Background(), in)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected ToolResult.Error: %q", res.Error)
	}
	if !bytes.Contains([]byte(res.Content), []byte(`"echoed":"hi"`)) {
		t.Errorf("expected echoed hi, got %q", res.Content)
	}
}
```

Add `"bytes"` to the existing test file's import block if not already there.

- [ ] **Step 2: Run tests to confirm failure**

```bash
go test ./core/ -run "TestErase_Coerces" -v
```

Expected: tests fail (current code passes `null` to `json.Unmarshal` → error).

- [ ] **Step 3: Wire `coerceArgs` into all three Erase entry points**

In `core/erase.go`, modify each of the three `ExecuteRaw`/`ExecuteStream` methods. Change the preamble in each from:

```go
	var in In
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
	}
```

to:

```go
	var in In
	args = coerceArgs(args)
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
```

This applies in all three methods: `erasedTool.ExecuteRaw` (line 33), `erasedStreamingTool.ExecuteRaw` (line 74), `erasedStreamingTool.ExecuteStream` (line 92). After coercion `args` is always non-empty (`coerceArgs` returns `{}` for empty/null), so the `len(args) > 0` guard is no longer needed.

- [ ] **Step 4: Run tests to confirm pass**

```bash
go test ./core/ -v
```

Expected: all `TestErase_*` and `TestCoerceArgs_*` PASS. No regressions.

- [ ] **Step 5: Commit**

```bash
git add core/erase.go core/erase_test.go
git commit -m "feat(core): apply input coercion in Erase adapters"
```

---

## Task 4: Auto-derive `OutputSchema` and honor `OutSchemaProvider`

**Files:**
- Modify: `core/erase.go:12-23` (`Erase` constructor)
- Modify: `core/erase.go:53-64` (`EraseStreaming` constructor)
- Modify: `core/erase_test.go` (append)

Schema publication happens once at registration, not per dispatch.

- [ ] **Step 1: Add failing tests in `core/erase_test.go`**

```go
// outputProviderTool overrides the derived output schema.
type outputProviderTool struct{}

func (o *outputProviderTool) Definition() ToolMeta {
	return ToolMeta{Name: "override", Description: "x"}
}
func (o *outputProviderTool) Execute(_ context.Context, _ echoInput) (echoOutput, error) {
	return echoOutput{Echoed: "ok"}, nil
}
func (o *outputProviderTool) OutSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"custom":{"type":"string"}}}`)
}

func TestErase_DerivesOutputSchemaForStruct(t *testing.T) {
	erased := Erase[echoInput, echoOutput](&echoTool{})
	def := erased.Definition()
	if len(def.OutputSchema) == 0 {
		t.Fatal("expected non-empty OutputSchema")
	}
	// Must match DeriveSchema[echoOutput]() exactly.
	want := DeriveSchema[echoOutput]()
	if !bytes.Equal(def.OutputSchema, want) {
		t.Errorf("OutputSchema = %q, want %q", def.OutputSchema, want)
	}
}

func TestErase_OutputSchemaEmptyForAny(t *testing.T) {
	type anyOutTool struct{}
	// Build a tool whose Out is `any` via an inline wrapper:
	tool := &anyOutToolImpl{}
	erased := Erase[echoInput, any](tool)
	def := erased.Definition()
	// DeriveSchema[any]() returns {}, so OutputSchema should be {} (or empty omitted).
	got := string(def.OutputSchema)
	if got != "{}" && got != "" {
		t.Errorf("OutputSchema for any = %q, want {} or empty", got)
	}
}

type anyOutToolImpl struct{}

func (a *anyOutToolImpl) Definition() ToolMeta { return ToolMeta{Name: "anyout"} }
func (a *anyOutToolImpl) Execute(_ context.Context, _ echoInput) (any, error) {
	return map[string]string{"k": "v"}, nil
}

func TestErase_HonorsOutSchemaProvider(t *testing.T) {
	erased := Erase[echoInput, echoOutput](&outputProviderTool{})
	def := erased.Definition()
	want := `{"type":"object","properties":{"custom":{"type":"string"}}}`
	if string(def.OutputSchema) != want {
		t.Errorf("OutputSchema = %q, want %q", def.OutputSchema, want)
	}
	// Verify the derived schema (which would NOT contain "custom") was discarded.
	derived := DeriveSchema[echoOutput]()
	if bytes.Equal(def.OutputSchema, derived) {
		t.Errorf("override not applied; got derived schema")
	}
}
```

- [ ] **Step 2: Run tests to confirm failure**

```bash
go test ./core/ -run "TestErase_(Derives|HonorsOut|OutputSchemaEmpty)" -v
```

Expected: FAIL — `def.OutputSchema` is empty / not set.

- [ ] **Step 3: Update `Erase` constructor**

Modify `Erase` in `core/erase.go`:

```go
func Erase[In, Out any](t Tool[In, Out]) AnyTool {
	meta := t.Definition()
	inSchema := DeriveSchema[In]()
	outSchema := deriveOutSchema[Out](t)
	return &erasedTool[In, Out]{
		tool: t,
		def: ToolDefinition{
			Name:         meta.Name,
			Description:  meta.Description,
			Parameters:   inSchema,
			OutputSchema: outSchema,
		},
	}
}
```

Modify `EraseStreaming` in the same file analogously:

```go
func EraseStreaming[In, Out any](t StreamingTool[In, Out]) StreamingAnyTool {
	meta := t.Definition()
	inSchema := DeriveSchema[In]()
	outSchema := deriveOutSchema[Out](t)
	return &erasedStreamingTool[In, Out]{
		tool: t,
		def: ToolDefinition{
			Name:         meta.Name,
			Description:  meta.Description,
			Parameters:   inSchema,
			OutputSchema: outSchema,
		},
	}
}
```

Add the `deriveOutSchema` helper at the bottom of `core/erase.go`:

```go
// deriveOutSchema returns the OutputSchema to publish for an erased tool.
// If t implements OutSchemaProvider, its override is used; otherwise the
// schema for Out is derived by reflection. The override is read via a type
// assertion on `any(t)`, mirroring the SchemaProvider pattern in DeriveSchema.
func deriveOutSchema[Out any](t any) json.RawMessage {
	if p, ok := t.(OutSchemaProvider); ok {
		return p.OutSchema()
	}
	return DeriveSchema[Out]()
}
```

- [ ] **Step 4: Run all `core/` tests**

```bash
go test ./core/ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add core/erase.go core/erase_test.go
git commit -m "feat(core): derive OutputSchema + honor OutSchemaProvider in Erase"
```

---

## Task 5: Propagate `Tool.Execute` Go error from erased adapters

**Files:**
- Modify: `core/erase.go` (the three Execute-call sites)
- Modify: `core/erase_test.go` (append propagation test)

This is the single breaking contract change. The unmarshal-error and marshal-error paths are NOT changed (they remain `(result, nil)`) — only the `tool.Execute(...)` error path propagates the Go error.

- [ ] **Step 1: Add failing test**

```go
func TestErase_PropagatesExecuteError(t *testing.T) {
	tool := &echoTool{failOnExecute: true} // existing test helper, returns errors.New(...)
	erased := Erase[echoInput, echoOutput](tool)
	res, err := erased.ExecuteRaw(context.Background(), json.RawMessage(`{"message":"hi"}`))
	if err == nil {
		t.Fatal("expected non-nil Go error, got nil")
	}
	if res.Error == "" {
		t.Fatal("expected ToolResult.Error to also be populated")
	}
}

func TestErase_UnmarshalErrorReturnsNilGoError(t *testing.T) {
	erased := Erase[echoInput, echoOutput](&echoTool{})
	// {"message": 42} — int into string field → unmarshal fails.
	res, err := erased.ExecuteRaw(context.Background(), json.RawMessage(`{"message":42}`))
	if err != nil {
		t.Fatalf("unmarshal errors must stay non-retryable (nil Go error), got %v", err)
	}
	if !strings.HasPrefix(res.Error, "invalid args:") {
		t.Errorf("expected invalid args prefix, got %q", res.Error)
	}
}
```

Verify `echoTool.failOnExecute` returns a non-nil Go error from its `Execute` method — read the existing test file to confirm. If it does not, add the branch:

```go
func (e *echoTool) Execute(_ context.Context, in echoInput) (echoOutput, error) {
	if e.failOnExecute {
		return echoOutput{}, errors.New("boom")
	}
	return echoOutput{Echoed: in.Message}, nil
}
```

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./core/ -run "TestErase_(PropagatesExecuteError|UnmarshalErrorReturnsNilGoError)" -v
```

Expected: `TestErase_PropagatesExecuteError` FAILS (current code returns nil Go error); `TestErase_UnmarshalErrorReturnsNilGoError` may already PASS.

- [ ] **Step 3: Modify the three Execute call sites**

In `core/erase.go`, change each occurrence of:

```go
	out, err := e.tool.Execute(ctx, in)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
```

to:

```go
	out, err := e.tool.Execute(ctx, in)
	if err != nil {
		// Propagate the typed Go error so the dispatch policy wrapper can
		// inspect it (Retryable, net.Error.Timeout(), context.DeadlineExceeded).
		// ToolResult.Error remains populated for the LLM-visible string.
		return ToolResult{Error: err.Error()}, err
	}
```

This applies in `erasedTool.ExecuteRaw` (line ~40), `erasedStreamingTool.ExecuteRaw` (line ~81), and the `ExecuteStream` call (`out, err := e.tool.ExecuteStream(ctx, in, ch)` at line ~99). All three call sites get the same change.

Do **NOT** change the unmarshal-error or marshal-error returns — these stay `(result, nil)` because they are not retryable and not interesting to the policy wrapper.

- [ ] **Step 4: Run all core tests**

```bash
go test ./core/ -v
```

Expected: all PASS.

- [ ] **Step 5: Run the full test suite to detect ripple effects**

```bash
go test ./... 2>&1 | tail -30
```

Expected: all packages PASS. If `agent/iteration.go` or `agent/loop.go` fails, audit those paths — but `toolResultToDispatch` (`agent/dispatch.go:50`) already prioritizes the Go error path, so no regression is expected.

- [ ] **Step 6: Commit**

```bash
git add core/erase.go core/erase_test.go
git commit -m "feat(core)!: propagate Tool.Execute Go error from erased adapters"
```

---

## Task 6: Add `core/retry.go` — `ToolPolicy`, `Retryable`, `RetryableError`, `DefaultRetryOn`

**Files:**
- Create: `core/retry.go`
- Create: `core/retry_test.go`

`core` cannot import anything from `oasis/...` (depguard rule). This file uses only `errors`, `net`, `time`, `context`.

- [ ] **Step 1: Write failing tests in `core/retry_test.go`**

```go
package core

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestRetryableError_NilPassThrough(t *testing.T) {
	if got := RetryableError(nil); got != nil {
		t.Fatalf("RetryableError(nil) = %v, want nil", got)
	}
}

func TestRetryableError_WrapsAndUnwraps(t *testing.T) {
	wrapped := RetryableError(io.EOF)
	if !errors.Is(wrapped, io.EOF) {
		t.Errorf("errors.Is(wrapped, io.EOF) = false, want true")
	}
	var r Retryable
	if !errors.As(wrapped, &r) {
		t.Fatalf("errors.As(wrapped, &Retryable) = false, want true")
	}
	if !r.Retryable() {
		t.Errorf("r.Retryable() = false, want true")
	}
}

func TestDefaultRetryOn_DeadlineExceeded(t *testing.T) {
	if !DefaultRetryOn(context.DeadlineExceeded) {
		t.Errorf("DefaultRetryOn(DeadlineExceeded) = false, want true")
	}
}

func TestDefaultRetryOn_NetTimeout(t *testing.T) {
	e := &net.DNSError{IsTimeout: true}
	if !DefaultRetryOn(e) {
		t.Errorf("DefaultRetryOn(net.DNSError{IsTimeout:true}) = false, want true")
	}
}

func TestDefaultRetryOn_PlainErrorRejected(t *testing.T) {
	if DefaultRetryOn(errors.New("plain")) {
		t.Errorf("DefaultRetryOn(plain) = true, want false")
	}
}

func TestDefaultRetryOn_RetryableErrorAccepted(t *testing.T) {
	if !DefaultRetryOn(RetryableError(errors.New("upstream 503"))) {
		t.Errorf("DefaultRetryOn(RetryableError(...)) = false, want true")
	}
}

func TestDefaultRetryOn_Nil(t *testing.T) {
	if DefaultRetryOn(nil) {
		t.Errorf("DefaultRetryOn(nil) = true, want false")
	}
}

func TestToolPolicy_BackoffFormula(t *testing.T) {
	cases := []struct {
		attempt int
		base    time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{0, 100 * time.Millisecond, 0, 100 * time.Millisecond},
		{1, 100 * time.Millisecond, 0, 200 * time.Millisecond},
		{2, 100 * time.Millisecond, 0, 400 * time.Millisecond},
		{3, 100 * time.Millisecond, 300 * time.Millisecond, 300 * time.Millisecond}, // capped
		{10, 100 * time.Millisecond, 1 * time.Second, 1 * time.Second},               // capped
		{0, 0, 0, 0},                                                                 // zero base → zero
	}
	for _, c := range cases {
		got := BackoffDelay(c.base, c.max, c.attempt)
		if got != c.want {
			t.Errorf("BackoffDelay(%v, %v, %d) = %v, want %v", c.base, c.max, c.attempt, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to confirm failure**

```bash
go test ./core/ -run "TestRetryable|TestDefaultRetryOn|TestToolPolicy_Backoff" -v
```

Expected: compile error `undefined: RetryableError, Retryable, DefaultRetryOn, ToolPolicy, BackoffDelay`.

- [ ] **Step 3: Implement `core/retry.go`**

```go
package core

import (
	"context"
	"errors"
	"net"
	"time"
)

// ToolPolicy describes the per-tool timeout and retry policy applied by the
// agent's dispatch wrapper. Zero value = no timeout, no retries (current
// behavior). Streaming tools (those implementing StreamingAnyTool) bypass
// this policy entirely — retrying a partially-streamed call would duplicate
// events at the consumer.
type ToolPolicy struct {
	// Timeout is the per-attempt context deadline. Zero means no timeout
	// (the parent context still applies).
	Timeout time.Duration
	// Retries is the number of additional attempts after the first. Zero
	// means a single attempt, identical to current behavior.
	Retries int
	// RetryDelay is the base backoff between attempts. The actual delay
	// before attempt N+1 is RetryDelay << N, capped by MaxRetryDelay.
	RetryDelay time.Duration
	// MaxRetryDelay caps the exponential backoff. Zero means no cap.
	MaxRetryDelay time.Duration
	// RetryOn decides whether a given error is retryable. nil → DefaultRetryOn.
	RetryOn func(error) bool
}

// Retryable is the opt-in convention tool authors use to mark an error as
// retryable. DefaultRetryOn honors this mark via errors.As, and any
// user-supplied RetryOn predicate may do the same.
type Retryable interface {
	Retryable() bool
}

// retryableErr wraps an underlying error and reports Retryable() == true.
type retryableErr struct{ err error }

func (r *retryableErr) Error() string   { return r.err.Error() }
func (r *retryableErr) Unwrap() error   { return r.err }
func (r *retryableErr) Retryable() bool { return true }

// RetryableError marks err as retryable. It is the recommended way for tool
// implementations to signal that a transient failure (HTTP 429, 5xx, etc.)
// is worth a retry attempt. Returns nil when err is nil.
//
//	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
//	    return zero, core.RetryableError(fmt.Errorf("upstream: HTTP %d", resp.StatusCode))
//	}
//	return zero, fmt.Errorf("upstream: HTTP %d", resp.StatusCode) // not retryable
func RetryableError(err error) error {
	if err == nil {
		return nil
	}
	return &retryableErr{err: err}
}

// DefaultRetryOn is the predicate used when ToolPolicy.RetryOn is nil. It
// returns true iff:
//
//  1. errors.Is(err, context.DeadlineExceeded) — our own timeout fired.
//  2. err is a net.Error with Timeout() == true — TCP-layer timeout.
//  3. err matches the Retryable interface via errors.As — opt-in mark.
//
// DefaultRetryOn is exported so user-supplied predicates can compose:
//
//	RetryOn: func(err error) bool {
//	    return core.DefaultRetryOn(err) || errors.Is(err, myExtraSentinel)
//	}
func DefaultRetryOn(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var r Retryable
	if errors.As(err, &r) && r.Retryable() {
		return true
	}
	return false
}

// BackoffDelay computes the backoff for retry attempt N (0-indexed) given
// the base RetryDelay and optional MaxRetryDelay cap. delay = base << attempt,
// then capped at max if max > 0. Exported for test parity and so user code
// can reproduce the framework's backoff schedule.
func BackoffDelay(base, max time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	// Guard against shift overflow: cap attempt shift at 30 (~17 minutes
	// at base = 1ms; the MaxRetryDelay cap below will dominate well before).
	shift := attempt
	if shift > 30 {
		shift = 30
	}
	d := base << shift
	if max > 0 && d > max {
		return max
	}
	return d
}
```

- [ ] **Step 4: Run tests to confirm pass**

```bash
go test ./core/ -run "TestRetryable|TestDefaultRetryOn|TestToolPolicy_Backoff" -v
```

Expected: all PASS.

- [ ] **Step 5: Verify depguard compliance**

```bash
golangci-lint run ./core/...
```

Expected: no violations (only stdlib imports used).

- [ ] **Step 6: Commit**

```bash
git add core/retry.go core/retry_test.go
git commit -m "feat(core): add ToolPolicy, RetryableError, DefaultRetryOn"
```

---

## Task 7: Add `ToolRegistry.IsStreamingTool` lookup

**Files:**
- Modify: `core/types.go` (append method near `Execute`/`ExecuteStream`)
- Modify: `core/tool_registry_test.go` (or extend existing file with the same test name pattern)

The agent's dispatch closure needs to know whether a tool by name is a `StreamingAnyTool` so it can bypass the policy wrapper.

- [ ] **Step 1: Add a failing test in `core/tool_registry_test.go`** (append)

```go
// streamingAnyToolStub is a minimal StreamingAnyTool used for registry tests.
type streamingAnyToolStub struct{ name string }

func (s *streamingAnyToolStub) Name() string              { return s.name }
func (s *streamingAnyToolStub) Definition() ToolDefinition { return ToolDefinition{Name: s.name} }
func (s *streamingAnyToolStub) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	return TextResult("ok"), nil
}
func (s *streamingAnyToolStub) ExecuteStream(_ context.Context, _ json.RawMessage, _ chan<- StreamEvent) (ToolResult, error) {
	return TextResult("ok"), nil
}

func TestToolRegistry_IsStreamingTool(t *testing.T) {
	reg := NewToolRegistry()
	reg.Add(&stubAnyTool{name: "plain"})
	reg.Add(&streamingAnyToolStub{name: "stream"})

	if reg.IsStreamingTool("stream") != true {
		t.Errorf("IsStreamingTool(stream) = false, want true")
	}
	if reg.IsStreamingTool("plain") != false {
		t.Errorf("IsStreamingTool(plain) = true, want false")
	}
	if reg.IsStreamingTool("missing") != false {
		t.Errorf("IsStreamingTool(missing) = true, want false")
	}
}
```

If `stubAnyTool` is not visible from `tool_registry_test.go`, copy its declaration from `erase_test.go` or reference it; both files are in the same `core` test package so they share symbols.

- [ ] **Step 2: Run test to confirm failure**

```bash
go test ./core/ -run TestToolRegistry_IsStreamingTool -v
```

Expected: compile error `reg.IsStreamingTool undefined`.

- [ ] **Step 3: Implement the method**

In `core/types.go`, append immediately after `ToolRegistry.ExecuteStream` (around line 216):

```go
// IsStreamingTool reports whether the tool registered under name implements
// StreamingAnyTool. Returns false for unknown names. Used by the agent
// dispatch layer to decide whether to bypass the per-tool policy wrapper.
func (r *ToolRegistry) IsStreamingTool(name string) bool {
	t, ok := r.index[name]
	if !ok {
		return false
	}
	_, ok = t.(StreamingAnyTool)
	return ok
}
```

- [ ] **Step 4: Run test to confirm pass**

```bash
go test ./core/ -run TestToolRegistry_IsStreamingTool -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/types.go core/tool_registry_test.go
git commit -m "feat(core): add ToolRegistry.IsStreamingTool"
```

---

## Task 8: Add `runWithPolicy` and unit tests (`agent/tool_policy.go`)

**Files:**
- Create: `agent/tool_policy.go`
- Create: `agent/tool_policy_test.go`

This task implements only the retry-loop primitive. Wiring it into dispatch comes in Task 10.

- [ ] **Step 1: Write failing tests in `agent/tool_policy_test.go`**

```go
package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

func TestRunWithPolicy_SuccessFirstAttempt(t *testing.T) {
	calls := 0
	res, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 3}, func(_ context.Context) (ToolResult, error) {
		calls++
		return ToolResult{Content: []byte(`"ok"`)}, nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if string(res.Content) != `"ok"` {
		t.Errorf("Content = %q, want \"ok\"", res.Content)
	}
}

func TestRunWithPolicy_RetriesUntilSuccess(t *testing.T) {
	var calls int32
	res, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 3, RetryDelay: 1 * time.Millisecond},
		func(_ context.Context) (ToolResult, error) {
			n := atomic.AddInt32(&calls, 1)
			if n < 3 {
				return ToolResult{}, core.RetryableError(errors.New("transient"))
			}
			return ToolResult{Content: []byte(`"finally"`)}, nil
		})
	if err != nil {
		t.Fatalf("err = %v, want nil after retries", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if string(res.Content) != `"finally"` {
		t.Errorf("Content = %q", res.Content)
	}
}

func TestRunWithPolicy_NonRetryableErrorReturnsImmediately(t *testing.T) {
	var calls int32
	plain := errors.New("not retryable")
	_, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 5, RetryDelay: 1 * time.Millisecond},
		func(_ context.Context) (ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return ToolResult{}, plain
		})
	if !errors.Is(err, plain) {
		t.Errorf("err = %v, want plain error", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retries on non-retryable)", calls)
	}
}

func TestRunWithPolicy_ExhaustsRetries(t *testing.T) {
	var calls int32
	_, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 2, RetryDelay: 1 * time.Millisecond},
		func(_ context.Context) (ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return ToolResult{}, core.RetryableError(errors.New("always fails"))
		})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != 3 { // 1 + 2 retries
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRunWithPolicy_TimeoutFires(t *testing.T) {
	var calls int32
	_, err := runWithPolicy(context.Background(), core.ToolPolicy{Timeout: 20 * time.Millisecond, Retries: 1, RetryDelay: 1 * time.Millisecond},
		func(ctx context.Context) (ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			select {
			case <-time.After(200 * time.Millisecond):
				return ToolResult{}, nil
			case <-ctx.Done():
				return ToolResult{}, ctx.Err()
			}
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if calls != 2 { // 1 + 1 retry on DeadlineExceeded
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestRunWithPolicy_ParentCancelAbortsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := runWithPolicy(ctx, core.ToolPolicy{Retries: 5, RetryDelay: 500 * time.Millisecond},
		func(_ context.Context) (ToolResult, error) {
			return ToolResult{}, core.RetryableError(errors.New("retry me"))
		})
	dur := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want Canceled", err)
	}
	if dur > 100*time.Millisecond {
		t.Errorf("loop did not exit promptly on cancel; took %v", dur)
	}
}

func TestRunWithPolicy_ZeroPolicyIsPassthrough(t *testing.T) {
	var calls int32
	plain := errors.New("plain")
	_, err := runWithPolicy(context.Background(), core.ToolPolicy{},
		func(_ context.Context) (ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return ToolResult{}, plain
		})
	if !errors.Is(err, plain) {
		t.Errorf("err = %v, want plain", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}
```

- [ ] **Step 2: Run tests to confirm failure**

```bash
go test ./agent/ -run TestRunWithPolicy -v
```

Expected: compile error `undefined: runWithPolicy`.

- [ ] **Step 3: Implement `agent/tool_policy.go`**

```go
package agent

import (
	"context"
	"time"

	"github.com/nevindra/oasis/core"
)

// runWithPolicy executes fn under the given ToolPolicy. It applies the
// per-attempt Timeout (if non-zero), the Retries budget, and the exponential
// backoff. The backoff sleep aborts immediately when the parent context is
// cancelled. The retry predicate defaults to core.DefaultRetryOn.
//
// Return contract:
//   - Success: (result, nil).
//   - All retries exhaust on retryable error: (result, lastErr) — the caller
//     (DispatchTool) materializes the LLM-visible result via toolResultToDispatch.
//   - Non-retryable error: (result, err) on first attempt; no retries.
//   - Parent ctx cancelled mid-backoff: (zeroResult, ctx.Err()).
func runWithPolicy(parent context.Context, policy core.ToolPolicy, fn func(context.Context) (ToolResult, error)) (ToolResult, error) {
	retryOn := policy.RetryOn
	if retryOn == nil {
		retryOn = core.DefaultRetryOn
	}

	var (
		result  ToolResult
		lastErr error
	)
	for attempt := 0; attempt <= policy.Retries; attempt++ {
		// Respect parent cancellation before issuing the next attempt.
		if err := parent.Err(); err != nil {
			return ToolResult{}, err
		}

		attemptCtx := parent
		var cancel context.CancelFunc
		if policy.Timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(parent, policy.Timeout)
		}
		result, lastErr = fn(attemptCtx)
		if cancel != nil {
			cancel()
		}

		if lastErr == nil {
			return result, nil
		}
		if attempt == policy.Retries || !retryOn(lastErr) {
			return result, lastErr
		}

		delay := core.BackoffDelay(policy.RetryDelay, policy.MaxRetryDelay, attempt)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-parent.Done():
				timer.Stop()
				return ToolResult{}, parent.Err()
			}
		}
	}
	return result, lastErr
}
```

Note: `ToolResult` is already an alias in the `agent` package (verify by `grep -n "ToolResult = " agent/`). If it is not, replace `ToolResult` with `core.ToolResult` here.

- [ ] **Step 4: Run tests to confirm pass**

```bash
go test ./agent/ -run TestRunWithPolicy -v
```

Expected: all 7 cases PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/tool_policy.go agent/tool_policy_test.go
git commit -m "feat(agent): add runWithPolicy retry/timeout loop"
```

---

## Task 9: Add policy resolver, `WithToolPolicy`, `WithToolPolicyMatch` options

**Files:**
- Modify: `agent/agent.go` (Config struct + With options)
- Modify: `agent/tool_policy.go` (add `resolveToolPolicy` helper)
- Create: `agent/tool_policy_options_test.go`

ServeMux-style precedence: exact name beats matchers; matchers scanned in registration order.

- [ ] **Step 1: Write failing tests in `agent/tool_policy_options_test.go`**

```go
package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

func TestWithToolPolicy_ExactName(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolPolicy("foo", core.ToolPolicy{Timeout: 5 * time.Second}),
	})
	p, ok := cfg.resolveToolPolicy("foo")
	if !ok || p.Timeout != 5*time.Second {
		t.Errorf("resolveToolPolicy(foo) = (%v, %v), want (5s, true)", p, ok)
	}
}

func TestWithToolPolicy_ExactOverwrites(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolPolicy("foo", core.ToolPolicy{Timeout: 1 * time.Second}),
		WithToolPolicy("foo", core.ToolPolicy{Timeout: 9 * time.Second}),
	})
	p, _ := cfg.resolveToolPolicy("foo")
	if p.Timeout != 9*time.Second {
		t.Errorf("Timeout = %v, want 9s (last-wins)", p.Timeout)
	}
}

func TestWithToolPolicyMatch_Ordering(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolPolicyMatch(func(n string) bool { return strings.HasPrefix(n, "mcp__") }, core.ToolPolicy{Timeout: 1 * time.Second}),
		WithToolPolicyMatch(func(n string) bool { return strings.HasPrefix(n, "mcp__github") }, core.ToolPolicy{Timeout: 2 * time.Second}),
	})
	// First registered match wins for mcp__github__issues.
	p, _ := cfg.resolveToolPolicy("mcp__github__issues")
	if p.Timeout != 1*time.Second {
		t.Errorf("Timeout = %v, want 1s (first-match-wins)", p.Timeout)
	}
}

func TestResolvePolicy_ExactBeatsMatcher(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolPolicyMatch(func(n string) bool { return true }, core.ToolPolicy{Timeout: 1 * time.Second}),
		WithToolPolicy("special", core.ToolPolicy{Timeout: 7 * time.Second}),
	})
	p, _ := cfg.resolveToolPolicy("special")
	if p.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v, want 7s (exact beats matcher)", p.Timeout)
	}
}

func TestResolvePolicy_Unknown(t *testing.T) {
	cfg := BuildConfig(nil)
	if _, ok := cfg.resolveToolPolicy("nope"); ok {
		t.Error("resolveToolPolicy(nope) = ok=true, want false")
	}
}
```

- [ ] **Step 2: Run tests to confirm failure**

```bash
go test ./agent/ -run "Test(WithToolPolicy|ResolvePolicy)" -v
```

Expected: compile errors `undefined: WithToolPolicy, WithToolPolicyMatch, resolveToolPolicy`.

- [ ] **Step 3: Add Config fields and options in `agent/agent.go`**

Add to the `Config` struct (immediately after the `maxToolResultLen` field, around line 75):

```go
	// Per-tool retry/timeout policy. toolPolicies are exact-name entries
	// (ServeMux-style; later registrations overwrite). toolPolicyMatchers
	// is an ordered list scanned in registration order; first matcher
	// whose predicate returns true wins. Exact matches always beat
	// matchers (mirrors net/http.ServeMux).
	toolPolicies        map[string]core.ToolPolicy
	toolPolicyMatchers  []toolPolicyMatcher
```

Define the matcher entry near the top of the file (after the `ToolsFunc` type alias around line 110):

```go
// toolPolicyMatcher pairs a name predicate with a policy for use by
// WithToolPolicyMatch.
type toolPolicyMatcher struct {
	match  func(name string) bool
	policy core.ToolPolicy
}
```

Add the options near `WithMaxParallelDispatch` (around line 435):

```go
// WithToolPolicy attaches a per-tool timeout and retry policy to the tool
// registered under the exact name. Re-registering the same name overwrites
// the prior entry (last-call-wins). Exact names take precedence over any
// matcher registered via WithToolPolicyMatch. Streaming tools (those
// implementing core.StreamingAnyTool) silently bypass policy wrapping.
func WithToolPolicy(name string, p core.ToolPolicy) AgentOption {
	return func(c *Config) {
		if c.toolPolicies == nil {
			c.toolPolicies = map[string]core.ToolPolicy{}
		}
		c.toolPolicies[name] = p
	}
}

// WithToolPolicyMatch attaches a policy to every tool whose name satisfies
// the matcher predicate. Matchers are scanned in registration order;
// the first matcher whose predicate returns true wins. Useful for applying
// a single policy to MCP tool families (e.g. names prefixed with mcp__).
// Exact-name entries from WithToolPolicy always take precedence.
func WithToolPolicyMatch(matcher func(name string) bool, p core.ToolPolicy) AgentOption {
	return func(c *Config) {
		c.toolPolicyMatchers = append(c.toolPolicyMatchers, toolPolicyMatcher{match: matcher, policy: p})
	}
}
```

- [ ] **Step 4: Add `resolveToolPolicy` to `agent/tool_policy.go`**

Append:

```go
// resolveToolPolicy implements ServeMux-style policy lookup: exact-name
// entry first, then matchers in registration order. Returns the policy
// and true if any rule matched.
func (c *Config) resolveToolPolicy(name string) (core.ToolPolicy, bool) {
	if c == nil {
		return core.ToolPolicy{}, false
	}
	if p, ok := c.toolPolicies[name]; ok {
		return p, true
	}
	for _, m := range c.toolPolicyMatchers {
		if m.match(name) {
			return m.policy, true
		}
	}
	return core.ToolPolicy{}, false
}
```

- [ ] **Step 5: Run tests to confirm pass**

```bash
go test ./agent/ -run "Test(WithToolPolicy|ResolvePolicy)" -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/agent.go agent/tool_policy.go agent/tool_policy_options_test.go
git commit -m "feat(agent): WithToolPolicy / WithToolPolicyMatch options"
```

---

## Task 10: Wire policy + streaming bypass into `NewStandardDispatch`

**Files:**
- Modify: `agent/dispatch.go` (`StandardDispatchConfig` and `NewStandardDispatch`)
- Modify: `agent/tool_policy_test.go` (append integration test against the dispatch closure)

The dispatch closure (built by `NewStandardDispatch`) is the chokepoint between the agent loop and `executeTool`. This is where we apply policy and bypass for streaming tools.

- [ ] **Step 1: Add a failing integration test in `agent/tool_policy_test.go`**

```go
// fakeTool implements ToolExecFunc-equivalent dispatch via a closure.
type policyTestExec struct {
	calls   int32
	failN   int   // fail first N attempts with RetryableError, then succeed
	errFn   func(int32) error
	result  ToolResult
	stream  bool // when true, IsStreamingTool returns true
}

func (p *policyTestExec) exec(_ context.Context, _ string, _ json.RawMessage) (ToolResult, error) {
	n := atomic.AddInt32(&p.calls, 1)
	if p.errFn != nil {
		if err := p.errFn(n); err != nil {
			return ToolResult{}, err
		}
	}
	return p.result, nil
}

func (p *policyTestExec) execStream(_ context.Context, _ string, _ json.RawMessage, _ chan<- StreamEvent) (ToolResult, error) {
	atomic.AddInt32(&p.calls, 1)
	return p.result, nil
}

func TestNewStandardDispatch_PolicyRetries(t *testing.T) {
	p := &policyTestExec{
		result: ToolResult{Content: []byte(`"done"`)},
		errFn: func(n int32) error {
			if n < 3 {
				return core.RetryableError(errors.New("transient"))
			}
			return nil
		},
	}
	cfg := StandardDispatchConfig{
		ExecuteTool:     p.exec,
		IsStreamingTool: func(string) bool { return false },
		ResolvePolicy: func(name string) (core.ToolPolicy, bool) {
			return core.ToolPolicy{Retries: 5, RetryDelay: 1 * time.Millisecond}, true
		},
	}
	d := NewStandardDispatch(cfg)
	dr := d(context.Background(), ToolCall{Name: "myTool", Args: json.RawMessage(`{}`)})
	if dr.IsError {
		t.Fatalf("expected success after retries, got IsError; Content=%q", dr.Content)
	}
	if p.calls != 3 {
		t.Errorf("calls = %d, want 3", p.calls)
	}
}

func TestNewStandardDispatch_StreamingBypassesPolicy(t *testing.T) {
	p := &policyTestExec{stream: true, result: ToolResult{Content: []byte(`"streamed"`)}}
	cfg := StandardDispatchConfig{
		ExecuteTool:       p.exec,
		ExecuteToolStream: p.execStream,
		StreamCh:          make(chan StreamEvent, 1),
		IsStreamingTool:   func(string) bool { return true },
		ResolvePolicy: func(string) (core.ToolPolicy, bool) {
			return core.ToolPolicy{Retries: 99}, true // would loop a lot if applied
		},
	}
	d := NewStandardDispatch(cfg)
	dr := d(context.Background(), ToolCall{Name: "stream", Args: json.RawMessage(`{}`)})
	if dr.IsError {
		t.Fatalf("unexpected IsError: %q", dr.Content)
	}
	if p.calls != 1 {
		t.Errorf("calls = %d, want 1 (policy must NOT apply to streaming tools)", p.calls)
	}
}

func TestNewStandardDispatch_NoPolicyPassthrough(t *testing.T) {
	p := &policyTestExec{result: ToolResult{Content: []byte(`"plain"`)}}
	cfg := StandardDispatchConfig{
		ExecuteTool:     p.exec,
		IsStreamingTool: func(string) bool { return false },
		ResolvePolicy:   func(string) (core.ToolPolicy, bool) { return core.ToolPolicy{}, false },
	}
	d := NewStandardDispatch(cfg)
	dr := d(context.Background(), ToolCall{Name: "plain", Args: nil})
	if dr.IsError {
		t.Fatalf("unexpected IsError: %q", dr.Content)
	}
	if p.calls != 1 {
		t.Errorf("calls = %d, want 1", p.calls)
	}
}
```

Add to the imports in `agent/tool_policy_test.go`:
```go
"encoding/json"
"sync/atomic"
```
(already imported from earlier tests except possibly `encoding/json`).

- [ ] **Step 2: Run tests to confirm failure**

```bash
go test ./agent/ -run TestNewStandardDispatch_Policy -v
```

Expected: compile errors `unknown field ResolvePolicy / IsStreamingTool in StandardDispatchConfig`.

- [ ] **Step 3: Extend `StandardDispatchConfig` and `NewStandardDispatch`**

In `agent/dispatch.go`, modify the `StandardDispatchConfig` struct (currently lines 77-86) to add two optional hooks at the end:

```go
type StandardDispatchConfig struct {
	Builtins          func(ctx context.Context, tc ToolCall, dispatch DispatchFunc) (DispatchResult, bool)
	SpawnHandler      func(ctx context.Context, args json.RawMessage, defs []ToolDefinition, exec ToolExecFunc) DispatchResult
	AgentRouter       AgentRouter
	ExecuteTool       ToolExecFunc
	ExecuteToolStream ToolExecStreamFunc
	ResolvedToolDefs  []ToolDefinition
	StreamCh          chan<- StreamEvent
	// ResolvePolicy returns the ToolPolicy for a tool name. nil = no policy
	// lookup. Returning (_, false) means no policy applies (pass-through).
	// LLMAgent passes a closure over Config.resolveToolPolicy.
	ResolvePolicy func(name string) (core.ToolPolicy, bool)
	// IsStreamingTool reports whether the tool registered under name is a
	// StreamingAnyTool. Used to bypass policy wrapping for streaming tools.
	// nil ⇒ treat all tools as non-streaming.
	IsStreamingTool func(name string) bool
	// Logger is used to emit a one-time warning when a streaming tool
	// has a policy registered. nil = no logging.
	Logger *slog.Logger
}
```

Add to the imports in `agent/dispatch.go`:
```go
"log/slog"
"sync"
"github.com/nevindra/oasis/core"
```

Modify `NewStandardDispatch` (currently lines 88-109) to apply policy on the non-streaming path:

```go
func NewStandardDispatch(cfg StandardDispatchConfig) DispatchFunc {
	// streamPolicyWarned tracks tool names for which a policy was registered
	// but the tool resolved as streaming; we log a warning once per name.
	var streamPolicyWarned sync.Map

	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
		if cfg.Builtins != nil {
			if r, ok := cfg.Builtins(ctx, tc, dispatch); ok {
				return r
			}
		}
		if tc.Name == "spawn_agent" && cfg.SpawnHandler != nil {
			return cfg.SpawnHandler(ctx, tc.Args, cfg.ResolvedToolDefs, cfg.ExecuteTool)
		}
		if cfg.AgentRouter != nil {
			if r, ok := cfg.AgentRouter(ctx, tc); ok {
				return r
			}
		}

		isStreaming := cfg.IsStreamingTool != nil && cfg.IsStreamingTool(tc.Name)

		// Streaming-tool bypass: policy never applies to a streaming tool.
		// Warn once if a user attempted to attach a policy to a streaming tool.
		if isStreaming {
			if cfg.ResolvePolicy != nil {
				if _, hasPolicy := cfg.ResolvePolicy(tc.Name); hasPolicy {
					if _, already := streamPolicyWarned.LoadOrStore(tc.Name, struct{}{}); !already && cfg.Logger != nil {
						cfg.Logger.Warn("tool policy ignored: tool is a StreamingAnyTool", "tool", tc.Name)
					}
				}
			}
			if cfg.StreamCh != nil && cfg.ExecuteToolStream != nil {
				return toolResultToDispatch(cfg.ExecuteToolStream(ctx, tc.Name, tc.Args, cfg.StreamCh))
			}
			return toolResultToDispatch(cfg.ExecuteTool(ctx, tc.Name, tc.Args))
		}

		// Non-streaming path: apply policy if one is registered for this name.
		if cfg.ResolvePolicy != nil {
			if policy, ok := cfg.ResolvePolicy(tc.Name); ok {
				return toolResultToDispatch(runWithPolicy(ctx, policy, func(c context.Context) (ToolResult, error) {
					return cfg.ExecuteTool(c, tc.Name, tc.Args)
				}))
			}
		}
		return DispatchTool(ctx, cfg.ExecuteTool, cfg.ExecuteToolStream, tc.Name, tc.Args, cfg.StreamCh)
	}
	return dispatch
}
```

- [ ] **Step 4: Run tests to confirm pass**

```bash
go test ./agent/ -run TestNewStandardDispatch_Policy -v
```

Expected: 3 cases PASS.

- [ ] **Step 5: Run the full agent test suite — no regressions**

```bash
go test ./agent/ -v 2>&1 | tail -30
```

Expected: every previously-green test still passes. Existing `network/` tests should also keep passing because they pass `StandardDispatchConfig` without `ResolvePolicy` / `IsStreamingTool`, falling through to the original `DispatchTool` path.

- [ ] **Step 6: Commit**

```bash
git add agent/dispatch.go agent/tool_policy_test.go
git commit -m "feat(agent): apply ToolPolicy in dispatch + streaming bypass"
```

---

## Task 11: Wire policy resolver + streaming checker in `LLMAgent.makeDispatch`

**Files:**
- Modify: `agent/llm.go:85-94` (`makeDispatch`)
- Modify: `agent/llm.go:55-78` (`buildLoopConfig` — pass the registry to the dispatch)
- Modify: `agent/llm.go` (anywhere a similar `buildLoopConfigFrom` exists, mirror the change — verify by grep)

The agent currently passes `executeTool = a.tools.Execute` (function value) into the dispatcher. We also need to expose the registry so `IsStreamingTool(name)` is callable inside the dispatch closure, and we need to pass the policy resolver.

- [ ] **Step 1: Locate every dispatch construction site**

```bash
grep -n "NewStandardDispatch\|StandardDispatchConfig{" /home/ubuntu/code/oasis/agent/
```

Read each site. There is at least one in `agent/llm.go` (`LLMAgent.makeDispatch`). If there is a second site (e.g. `buildLoopConfigFrom`'s analogue), it must be updated too.

- [ ] **Step 2: Pass the registry/streaming-checker and policy resolver into `makeDispatch`**

Update `LLMAgent.makeDispatch` signature in `agent/llm.go:85` to:

```go
func (a *LLMAgent) makeDispatch(executeTool ToolExecFunc, executeToolStream ToolExecStreamFunc, ch chan<- StreamEvent, resolvedToolDefs []ToolDefinition, isStreamingTool func(string) bool) DispatchFunc {
	return NewStandardDispatch(StandardDispatchConfig{
		Builtins:          a.DispatchBuiltins,
		SpawnHandler:      a.ExecuteSpawn,
		ExecuteTool:       executeTool,
		ExecuteToolStream: executeToolStream,
		ResolvedToolDefs:  resolvedToolDefs,
		StreamCh:          ch,
		ResolvePolicy:     a.cfg().resolveToolPolicy,
		IsStreamingTool:   isStreamingTool,
		Logger:            a.logger,
	})
}
```

Caveat: `a.cfg()` returns a synthesized minimal Config — verify the policy maps survive that path. Read `agent/llm.go:129-147` (the `cfg()` method) and extend it to copy `toolPolicies` and `toolPolicyMatchers`:

```go
func (a *LLMAgent) cfg() *Config {
	var maxSteps int
	if a.maxSteps != 0 {
		maxSteps = a.maxSteps
	}
	return &Config{
		prompt:              a.systemPrompt,
		maxIter:             a.maxIter,
		responseSchema:      a.responseSchema,
		maxAttachmentBytes:  a.maxAttachmentBytes,
		maxToolResultLen:    a.maxToolResultLen,
		maxPlanSteps:        a.maxPlanSteps,
		generationParams:    a.genParams,
		tracer:              a.tracer,
		logger:              a.logger,
		inputHandler:        a.handler,
		maxSteps:            &maxSteps,
		toolPolicies:        a.toolPolicies,
		toolPolicyMatchers:  a.toolPolicyMatchers,
	}
}
```

You must also add `toolPolicies` and `toolPolicyMatchers` fields to whatever struct backs `a` (likely `AgentCore`). Find it:

```bash
grep -n "toolPolicies\|maxToolResultLen" /home/ubuntu/code/oasis/agent/agentcore.go
```

Mirror the existing pattern for `maxToolResultLen` (a similar config-→-core field). Add to `AgentCore`:

```go
toolPolicies       map[string]core.ToolPolicy
toolPolicyMatchers []toolPolicyMatcher
```

And in `InitCore` (or whichever function copies Config fields into AgentCore), copy the new fields:

```go
toolPolicies:       cfg.toolPolicies,
toolPolicyMatchers: cfg.toolPolicyMatchers,
```

Grep for the exact spelling used in InitCore by another similar field (e.g. `maxPlanSteps`) and mirror it precisely.

- [ ] **Step 3: Pass the streaming-checker from the registry in `buildLoopConfig`**

Update `buildLoopConfig` in `agent/llm.go:55-78`:

```go
func (a *LLMAgent) buildLoopConfig(ctx context.Context, task AgentTask, ch chan<- StreamEvent) LoopConfig {
	prompt, provider := a.ResolvePromptAndProvider(ctx, task)
	if a.activeSkillInstructions != "" {
		prompt = prompt + "\n\n# Active Skills\n\n" + a.activeSkillInstructions
	}

	var toolDefs []ToolDefinition
	var executeTool ToolExecFunc
	var executeToolStream ToolExecStreamFunc
	var isStreamingTool func(string) bool
	if dynDefs, dynExec := a.ResolveDynamicTools(ctx, task); dynDefs != nil {
		a.logger.Debug("using dynamic tools", "agent", a.name, "tool_count", len(dynDefs))
		toolDefs = a.CacheBuiltinToolDefs(dynDefs)
		executeTool = dynExec
		// Dynamic tools currently have no streaming variant exposed — treat
		// them all as non-streaming for policy purposes. (Follow-up: extend
		// ToolsFunc to return a streaming-aware registry.)
		isStreamingTool = func(string) bool { return false }
	} else {
		toolDefs = a.cachedToolDefs
		executeTool = a.tools.Execute
		executeToolStream = a.tools.ExecuteStream
		isStreamingTool = a.tools.IsStreamingTool
	}

	return a.BaseLoopConfig("agent:"+a.name, prompt, provider, toolDefs, a.makeDispatch(executeTool, executeToolStream, ch, toolDefs, isStreamingTool))
}
```

Apply the same pattern to `buildLoopConfigFrom` (search for the second build site).

- [ ] **Step 4: Build and run the full test suite**

```bash
go build ./...
go test ./... 2>&1 | tail -50
```

Expected: clean build, all tests PASS. If a `network/` test fails because `NewStandardDispatch` is called without `ResolvePolicy`/`IsStreamingTool`, those fields are optional (nil-safe in the dispatch closure) — no change should be needed.

- [ ] **Step 5: Add an end-to-end-style test asserting an LLMAgent with WithToolPolicy retries a transient tool**

Append to `agent/tool_policy_test.go`:

```go
// retryingFakeTool returns RetryableError for the first N calls, then succeeds.
type retryingFakeTool struct {
	name     string
	failN    int32
	calls    int32
}

func (r *retryingFakeTool) Name() string              { return r.name }
func (r *retryingFakeTool) Definition() ToolDefinition { return ToolDefinition{Name: r.name} }
func (r *retryingFakeTool) ExecuteRaw(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	n := atomic.AddInt32(&r.calls, 1)
	if n <= r.failN {
		return ToolResult{}, core.RetryableError(errors.New("transient"))
	}
	return ToolResult{Content: []byte(`"ok"`)}, nil
}

// (End-to-end test through LLMAgent.Execute requires a fake Provider — skip
// if existing test helpers don't provide one; this assertion can be done at
// the dispatch closure level via the tests already added above. If a fake
// Provider exists in agent/testhelpers_test.go or similar, write the
// end-to-end test there.)
```

(If no fake provider is available, this end-to-end test is OPTIONAL — the dispatch-closure tests in Task 10 already give full retry coverage. Skip if testhelpers don't already provide a fake provider.)

- [ ] **Step 6: Commit**

```bash
git add agent/llm.go agent/agentcore.go agent/tool_policy_test.go
git commit -m "feat(agent): wire ToolPolicy resolver + streaming checker"
```

---

## Task 12: Re-export new public API in `oasis.go`

**Files:**
- Modify: `oasis.go` (Tool primitives block, around lines 422-471)

The umbrella exposes the most-common APIs; agent-level options stay accessible via the `agent` subpackage import for callers who want them (consistent with the existing pattern — `WithToolResultStore` etc. are not re-exported by name).

- [ ] **Step 1: Add re-exports**

In `oasis.go`, immediately after the existing `OutSchemaProvider`-relevant exports (around line 450, after `EraseStreaming`):

```go
// --- Tool robustness primitives ---

// ToolPolicy describes a per-tool timeout and retry policy applied by the
// agent's dispatch wrapper. See agent.WithToolPolicy / agent.WithToolPolicyMatch.
type ToolPolicy = core.ToolPolicy

// Retryable is the opt-in convention for marking a Go error as retryable.
// Use core.RetryableError to wrap an existing error so it satisfies this
// interface; DefaultRetryOn honors the wrapper via errors.As.
type Retryable = core.Retryable

// OutSchemaProvider is the opt-in override for Erase's auto-derived output
// schema. Tool implementations may implement this to publish a richer
// JSON Schema than reflection produces.
type OutSchemaProvider = core.OutSchemaProvider

// RetryableError wraps err so DefaultRetryOn reports it as retryable.
func RetryableError(err error) error { return core.RetryableError(err) }

// DefaultRetryOn is the predicate used when ToolPolicy.RetryOn is nil.
// Exported for composition: user predicates can fall through to it.
func DefaultRetryOn(err error) bool { return core.DefaultRetryOn(err) }
```

- [ ] **Step 2: Verify build**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 3: Run all tests**

```bash
go test ./... 2>&1 | tail -10
```

Expected: every package PASS.

- [ ] **Step 4: Commit**

```bash
git add oasis.go
git commit -m "feat: re-export ToolPolicy, Retryable, RetryableError, DefaultRetryOn, OutSchemaProvider"
```

---

## Task 13: CHANGELOG entry + lint pass

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add `[Unreleased]` entries**

Insert under `## [Unreleased]` → `### Breaking`:

```markdown
- **`Tool.Execute` errors now propagate as Go errors from the erased adapters.** Previously `core.Erase` swallowed the Go error from `tool.Execute(...)` into `ToolResult.Error` and returned `(result, nil)`. It now returns `(result, err)` so the new dispatch policy wrapper can inspect typed errors (`Retryable`, `net.Error.Timeout()`, `context.DeadlineExceeded`). The LLM-visible result is unchanged because `agent.toolResultToDispatch` already prioritizes the Go error path. External `AnyTool` implementers that read `ToolResult.Error` are unaffected. Implementers that re-wrap erased tools and previously assumed a nil error return from `ExecuteRaw` must now propagate or absorb the typed error. Argument-unmarshal errors and result-marshal errors continue to return `(result, nil)`.
```

Insert under `### Added`:

```markdown
- `core.ToolPolicy` (per-tool `Timeout`, `Retries`, `RetryDelay`, `MaxRetryDelay`, `RetryOn`).
- `core.Retryable` interface, `core.RetryableError(err) error` wrapper, `core.DefaultRetryOn(err) bool` predicate, `core.BackoffDelay(base, max, attempt)` helper.
- `core.OutSchemaProvider` opt-in interface — tools may publish a custom output JSON Schema that overrides the schema derived from `Out` by reflection.
- `core.ToolDefinition.OutputSchema json.RawMessage` field, populated by `core.Erase` / `core.EraseStreaming` via `DeriveSchema[Out]()` (or the override). Provider implementations decide whether to forward this to the LLM.
- `core.ToolRegistry.IsStreamingTool(name) bool` lookup.
- `agent.WithToolPolicy(name string, p core.ToolPolicy)` and `agent.WithToolPolicyMatch(matcher func(name string) bool, p core.ToolPolicy)` options. ServeMux-style precedence: exact name first, then matchers in registration order. Streaming tools bypass the policy wrapper entirely (with a one-shot `slog.Warn` if a policy was registered for one).
- Umbrella re-exports: `oasis.ToolPolicy`, `oasis.Retryable`, `oasis.RetryableError`, `oasis.DefaultRetryOn`, `oasis.OutSchemaProvider`.
```

Insert under `### Changed` (or create the section if absent):

```markdown
- `core.Erase` now applies structural input coercion (`null`/empty → `{}`, stringified-JSON object/array unwrap one level) before `json.Unmarshal`. Coercion is pure-function, zero-alloc on the happy path, and never errors — malformed inputs that don't match either pattern pass through unchanged so the existing `json.Unmarshal` failure path reports the real problem. This default-on behavior is intentional: no opt-out.
```

- [ ] **Step 2: Run linter**

```bash
golangci-lint run ./...
```

Expected: no findings (depguard rules respected; no new imports added to `core/` from outside stdlib).

- [ ] **Step 3: Final full test pass**

```bash
go test ./... 2>&1 | tail -30
```

Expected: every package PASS.

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): tool robustness layer entries"
```

---

## Done criteria

- All of `go test ./...` passes.
- `golangci-lint run ./...` passes.
- `core/` imports only stdlib (verify with `go list -deps ./core/ | grep -v '^$' | grep github.com`).
- A unit test exercising `WithToolPolicy("foo", ...)` with a tool that fails twice then succeeds verifies retry behavior end-to-end (Task 10's dispatch test covers this).
- A unit test exercising `WithToolPolicyMatch(prefix == "mcp__")` verifies the matcher path (Task 9 covers this).
- The streaming-bypass test asserts the policy is NOT applied when `IsStreamingTool` returns true (Task 10 covers this).
- `OutputSchema` is non-nil for `Tool[InStruct, OutStruct]` and matches `DeriveSchema[OutStruct]()` (Task 4 covers this).
- `RetryableError` wraps with `Unwrap`, satisfies `errors.As(&Retryable)`, and `DefaultRetryOn` recognizes it (Task 6 covers this).
- `CHANGELOG.md` has Breaking, Added, Changed entries.

## Out of scope (deferred follow-ups)

- HTTP status-code retry helpers (`RetryHTTPCodes(...)`).
- Jitter in backoff (add only if thundering-herd is observed).
- Per-tool tracing spans (observer satellite concern).
- Tool approval gates (separate spec).
- Pre-execute authorization hook (`WithToolAuth`).
- Sub-agent `DelegationConfig` (separate spec).
- Provider-side wiring of `OutputSchema` into tool specs sent to the LLM (Gemini, OpenAI-compat).
- Adopting `core.RetryableError` inside `mcp/tool_wrapper.go` for transient MCP failures.
