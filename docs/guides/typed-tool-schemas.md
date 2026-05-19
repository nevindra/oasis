# Typed Tool Schemas (Phase 1.5)

In v0.x.x (Phase 1.5), `Tool[In, Out]` derives its JSON Schema from the
input type `In` by reflection. Authors write a typed Go struct and a
short `Definition() ToolMeta`; the schema shown to the LLM is computed
at registration time.

## Before vs. after

### Before (Phase 1)

```go
type FetchInput struct {
    URL string `json:"url"`
}

type Tool struct {
    client *http.Client
}

func (t *Tool) Name() string { return "http_fetch" }

func (t *Tool) Definition() oasis.ToolDefinition {
    return oasis.ToolDefinition{
        Name:        "http_fetch",
        Description: "Fetch a URL and extract its readable text content.",
        Parameters: json.RawMessage(`{
            "type":"object",
            "properties":{"url":{"type":"string","description":"URL to fetch"}},
            "required":["url"]
        }`),
    }
}

func (t *Tool) Execute(ctx context.Context, in FetchInput) (string, error) {
    // ...
}
```

### After (Phase 1.5)

```go
type FetchInput struct {
    URL string `json:"url" describe:"URL to fetch"`
}

type Tool struct {
    client *http.Client
}

func (t *Tool) Definition() oasis.ToolMeta {
    return oasis.ToolMeta{
        Name:        "http_fetch",
        Description: "Fetch a URL and extract its readable text content.",
    }
}

func (t *Tool) Execute(ctx context.Context, in FetchInput) (string, error) {
    // ...
}
```

Changes:
- `Name()` method deleted (the name lives in `ToolMeta.Name`).
- `Definition()` returns `oasis.ToolMeta` (name + description only).
- The `Parameters: json.RawMessage(...)` block is deleted.
- `describe:"URL to fetch"` tag moved onto the struct field — same
  information, now next to the Go type it describes.

## Supported types

| Go type | JSON Schema |
|---|---|
| `bool` | `{"type":"boolean"}` |
| `int`, `int8`–`int64`, `uint`, `uint8`–`uint64` | `{"type":"integer"}` |
| `float32`, `float64` | `{"type":"number"}` |
| `string` | `{"type":"string"}` |
| `[]T` | `{"type":"array","items":<schema of T>}` |
| `[]byte` | `{"type":"string"}` (base64 on the wire) |
| `map[string]T` | `{"type":"object","additionalProperties":<schema of T>}` |
| `struct` | `{"type":"object","properties":{...},"required":[...]}` |
| `*T` (struct field) | schema of T; field NOT in `required` |
| `any` / `interface{}` | `{}` |
| `json.RawMessage` | `{}` |
| `time.Time` | `{"type":"string","format":"date-time"}` |

A field is **optional** (excluded from `required`) when its type is `*T`
or its `json` tag contains `,omitempty`.

## Struct tags

The reflector recognises three tags:

- `json:"name,omitempty"` — stdlib; honored for both naming and optionality.
- `describe:"..."` — free-text description shown to the LLM.
- `enum:"a,b,c"` — string-only enumeration (comma-separated).

## Escape hatch: `SchemaProvider`

When reflection cannot express what a tool needs (`oneOf`, conditional
`required`, recursive shapes, provider-specific features), implement
`SchemaProvider` on the input type:

```go
type ExoticIn struct {
    Mode string          `json:"mode"`
    Args json.RawMessage `json:"args"`
}

func (ExoticIn) JSONSchema() json.RawMessage {
    return json.RawMessage(`{"oneOf":[...]}`)
}
```

The reflector detects the method via a type assertion at `Erase()` time
and uses the override verbatim.

## Errors are loud

If a tool's input contains an unsupported type (`complex64`, `chan`,
`func`, non-string map key, recursion without `SchemaProvider`), the
reflector **panics** at `Erase[In, Out]()` registration time with a
descriptive message:

```
panic: oasis.DeriveSchema: field 'Limit' has unsupported type complex64
(kind=complex64). Supported kinds: bool, int*, uint*, float*, string,
[]T, map[string]T, struct, *T, any, time.Time, json.RawMessage. For
unsupported shapes (oneOf, conditional required, recursion,
provider-specific features), implement SchemaProvider on the input type.
```

Failures at startup beat failures during an LLM call.
