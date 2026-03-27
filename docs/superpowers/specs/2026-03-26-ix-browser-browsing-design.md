# IX Browser Browsing via Pinchtab

**Date:** 2026-03-26
**Status:** Approved
**Scope:** Minimal browser browsing + PDF export for IX sandbox

## Summary

Add browser browsing capabilities to the IX sandbox using [Pinchtab](https://github.com/pinchtab/pinchtab) bridge mode. Each IX container optionally embeds a Pinchtab bridge subprocess + headless Chrome, enabling AI agents to navigate the web, extract structured content via accessibility-tree snapshots, extract readable text for RAG, take screenshots, export PDFs, and interact with page elements using semantic refs instead of pixel coordinates.

## Motivation

The current browser interface (`BrowserNavigate`, `BrowserScreenshot`, `BrowserAction`) uses pixel-coordinate-based interaction. This requires the agent to take a screenshot, use a vision model to locate elements, then send X/Y coordinates — expensive and fragile. Pinchtab's accessibility-tree approach returns semantic element refs (`e0`, `e1`, ...) and clean text extraction, reducing token costs by 5-13x per page.

## Architecture

**Approach:** Subprocess + HTTP Proxy (Approach A)

```
ix daemon (:8080)
  |-- receives /v1/browser/* requests from IXSandbox client
  |-- proxies to pinchtab bridge (:9867) on localhost
  +-- pinchtab bridge manages headless Chrome
```

**Container model:** One Pinchtab bridge + one Chrome process per IX container. Bridge mode (not server mode) — no multi-instance management needed since IX Manager handles container lifecycle.

**Docker image:** Separate image `oasis-ix-browser:latest` extending `oasis-ix:latest`.

```dockerfile
FROM oasis-ix:latest
RUN apt-get update && apt-get install -y chromium --no-install-recommends \
    && rm -rf /var/lib/apt/lists/*
COPY --from=pinchtab/pinchtab:latest /usr/local/bin/pinchtab /usr/local/bin/pinchtab
```

Users who don't need browser capabilities use the lean base image. Users who do specify `oasis-ix-browser:latest` in `CreateOpts.Image`.

## Sandbox Interface Changes

### New Methods

```go
// BrowserSnapshot returns the accessibility tree of the current page.
// Each interactive element has a Ref (e.g., "e0", "e5") that can be
// passed to BrowserAction for precise interaction without coordinates.
BrowserSnapshot(ctx context.Context, opts SnapshotOpts) (BrowserSnapshot, error)

// BrowserText extracts readable text content from the current page.
// Uses readability-style extraction by default; set Raw to true for innerText.
BrowserText(ctx context.Context, opts TextOpts) (BrowserTextResult, error)

// BrowserPDF exports the current page as a PDF document.
BrowserPDF(ctx context.Context) ([]byte, error)
```

### New Types

```go
type SnapshotOpts struct {
    Filter   string // "interactive" filters to actionable elements only
    Selector string // CSS selector to scope snapshot to a subtree
    Depth    int    // traversal depth; 0 = unlimited
}

type BrowserSnapshot struct {
    URL   string         // current page URL
    Title string         // page title
    Nodes []SnapshotNode // accessibility tree nodes
}

type SnapshotNode struct {
    Ref  string // element reference (e.g., "e0") — use in BrowserAction.Ref
    Role string // semantic role (link, button, textbox, heading, etc.)
    Name string // accessible name / visible text
}

type TextOpts struct {
    Raw      bool // true = innerText, false = readability extraction
    MaxChars int  // 0 = unlimited
}

type BrowserTextResult struct {
    URL       string
    Title     string
    Text      string
    Truncated bool
}
```

### Enhanced BrowserAction

```go
type BrowserAction struct {
    Type string // "click", "type", "scroll", "navigate", "key", "hover", "fill", "press", "select"
    Ref  string // element ref from snapshot (preferred)
    X    int    // pixel coordinates (fallback for canvas/maps)
    Y    int
    Text string // text for type/fill, URL for navigate
    Key  string // key name for key/press
}
```

`Ref` is the preferred interaction method — agents get refs from `BrowserSnapshot` and pass them to `BrowserAction`. X/Y coordinates remain for non-accessible elements (canvas, WebGL, image maps).

## Two-Layer Implementation

The browser feature spans two layers:

1. **IXSandbox client** (`sandbox/ix/`) — Go code running alongside the agent, makes HTTP calls to the ix daemon
2. **ix daemon** (`internal/ixd/`) — HTTP server inside the Docker container, proxies browser requests to Pinchtab

Currently, the ix daemon has **no browser endpoints** — the existing client methods (`BrowserNavigate`, `BrowserAction`, `BrowserScreenshot` in `sandbox/ix/sandbox.go`) call endpoints that don't exist server-side yet. Both layers are built from scratch.

### ix Daemon: New Browser Proxy Endpoints

New file `internal/ixd/browser.go` — all handlers proxy to Pinchtab at `localhost:9867`:

| ix daemon endpoint | Pinchtab endpoint | Purpose |
|---|---|---|
| `POST /v1/browser/navigate` | `POST /navigate` | Navigate to URL |
| `POST /v1/browser/action` | `POST /action` | Click, type, fill by ref or coords |
| `GET /v1/browser/screenshot` | `GET /screenshot?raw=true` | PNG capture |
| `GET /v1/browser/snapshot` | `GET /snapshot` | Accessibility tree |
| `GET /v1/browser/text` | `GET /text` | Readable text extraction |
| `GET /v1/browser/pdf` | `GET /pdf` | PDF export |

The ix daemon also manages Pinchtab's lifecycle (subprocess spawn, health, restart).

### IXSandbox Client: Updated Methods

```go
// New methods — call new ix daemon endpoints
func (s *IXSandbox) BrowserSnapshot(ctx context.Context, opts sandbox.SnapshotOpts) (sandbox.BrowserSnapshot, error)
func (s *IXSandbox) BrowserText(ctx context.Context, opts sandbox.TextOpts) (sandbox.BrowserTextResult, error)
func (s *IXSandbox) BrowserPDF(ctx context.Context) ([]byte, error)

// Existing methods — repointed to new ix daemon endpoints
// BrowserNavigate: /v1/browser/actions → /v1/browser/navigate
// BrowserAction:   /v1/browser/actions → /v1/browser/action
// BrowserScreenshot: /v1/browser/screenshot (unchanged path, now actually implemented)
```

### Browser Availability

ix daemon checks for `pinchtab` binary in PATH at startup:
- Present: spawn `pinchtab bridge --port 9867 --headless --idpi-allow="*"`, poll `/health` until ready, set `browserAvailable = true`
- Absent: set `browserAvailable = false`, browser endpoints return HTTP 501 with `"browser not available: use oasis-ix-browser image"`

## Tools

### New Tools (3)

**`snapshot`** — Get accessibility tree with element refs.
```json
{
    "filter": "string — 'interactive' to show only actionable elements",
    "selector": "string — CSS selector to scope",
    "depth": "integer — tree depth limit"
}
```

**`page_text`** — Extract readable text content for RAG.
```json
{
    "raw": "boolean — true for innerText, false for readability extraction",
    "max_chars": "integer — truncation limit"
}
```

**`export_pdf`** — Export current page as PDF. No parameters.

### Enhanced Existing Tool

**`browser`** — Gains `ref` parameter and new action types (`hover`, `fill`, `press`, `select`).
```json
{
    "action": "string",
    "ref": "string — element ref from snapshot",
    "url": "string",
    "x": "integer",
    "y": "integer",
    "text": "string",
    "key": "string"
}
```

### Unchanged

**`screenshot`** — Unchanged, still captures PNG.

### Total Tool Count

13 tools (14 with `deliver_file`). Up from 10 (11 with `deliver_file`).

## Docker & Container Changes

### Separate Image

`oasis-ix-browser:latest` extends `oasis-ix:latest` with Chromium and Pinchtab binary.

### Security Adjustments

When ix Manager creates a container from the browser image:
- `--shm-size=2g` — Chrome needs shared memory for rendering
- Relax seccomp profile for Chrome's sandbox
- Default memory: 3GB (up from 2GB, configurable via `ResourceSpec.Memory`)

### Detection

ix Manager detects browser image by checking ix daemon's `/health` response which includes `"browser": true/false` based on whether Pinchtab was found and started.

### IDPI

Pinchtab defaults to local-only browsing. ix daemon starts Pinchtab with IDPI disabled for general web access: `pinchtab bridge --port 9867 --headless --idpi-allow="*"`.

## Pinchtab Process Lifecycle

```
ix daemon starts
  +-- check: is `pinchtab` in PATH?
      |-- yes: spawn `pinchtab bridge --port 9867 --headless --idpi-allow="*"`
      |         poll GET localhost:9867/health (max 10s, exponential backoff)
      |         set browserAvailable = true
      +-- no:  set browserAvailable = false
               browser methods return "browser not available" error

ix daemon shutdown
  +-- SIGTERM to pinchtab process
      +-- Chrome dies with it
```

### Crash Recovery

- ix daemon detects Pinchtab failure via failed HTTP call
- Auto-restart subprocess (max 3 attempts)
- Browser state (open tabs, navigation history) lost on restart — agent re-navigates
- Same pattern as existing health monitor in `health.go`

## Error Handling

| Scenario | Behavior |
|---|---|
| Browser not available (base image) | `ToolResult{Error: "browser not available: use oasis-ix-browser image"}` |
| Pinchtab crash | Auto-restart (max 3), return error if exhausted |
| Chrome OOM | Pinchtab detects crash, ix daemon restarts Pinchtab |
| Navigation timeout | Pass-through Pinchtab's timeout parameter, default 30s |
| Large page content | `MaxChars` (text), `Depth` (snapshot), `maxDeliverFileBytes` (PDF) |

All errors go through `ToolResult.Error`, not Go `error` — per engineering guidelines.

## Testing

### Unit Tests (sandbox/ix/)

- Mock Pinchtab HTTP responses for each new client method
- Test `BrowserAction` with `Ref` field mapping to Pinchtab's `{"kind": "...", "ref": "e5"}`
- Test error paths: not available, crash, timeout, malformed responses
- Test Pinchtab process lifecycle: spawn, health poll, restart

### Tool Tests (sandbox/)

- Test new tools (`snapshot`, `page_text`, `export_pdf`) via mock Sandbox
- Test enhanced `browser` tool with `ref` parameter
- Test `export_pdf` integration with `FileDelivery`

### Integration Tests (sandbox/ix/)

- Require Docker + `oasis-ix-browser:latest` image
- Full flow: create sandbox, navigate, snapshot, click ref, text, pdf, screenshot, destroy
- Build tag: `//go:build integration`
- Extend existing `integration_test.go`

## Files Changed

### Sandbox Interface & Client (Go framework)

| File | Change |
|---|---|
| `sandbox/sandbox.go` | Add 3 methods to interface, 4 new types, enhance `BrowserAction` |
| `sandbox/ix/sandbox.go` | Implement 3 new methods, repoint 3 existing methods |
| `sandbox/tools.go` | Add 3 new tools, enhance `browser` tool with `ref` |
| `sandbox/ix/manager.go` | Adjust container security for browser image (shm, seccomp, memory) |
| `sandbox/ix/sandbox_test.go` | Unit tests for new/changed client methods |
| `sandbox/tools_test.go` | Tool tests for new/enhanced tools |
| `sandbox/ix/integration_test.go` | End-to-end browser flow test |

### ix Daemon (server inside container)

| File | Change |
|---|---|
| `internal/ixd/server.go` | Register new `/v1/browser/*` routes, health response gains `"browser"` field |
| `internal/ixd/browser.go` (new) | Browser proxy handlers — forwards requests to Pinchtab |
| `internal/ixd/pinchtab.go` (new) | Pinchtab subprocess lifecycle: spawn, health poll, crash recovery |

### Docker & Docs

| File | Change |
|---|---|
| `cmd/ix/Dockerfile.browser` (new) | `oasis-ix-browser:latest` image extending base |
| `docs/concepts/code-execution.md` | Update tool table, add browser section |
