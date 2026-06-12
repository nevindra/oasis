# Generative UI (Oasis primitive) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Commit policy (project override):** Do **NOT** run `git commit`. Each task ends with a **Stage** step (`git add`). Leave the working tree dirty — the user reviews and commits batches themselves.

**Goal:** Add a first-class, typed "this tool output is a UI component" primitive to Oasis so an agent can stream app-rendered components (`{name, props}`) instead of only text — without touching the agent loop's existing event emission.

**Architecture:** A tool sets an optional `UI *UIComponent` on its `ToolResult` (via the `UIResult(...)` helper, or by having a typed tool's `Out` implement `UIRenderable`). The descriptor threads through the existing dispatch path (`ToolResult` → `DispatchResult` → `toolExecResult`) and is emitted as a new `EventUIComponent` stream event right after `EventToolCallResult`, reusing the existing `ID`/`Name`/`Object` fields on `StreamEvent`. Consumers (Athena, A2A, a future AG-UI adapter) read the new event; the core loop is otherwise unchanged.

**Tech Stack:** Go 1.24, standard library only (`encoding/json`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-12-generative-ui-design.md`

---

## File Structure

| File | Responsibility | New? |
|------|----------------|------|
| `core/types.go` | `UIComponent` struct + `UI` field on `ToolResult` | modify |
| `core/stream.go` | `EventUIComponent` constant + `AllStreamEventTypes()` entry | modify |
| `core/tool.go` | `UIRenderable` optional-capability interface | modify |
| `core/tool_helpers.go` | `UIResult[T]` helper | modify |
| `core/erase.go` | auto-set `UI` when `Out` implements `UIRenderable` | modify |
| `core/ui_test.go` | tests for type/const/helper/erase | create |
| `internal/runtime/dispatch.go` | `UI` field on `DispatchResult` | modify |
| `agent/dispatch.go` | copy UI in `toolResultToDispatch`; `ui` on `toolExecResult`; set at both sites | modify |
| `agent/dispatch_ui_test.go` | unit tests for the threading | create |
| `agent/iteration.go` | emit `EventUIComponent` after `EventToolCallResult` | modify |
| `agent/ui_stream_test.go` | end-to-end emit test through the loop | create |
| `oasis.go` | re-export `UIComponent`, `UIRenderable`, `UIResult`, `EventUIComponent` | modify |
| `ui_reexport_test.go` | root re-export test | create |
| `docs/external/agent/api.md`, `docs/external/tools/examples.md`, `CHANGELOG.md` | docs | modify |

Tasks are ordered by dependency — earlier types are referenced by later tasks.

---

## Task 1: Core types — `UIComponent`, `ToolResult.UI`, `EventUIComponent`

**Files:**
- Modify: `core/types.go:96` (the `ToolResult` struct)
- Modify: `core/stream.go:131` (end of the const block) and `core/stream.go:137` (`AllStreamEventTypes`)
- Test: `core/ui_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `core/ui_test.go`:

```go
package core

import (
	"encoding/json"
	"testing"
)

func TestUIComponent_RoundTrip(t *testing.T) {
	c := UIComponent{Name: "FlightCard", Props: json.RawMessage(`{"count":2}`)}
	r := ToolResult{Content: "x", UI: &c}
	if r.UI.Name != "FlightCard" {
		t.Fatalf("UI.Name = %q, want FlightCard", r.UI.Name)
	}
	if string(r.UI.Props) != `{"count":2}` {
		t.Fatalf("UI.Props = %s", r.UI.Props)
	}
}

func TestEventUIComponent_InAllStreamEventTypes(t *testing.T) {
	found := false
	for _, e := range AllStreamEventTypes() {
		if e == EventUIComponent {
			found = true
		}
	}
	if !found {
		t.Fatal("EventUIComponent missing from AllStreamEventTypes()")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./core/ -run 'TestUIComponent_RoundTrip|TestEventUIComponent_InAllStreamEventTypes' -v`
Expected: FAIL to compile — `undefined: UIComponent`, `undefined: EventUIComponent`.

- [ ] **Step 3: Add the `UIComponent` struct and `ToolResult.UI` field**

In `core/types.go`, replace the `ToolResult` struct (currently at line 96) with:

```go
// UIComponent describes a frontend component to render in place of (or
// alongside) a tool result's text. Name is a registry key the frontend
// resolves to a renderer; Props is the typed payload the renderer receives.
type UIComponent struct {
	Name  string          `json:"name"`
	Props json.RawMessage `json:"props"`
}

type ToolResult struct {
	Content     string       `json:"content,omitempty"`
	Error       string       `json:"error,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"` // multimodal content (images, PDFs, etc.) passed to the LLM
	// UI, when non-nil, instructs consumers to render the result as the named
	// frontend component instead of (or alongside) Content. Set via UIResult
	// or by an Out type implementing UIRenderable.
	UI *UIComponent `json:"ui,omitempty"`
}
```

(`encoding/json` is already imported in `core/types.go`.)

- [ ] **Step 4: Add the `EventUIComponent` constant**

In `core/stream.go`, inside the `const (...)` block, immediately after the `EventProcessorSuspended` line (currently line 131), add:

```go
	// EventUIComponent signals a tool produced a renderable UI component.
	// ID correlates with the preceding EventToolCallStart/Result; Name carries
	// the component name; Object carries the props JSON. Emitted directly after
	// the tool's EventToolCallResult on the success path only.
	EventUIComponent StreamEventType = "ui-component"
```

- [ ] **Step 5: Register it in `AllStreamEventTypes()`**

In `core/stream.go`, in the slice returned by `AllStreamEventTypes()`, add `EventUIComponent,` as a new line. Place it after `EventToolCallResult,` (the slice is hand-maintained; ordering is not significant, but keep it near the tool events for readability).

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./core/ -run 'TestUIComponent_RoundTrip|TestEventUIComponent_InAllStreamEventTypes' -v`
Expected: PASS (both tests).

- [ ] **Step 7: Stage (do not commit)**

```bash
git add core/types.go core/stream.go core/ui_test.go
```

---

## Task 2: `UIResult` helper

**Files:**
- Modify: `core/tool_helpers.go` (add after `JSONResult`, ~line 25)
- Test: `core/ui_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `core/ui_test.go`:

```go
func TestUIResult(t *testing.T) {
	type props struct {
		Title string `json:"title"`
	}
	r := UIResult("Card", props{Title: "hi"})
	if r.UI == nil {
		t.Fatal("UI is nil")
	}
	if r.UI.Name != "Card" {
		t.Fatalf("UI.Name = %q, want Card", r.UI.Name)
	}
	if string(r.UI.Props) != `{"title":"hi"}` {
		t.Fatalf("UI.Props = %s", r.UI.Props)
	}
	// Content mirrors the props JSON so the LLM still "sees" the rendered data.
	if r.Content != `{"title":"hi"}` {
		t.Fatalf("Content = %q", r.Content)
	}
	if r.Error != "" {
		t.Fatalf("Error = %q, want empty", r.Error)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./core/ -run TestUIResult -v`
Expected: FAIL to compile — `undefined: UIResult`.

- [ ] **Step 3: Implement the helper**

In `core/tool_helpers.go`, after the `JSONResult` function (ends at line 25), add:

```go
// UIResult builds a ToolResult that renders as the named frontend component.
// props is marshaled to JSON for both UI.Props and Content, so the LLM still
// "sees" the data it rendered and the loop can continue with context. Panics
// on marshal failure — a programming error, matching JSONResult's convention.
func UIResult[T any](name string, props T) ToolResult {
	b, err := json.Marshal(props)
	if err != nil {
		panic("core.UIResult: json.Marshal failed: " + err.Error())
	}
	return ToolResult{
		Content: string(b),
		UI:      &UIComponent{Name: name, Props: b},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./core/ -run TestUIResult -v`
Expected: PASS.

- [ ] **Step 5: Stage (do not commit)**

```bash
git add core/tool_helpers.go core/ui_test.go
```

---

## Task 3: `UIRenderable` interface + `Erase` auto-detection

**Files:**
- Modify: `core/tool.go` (add interface after the `StreamingTool` interface, ~line 61)
- Modify: `core/erase.go:49-53` (`erasedTool.ExecuteRaw`) and `core/erase.go:98-102` (`erasedStreamingTool.ExecuteStream`)
- Test: `core/ui_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `core/ui_test.go`:

```go
type uiOut struct {
	V int `json:"v"`
}

func (uiOut) UIComponent() string { return "Widget" }

type plainOut struct {
	V int `json:"v"`
}

type uiTool struct{}

func (uiTool) Definition() ToolMeta { return ToolMeta{Name: "ui", Description: "d"} }
func (uiTool) Execute(_ context.Context, _ struct{}) (uiOut, error) {
	return uiOut{V: 7}, nil
}

type plainTool struct{}

func (plainTool) Definition() ToolMeta { return ToolMeta{Name: "plain", Description: "d"} }
func (plainTool) Execute(_ context.Context, _ struct{}) (plainOut, error) {
	return plainOut{V: 7}, nil
}

func TestErase_SetsUIWhenOutRenderable(t *testing.T) {
	at := Erase[struct{}, uiOut](uiTool{})
	res, err := at.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if res.UI == nil {
		t.Fatal("UI is nil, want set")
	}
	if res.UI.Name != "Widget" {
		t.Fatalf("UI.Name = %q, want Widget", res.UI.Name)
	}
	if string(res.UI.Props) != `{"v":7}` {
		t.Fatalf("UI.Props = %s", res.UI.Props)
	}
	if res.Content != `{"v":7}` {
		t.Fatalf("Content = %q", res.Content)
	}
}

func TestErase_NoUIWhenOutNotRenderable(t *testing.T) {
	at := Erase[struct{}, plainOut](plainTool{})
	res, err := at.ExecuteRaw(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if res.UI != nil {
		t.Fatalf("UI = %+v, want nil", res.UI)
	}
}

type uiStreamTool struct{ uiTool }

func (uiStreamTool) ExecuteStream(_ context.Context, _ struct{}, _ chan<- StreamEvent) (uiOut, error) {
	return uiOut{V: 7}, nil
}

func TestEraseStreaming_SetsUIWhenOutRenderable(t *testing.T) {
	st := EraseStreaming[struct{}, uiOut](uiStreamTool{})
	res, err := st.ExecuteStream(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if res.UI == nil || res.UI.Name != "Widget" {
		t.Fatalf("UI = %+v, want Widget", res.UI)
	}
}
```

Add `"context"` to the imports of `core/ui_test.go` (the test file's import block currently has only `encoding/json` and `testing`):

```go
import (
	"context"
	"encoding/json"
	"testing"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./core/ -run 'TestErase_SetsUIWhenOutRenderable|TestErase_NoUIWhenOutNotRenderable|TestEraseStreaming_SetsUIWhenOutRenderable' -v`
Expected: the package compiles (the tests reference only `res.UI`, added in Task 1, and a plain `UIComponent()` method — not the `UIRenderable` interface). `TestErase_SetsUIWhenOutRenderable` and `TestEraseStreaming_SetsUIWhenOutRenderable` FAIL on the assertion `UI is nil` (Erase doesn't set it yet); `TestErase_NoUIWhenOutNotRenderable` already PASSes.

- [ ] **Step 3: Add the `UIRenderable` interface**

In `core/tool.go`, after the `StreamingTool` interface (ends at line 61), add:

```go
// UIRenderable is the optional capability a typed tool's Out type implements to
// render as a frontend component. When present, Erase/EraseStreaming set
// ToolResult.UI to {Name: Out.UIComponent(), Props: <marshaled Out>}. Mirrors
// the OutSchemaProvider opt-in pattern.
type UIRenderable interface {
	UIComponent() string
}
```

- [ ] **Step 4: Set UI in `erasedTool.ExecuteRaw`**

In `core/erase.go`, replace the tail of `ExecuteRaw` (currently lines 49-53):

```go
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	return ToolResult{Content: string(body)}, nil
```

with:

```go
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	res := ToolResult{Content: string(body)}
	if r, ok := any(out).(UIRenderable); ok {
		res.UI = &UIComponent{Name: r.UIComponent(), Props: body}
	}
	return res, nil
```

- [ ] **Step 5: Set UI in `erasedStreamingTool.ExecuteStream`**

In `core/erase.go`, apply the identical change to the tail of `ExecuteStream` (currently lines 98-102) — replace:

```go
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	return ToolResult{Content: string(body)}, nil
```

with:

```go
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	res := ToolResult{Content: string(body)}
	if r, ok := any(out).(UIRenderable); ok {
		res.UI = &UIComponent{Name: r.UIComponent(), Props: body}
	}
	return res, nil
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./core/ -run 'TestErase_SetsUIWhenOutRenderable|TestErase_NoUIWhenOutNotRenderable|TestEraseStreaming_SetsUIWhenOutRenderable' -v`
Expected: PASS (all three).

- [ ] **Step 7: Run the whole core package to catch regressions**

Run: `go test ./core/ -v`
Expected: PASS (all existing + new tests).

- [ ] **Step 8: Stage (do not commit)**

```bash
git add core/tool.go core/erase.go core/ui_test.go
```

---

## Task 4: Thread `UI` through the dispatch path

**Files:**
- Modify: `internal/runtime/dispatch.go:11-17` (`DispatchResult`)
- Modify: `agent/dispatch.go:26` (`toolResultToDispatch`), `agent/dispatch.go:121-127` (`toolExecResult`), `agent/dispatch.go:163` and `agent/dispatch.go:193` (construction sites)
- Test: `agent/dispatch_ui_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `agent/dispatch_ui_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestToolResultToDispatch_CopiesUI(t *testing.T) {
	ui := &core.UIComponent{Name: "Card", Props: json.RawMessage(`{"a":1}`)}
	dr := toolResultToDispatch(core.ToolResult{Content: "x", UI: ui}, nil)
	if dr.UI != ui {
		t.Fatalf("DispatchResult.UI = %+v, want the same pointer", dr.UI)
	}
}

func TestToolResultToDispatch_NoUIOnError(t *testing.T) {
	dr := toolResultToDispatch(core.ToolResult{Error: "boom", UI: &core.UIComponent{Name: "Card"}}, nil)
	if dr.UI != nil {
		t.Fatalf("DispatchResult.UI = %+v, want nil on error result", dr.UI)
	}
}

func TestDispatchParallel_PropagatesUI(t *testing.T) {
	ui := &core.UIComponent{Name: "Card", Props: json.RawMessage(`{}`)}
	dispatch := func(_ context.Context, _ core.ToolCall) DispatchResult {
		return DispatchResult{Content: "ok", UI: ui}
	}

	// Single-call fast path.
	single := dispatchParallel(context.Background(), []core.ToolCall{{ID: "1", Name: "t"}}, dispatch, 4)
	if single[0].ui != ui {
		t.Fatalf("single: toolExecResult.ui = %+v, want set", single[0].ui)
	}

	// Multi-call worker path.
	multi := dispatchParallel(context.Background(),
		[]core.ToolCall{{ID: "1", Name: "t"}, {ID: "2", Name: "t"}}, dispatch, 4)
	for i, r := range multi {
		if r.ui != ui {
			t.Fatalf("multi[%d]: toolExecResult.ui = %+v, want set", i, r.ui)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run 'TestToolResultToDispatch_CopiesUI|TestToolResultToDispatch_NoUIOnError|TestDispatchParallel_PropagatesUI' -v`
Expected: FAIL to compile — `dr.UI undefined` (field not on `DispatchResult`), `r.ui undefined` (field not on `toolExecResult`).

- [ ] **Step 3: Add `UI` to `DispatchResult`**

In `internal/runtime/dispatch.go`, replace the `DispatchResult` struct (lines 11-17):

```go
type DispatchResult struct {
	Content     string
	Usage       core.Usage
	Attachments []core.Attachment
	// IsError signals that Content represents an error message.
	IsError bool
	// UI, when non-nil, carries a renderable component descriptor produced by
	// the tool. Copied from ToolResult.UI on the success path.
	UI *core.UIComponent
}
```

- [ ] **Step 4: Copy `UI` in `toolResultToDispatch`**

In `agent/dispatch.go`, replace the success branch of `toolResultToDispatch` (line 26):

```go
	return DispatchResult{Content: result.Content, Attachments: result.Attachments}
```

with:

```go
	return DispatchResult{Content: result.Content, Attachments: result.Attachments, UI: result.UI}
```

(Leave the two error branches above it unchanged — error results carry no UI.)

- [ ] **Step 5: Add `ui` to `toolExecResult`**

In `agent/dispatch.go`, replace the `toolExecResult` struct (lines 121-127):

```go
// toolExecResult holds the result of a single parallel tool call.
type toolExecResult struct {
	content     string
	usage       core.Usage
	attachments []core.Attachment
	duration    time.Duration
	isError     bool
	ui          *core.UIComponent
}
```

- [ ] **Step 6: Populate `ui` at both construction sites**

In `agent/dispatch.go`, in `dispatchParallel`, the single-call fast path (line 163):

```go
		return []toolExecResult{{content: dr.Content, usage: dr.Usage, attachments: dr.Attachments, duration: time.Since(start), isError: dr.IsError, ui: dr.UI}}
```

and the worker path (line 193):

```go
				resultCh <- indexedResult{w.idx, toolExecResult{content: dr.Content, usage: dr.Usage, attachments: dr.Attachments, duration: time.Since(start), isError: dr.IsError, ui: dr.UI}}
```

(The two ctx-cancelled `toolExecResult{content: "error: ...", isError: true}` literals at lines 188 and 217 need no change — error paths have no UI.)

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./agent/ -run 'TestToolResultToDispatch_CopiesUI|TestToolResultToDispatch_NoUIOnError|TestDispatchParallel_PropagatesUI' -v`
Expected: PASS (all three).

- [ ] **Step 8: Stage (do not commit)**

```bash
git add internal/runtime/dispatch.go agent/dispatch.go agent/dispatch_ui_test.go
```

---

## Task 5: Emit `EventUIComponent` in the iteration loop

**Files:**
- Modify: `agent/iteration.go` (immediately after the `EventToolCallResult` emit block, lines 461-473)
- Test: `agent/ui_stream_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `agent/ui_stream_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestEventUIComponent_EmittedAfterToolResult(t *testing.T) {
	flightTool := core.RawTool("show_flights", "shows flights",
		json.RawMessage(`{"type":"object"}`),
		func(_ context.Context, _ json.RawMessage) (core.ToolResult, error) {
			return core.UIResult("FlightCard", map[string]int{"count": 2}), nil
		})

	provider := &scriptedProvider{responses: []core.ChatResponse{
		{ToolCalls: []core.ToolCall{{ID: "1", Name: "show_flights", Args: json.RawMessage(`{}`)}}},
		{Content: "done"},
	}}

	a := New("ui", "ui agent", provider, WithTools(flightTool))

	ch := make(chan core.StreamEvent, 64)
	if _, err := a.Execute(context.Background(), AgentTask{Input: "flights"}, core.WithStream(ch)); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var events []core.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	resultIdx, uiIdx := -1, -1
	for i, ev := range events {
		switch ev.Type {
		case core.EventToolCallResult:
			if ev.ID == "1" {
				resultIdx = i
			}
		case core.EventUIComponent:
			uiIdx = i
		}
	}
	if uiIdx == -1 {
		t.Fatal("no EventUIComponent emitted")
	}
	if resultIdx == -1 || uiIdx < resultIdx {
		t.Fatalf("EventUIComponent (idx %d) must follow EventToolCallResult (idx %d)", uiIdx, resultIdx)
	}

	ui := events[uiIdx]
	if ui.ID != "1" {
		t.Fatalf("UI event ID = %q, want 1", ui.ID)
	}
	if ui.Name != "FlightCard" {
		t.Fatalf("UI event Name = %q, want FlightCard", ui.Name)
	}
	if string(ui.Object) != `{"count":2}` {
		t.Fatalf("UI event Object = %s, want {\"count\":2}", ui.Object)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEventUIComponent_EmittedAfterToolResult -v`
Expected: FAIL — "no EventUIComponent emitted" (the loop does not emit it yet).

- [ ] **Step 3: Add the emit block**

In `agent/iteration.go`, immediately after the `EventToolCallResult` emit block (the `if ch != nil { select { case ch <- core.StreamEvent{Type: core.EventToolCallResult, ...}: case <-ctx.Done(): } }` ending at line 473), insert:

```go
			// Emit ui-component event when the tool produced a renderable component.
			if ch != nil && results[j].ui != nil {
				select {
				case ch <- core.StreamEvent{
					Type:   core.EventUIComponent,
					ID:     tc.ID,
					Name:   results[j].ui.Name,
					Object: results[j].ui.Props,
				}:
				case <-ctx.Done():
				}
			}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestEventUIComponent_EmittedAfterToolResult -v`
Expected: PASS.

- [ ] **Step 5: Run the whole agent package to catch regressions**

Run: `go test ./agent/ -v`
Expected: PASS (all existing + new tests).

- [ ] **Step 6: Stage (do not commit)**

```bash
git add agent/iteration.go agent/ui_stream_test.go
```

---

## Task 6: Re-export the primitive on the root umbrella

**Files:**
- Modify: `oasis.go` — type aliases (~line 45 area), event const block (~line 148), helper forward (~line 213 area)
- Test: `ui_reexport_test.go` (create, repo root)

- [ ] **Step 1: Write the failing test**

Create `ui_reexport_test.go` at the repo root:

```go
package oasis_test

import (
	"testing"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/core"
)

func TestUIReexports(t *testing.T) {
	if oasis.EventUIComponent != core.EventUIComponent {
		t.Fatal("oasis.EventUIComponent != core.EventUIComponent")
	}

	r := oasis.UIResult("Card", map[string]int{"a": 1})
	if r.UI == nil || r.UI.Name != "Card" {
		t.Fatalf("UIResult.UI = %+v, want Card", r.UI)
	}

	// Type aliases must be usable from the root package.
	var _ oasis.UIComponent
	var _ oasis.UIRenderable
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestUIReexports -v`
Expected: FAIL to compile — `undefined: oasis.EventUIComponent`, `oasis.UIResult`, `oasis.UIComponent`, `oasis.UIRenderable`.

- [ ] **Step 3: Add type aliases**

In `oasis.go`, next to `type ToolResult = core.ToolResult` (line 45), add:

```go
type UIComponent = core.UIComponent
type UIRenderable = core.UIRenderable
```

- [ ] **Step 4: Add the event constant**

In `oasis.go`, in the const block that defines the event re-exports (where `EventToolCallResult = core.EventToolCallResult` is, line 148), add:

```go
	EventUIComponent = core.EventUIComponent
```

- [ ] **Step 5: Add the `UIResult` forwarding function**

In `oasis.go`, next to `func Erase[In, Out any](...)` (line 213), add:

```go
// UIResult re-exports core.UIResult: build a ToolResult that renders as the
// named frontend component.
func UIResult[T any](name string, props T) core.ToolResult { return core.UIResult(name, props) }
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test . -run TestUIReexports -v`
Expected: PASS.

- [ ] **Step 7: Stage (do not commit)**

```bash
git add oasis.go ui_reexport_test.go
```

---

## Task 7: Docs + CHANGELOG

**Files:**
- Modify: `docs/external/agent/api.md` (the StreamEvent type table — `EventToolCallResult` row is at line 143)
- Modify: `docs/external/tools/examples.md` (append a generative-UI recipe)
- Modify: `CHANGELOG.md` (the `## [Unreleased]` → `### Added` section)

- [ ] **Step 1: Add the event to the agent event table**

In `docs/external/agent/api.md`, find the StreamEvent table row for `EventToolCallResult` (line 143) and add a new row directly below it:

```markdown
| `EventUIComponent` | Tool produced a renderable component; `Name` carries the component name, `Object` the props JSON, `ID` correlates to the tool call |
```

- [ ] **Step 2: Add a generative-UI recipe**

Append to `docs/external/tools/examples.md`:

```markdown
## Generative UI: render a component instead of text

A tool can return a UI component descriptor instead of plain text. The agent
emits an `EventUIComponent` after the tool result; a frontend maps the
component `Name` to a renderer and validates `Props`.

```go
// Helper path — for func/RawTool/hand-rolled AnyTool:
func searchFlights(ctx context.Context, in FlightQuery) (core.ToolResult, error) {
	return core.UIResult("FlightCard", lookup(in)), nil
}

// Interface path — a typed Tool[In, Out] whose Out opts in:
type FlightResults struct {
	Flights []Flight `json:"flights"`
}

func (FlightResults) UIComponent() string { return "FlightCard" } // implements core.UIRenderable
// Erase detects UIRenderable and sets ToolResult.UI automatically.
```

On the wire the agent emits, in order: `EventToolCallResult` then
`EventUIComponent{ID: <call id>, Name: "FlightCard", Object: <props json>}`.
```

- [ ] **Step 3: Add the CHANGELOG entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added`, add as the first bullet:

```markdown
- **Generative UI primitive.** A tool can mark its output as a renderable
  frontend component: `core.UIResult(name, props)` (helper) or a typed tool's
  `Out` implementing `core.UIRenderable`. The agent emits a new
  `core.EventUIComponent` stream event (carrying the component name in `Name`
  and props JSON in `Object`) directly after `EventToolCallResult`. Re-exported
  on the root umbrella as `oasis.UIComponent`, `oasis.UIRenderable`,
  `oasis.UIResult`, and `oasis.EventUIComponent`. Core loop event emission is
  unchanged; consumers opt in by handling the new event. Zero new dependencies.
```

- [ ] **Step 4: Stage (do not commit)**

```bash
git add docs/external/agent/api.md docs/external/tools/examples.md CHANGELOG.md
```

---

## Task 8: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Build the whole module**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 2: Run the full test suite**

Run: `go test ./...`
Expected: all packages PASS (`ok` / `no test files`), no `FAIL`.

- [ ] **Step 3: Run the linter (enforces depguard + style)**

Run: `golangci-lint run ./...`
Expected: no findings. (If `golangci-lint` is not installed, note it and skip — do not treat as a failure.)

- [ ] **Step 4: Confirm the working tree is staged but uncommitted**

Run: `git status --short`
Expected: the touched files appear with `A`/`M` in the staged column; nothing committed. Leave it for the user to review and commit.

---

## Notes for the implementer

- **No commits.** The project owner batches and commits changes themselves. Stage with `git add`; never run `git commit`.
- **TDD order matters.** Tasks 1→6 build on each other's types; run each task's test at the "fail" step to confirm you're testing the right thing before implementing.
- **Sandbox caveat.** If `git` or other commands fail with an `apply-seccomp ... /proc/self/setgroups ... Permission denied` error, that is the command sandbox, not your change — rerun the command with the sandbox disabled.
- **Out of scope (do NOT build):** AG-UI satellite, A2A `EventUIComponent` translation, declarative UI trees, an LLM-driven `render_ui` tool, and the Athena consumer wiring. The Athena side is a separate plan in a separate repo.
```