# Generative UI — Native Primitive (Oasis side)

- **Date:** 2026-06-12
- **Status:** Approved design, pre-implementation
- **Scope:** Oasis framework primitive + the consuming changes in `athena-new`
- **Author:** brainstormed with nevindra

## 1. Goal

Give Oasis a first-class, typed contract for "this tool output **is** a UI
component — render it as `X` with these props" so an agent can stream rich,
app-rendered components (charts, cards, tables, forms) instead of only text.

Today an Oasis tool can already shuttle arbitrary JSON to a frontend
(`ToolResult.Content` holds JSON; the frontend parses it). That works but is an
*ad-hoc convention* every app reinvents. This design replaces the convention
with a blessed primitive so the contract is consistent across in-process
consumers, A2A, and any future AG-UI adapter — without touching the agent
loop's event emission.

## 2. Non-goals (deliberate YAGNI)

These are explicitly **out of scope** for the first cut. Each can be layered on
top of the primitive later without rework:

- **Full AG-UI protocol** (a satellite mirroring `a2a/`). The consumer is
  Athena with a custom UI and **no CopilotKit**, so AG-UI's interop payoff is
  not realized now. The primitive is shaped to make a future `agui/` adapter
  cheap (see §8), but we do not build it.
- **Declarative UI trees** (A2UI-style node trees). Only named-component +
  props for now.
- **Bidirectional shared-state sync** (AG-UI `StateSnapshot`/`StateDelta`).
- **Frontend-tools / HITL round-trip** (Oasis already has suspend/resume if
  this is ever wanted).
- **LLM-driven `render_ui(component, props)` meta-tool.** This is just an
  ordinary tool built on the primitive; ship it later if a use case appears.

## 3. Core concept

Generative UI = **tool-call rendering**, the convergent industry pattern
(AG-UI, CopilotKit, Vercel AI SDK). The *component is chosen by which tool runs*
plus the *typed props it returns*. The LLM only decides which tool to call; the
**frontend** owns the catalog of renderers, keyed by component name. The
framework's job is to carry a `{name, props}` descriptor through the existing
stream with a clean discriminator.

## 4. Producer API (Oasis)

### 4.1 New type + field

`core/types.go` — a new struct, and one optional field on `ToolResult`
(currently `core/types.go:96`, `{Content, Error, Attachments}`):

```go
// UIComponent describes a frontend component to render in place of (or
// alongside) a tool result's text. Name is a registry key the frontend
// resolves to a renderer; Props is the typed payload the renderer receives.
type UIComponent struct {
    Name  string          `json:"name"`
    Props json.RawMessage `json:"props"`
}

type ToolResult struct {
    Content     string          `json:"content,omitempty"`
    Error       string          `json:"error,omitempty"`
    Attachments []Attachment    `json:"attachments,omitempty"`
    UI          *UIComponent    `json:"ui,omitempty"` // NEW — nil = plain result
}
```

Adding a field to an existing struct is non-breaking. `UI` is a pointer so the
zero value (`nil`) keeps every existing tool unchanged.

### 4.2 Two authoring paths

**(a) Helper, for `Func`/`RawTool`/hand-rolled `AnyTool`** —
`core/tool_helpers.go`, next to `JSONResult`:

```go
// UIResult builds a ToolResult that renders as the named component. props is
// marshaled to JSON for both UI.Props and Content (so the LLM still "sees" the
// data it rendered and the loop can continue with context). Panics on marshal
// failure — a programming error, matching JSONResult's convention.
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

Usage:

```go
func searchFlights(ctx context.Context, in FlightQuery) (core.ToolResult, error) {
    flights := lookup(in)
    return core.UIResult("FlightCard", flights), nil
}
```

**(b) Interface, for typed `Tool[In, Out]`** — an optional capability on the
`Out` value, mirroring the existing `OutSchemaProvider` pattern
(`core/erase.go:109`):

```go
// core/tool.go — next to the other optional-capability interfaces.
// A tool's Out type opts into UI rendering by implementing UIRenderable.
type UIRenderable interface {
    UIComponent() string // returns the component name; props = the Out value
}
```

`Erase`'s `ExecuteRaw` already marshals `out` to JSON for `Content`
(`core/erase.go:49-53`). Extend it (and the streaming twin at
`core/erase.go:98-102`) to also set `UI` when `Out` implements the interface:

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

```go
type FlightResults struct{ Flights []Flight `json:"flights"` }
func (FlightResults) UIComponent() string { return "FlightCard" }
// A Tool[FlightQuery, FlightResults] now renders automatically — no helper call.
```

## 5. Stream event (Oasis)

**No new `StreamEvent` fields.** The struct already carries `ID`, `Name`, and
`Object json.RawMessage` (used today by `EventObjectDelta/Finish`). Reuse them.
Add one constant in `core/stream.go` and register it in
`AllStreamEventTypes()` (`core/stream.go:137`):

```go
// EventUIComponent signals a tool produced a renderable UI component.
// ID correlates with the preceding EventToolCallStart/Result; Name carries
// the component name; Object carries the props JSON. Always emitted directly
// after the tool's EventToolCallResult.
EventUIComponent StreamEventType = "ui-component"
```

### 5.1 Threading the descriptor to the emit site

`results[j]` at the emit site (`agent/iteration.go:461`) is the internal
`toolExecResult` (`agent/dispatch.go:121`), not a `ToolResult`. The `UI`
descriptor must ride along the same path the existing fields take:

1. `core.ToolResult.UI` — set by §4.
2. `DispatchResult` (defined in `internal/runtime`, re-exported via
   `agent/agent.go`) — add `UI *core.UIComponent`.
3. `toolResultToDispatch` (`agent/dispatch.go:19-27`) — copy `result.UI` into
   the `DispatchResult` on the success branch (alongside `Attachments`).
4. `toolExecResult` (`agent/dispatch.go:121`) — add `ui *core.UIComponent`,
   populated from `DispatchResult.UI` wherever the parallel collector builds it.
5. `agent/iteration.go` — immediately after the `EventToolCallResult` send
   (`:461-473`), emit when present:

```go
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

UI is only set on the success path, so error results never emit a UI event.

## 6. Re-exports (`oasis.go`)

Surface the primitive on the curated root — this is a headline feature, not a
niche API:

- `type UIComponent = core.UIComponent`
- `type UIRenderable = core.UIRenderable`
- `func UIResult[T any](name string, props T) core.ToolResult` (thin forward)
- `const EventUIComponent = core.EventUIComponent` (next to the other event
  constants, `oasis.go:148`)

`ToolResult` is already aliased (`oasis.go:45`) so the new field is free.

## 7. Consumer: athena-new

The only end-to-end work outside Oasis. All additive — mirrors the existing
`tool_call` / `attachment` block handling.

**Backend**
- `internal/adapter/adapter.go` — add `EventUIComponent = "ui_component"` to the
  event-type enum.
- `internal/adapter/oasis.go` (`translateEvent`) — add a case mapping
  `oasis.EventUIComponent` → wire event
  `{type:"ui_component", payload:{id, name, props}}`. The SSE hub and wire
  encoder need no change (payload is already `json.RawMessage`).

**Frontend** (`ui/`)
- `ui/src/features/chat/turns.ts` (`streamingEventsToAgentTurn`) — recognize
  `ui_component` and emit a new `ui_component` block (correlate by `id` like
  tool calls).
- `ui/src/features/chat/components/turns/agent-turn.tsx` — render the block via
  a `componentRegistry[name]` lookup → React component, validating `props`
  (zod optional). Unknown name → graceful fallback (render the raw JSON /
  "unsupported component"), never throw.
- The **component registry** is an app-owned map (`{ FlightCard: <Comp/>, … }`),
  exactly the pattern already used for `ToolCallBlock` / `PlatformToolCard`.

## 8. Future consumers (not built now, kept cheap)

- **A2A** (`a2a/server_stream.go:330`, `translateEvent`, currently text-only):
  add a case `EventUIComponent` → an `Artifact` `Part{Data: props}` with the
  component name in `Part`/`Artifact` metadata. Deferred until A2A needs UI.
- **AG-UI adapter** (a future `agui/` satellite): `EventUIComponent` →
  AG-UI `Custom` event (`{name, value}`) or a tool-render event. Because the
  primitive already models UI as tool-call rendering, this translation is
  mechanical — this is the "door left open" without paying for it now.

## 9. Error handling

- `UIResult` panics on marshal failure (programming error; matches
  `JSONResult`). The interface path returns a normal marshal `Error` result.
- Frontend treats an unknown component name as a render fallback, not an error.
- No framework-level validation of component names — the frontend owns the
  catalog. The tool's `OutputSchema` (already derived from `Out`) documents the
  prop shape for anyone who wants to validate.

## 10. Versioning

Minor bump (v0.x). The change introduces new exported types
(`UIComponent`, `UIRenderable`), a new function (`UIResult`), a new event
constant, and a new struct field — none of which a patch release may carry per
the repo's versioning rule. No breaking changes to existing APIs.

## 11. Testing

**Oasis**
- A `Func` tool returning `UIResult(...)` produces, in order,
  `EventToolCallResult` then `EventUIComponent` with the correct `ID`, `Name`,
  and `Object`.
- A typed `Tool[In, Out]` whose `Out` implements `UIRenderable` auto-sets
  `ToolResult.UI` through `Erase` (and `EraseStreaming`).
- An error result emits **no** `EventUIComponent`.
- `EventUIComponent` is present in `AllStreamEventTypes()` (exhaustiveness
  test).
- `UIResult` sets both `Content` and `UI.Props` to the same JSON.

**Athena**
- `translateEvent` maps `EventUIComponent` to the `ui_component` wire event.
- Frontend: `streamingEventsToAgentTurn` produces a `ui_component` block;
  registry hit renders the component; registry miss renders the fallback.

## 12. File touch list (Oasis)

| File | Change |
|------|--------|
| `core/types.go:96` | add `UIComponent` struct; add `UI *UIComponent` field to `ToolResult` |
| `core/tool.go` | add `UIRenderable` interface |
| `core/tool_helpers.go` | add `UIResult[T]` helper |
| `core/stream.go:13`, `:137` | add `EventUIComponent` const; register in `AllStreamEventTypes()` |
| `core/erase.go:49`, `:98` | set `ToolResult.UI` when `Out` implements `UIRenderable` |
| `internal/runtime` (`DispatchResult`) | add `UI *core.UIComponent` |
| `agent/dispatch.go:19`, `:121` | copy UI in `toolResultToDispatch`; add `ui` to `toolExecResult` + populate |
| `agent/iteration.go:461` | emit `EventUIComponent` after `EventToolCallResult` |
| `oasis.go:45`, `:148`, `:213` | re-export `UIComponent`, `UIRenderable`, `UIResult`, `EventUIComponent` |

## 13. Decisions locked

1. **Separate `EventUIComponent`** rather than overloading
   `EventToolCallResult` — gives the frontend a clean discriminator and lets
   non-tool emitters (a processor, a future `render_ui`) reuse it.
2. **UI as a `ToolResult` field** (per-call data) rather than a tool-level
   interface — UI is data, not a static capability. The `UIRenderable`
   interface is only the ergonomic on-ramp for typed tools.
3. **Tool author chooses the component** as the foundation; an LLM-driven
   `render_ui` tool is a later add-on over the same primitive.
4. **No new `StreamEvent` fields** — reuse `ID`/`Name`/`Object`.
