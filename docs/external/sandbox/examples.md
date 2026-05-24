# Sandbox Examples

All examples use `github.com/nevindra/oasis/sandbox` and an implementation package.
Replace `ix` with your actual implementation import.

---

## Recipe 1: Wire a sandbox to an agent (minimal)

**Goal:** Provision a container and give an agent shell + file + browser access in
the fewest lines possible.

```go
package main

import (
    "context"
    "log"

    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/sandbox"
    ix    "github.com/nevindra/oasis-sandbox-ix"
)

func main() {
    ctx := context.Background()

    mgr := ix.NewManager(ix.DefaultConfig())
    defer mgr.Close()

    sb, err := mgr.Create(ctx, sandbox.CreateOpts{SessionID: "demo-1"})
    if err != nil {
        log.Fatal(err)
    }
    defer sb.Close()

    agent := oasis.NewLLMAgent(
        "assistant", "You are a helpful coding assistant.",
        provider,
        oasis.WithSandbox(sb, sandbox.Tools(sb)...),
    )

    result, err := agent.Execute(ctx, oasis.AgentTask{
        Input: "Create a file called hello.py that prints 'Hello, World!' and run it.",
    })
    if err != nil {
        log.Fatal(err)
    }
    log.Println(result.Output)
}
```

**Plain-English walkthrough:**

- `ix.NewManager` creates the container runtime. One manager per process is typical.
- `mgr.Create` boots a container for this session and blocks until it's healthy.
- `sandbox.Tools(sb)` produces all 19 agent-callable tools. Spreading them with
  `...` into `WithSandbox` registers each one individually.
- `WithSandbox` tells the agent both "here is the sandbox reference" and "here are
  the tools to offer the LLM". The agent will include them in its tool list on every
  API call.

**Variations:**

- Set `SessionID` to your conversation ID so you can retrieve the sandbox later with
  `mgr.Get(sessionID)`.
- Pass `sandbox.CreateOpts{Image: "myorg/myimage:latest"}` to use a custom container.
- Set `TTL: 30 * time.Minute` to auto-expire idle sandboxes.

---

## Recipe 2: Persist sandbox output to S3 (file mounts)

**Goal:** Any file the agent writes to `/workspace/outputs/` is automatically
uploaded to S3 when the session ends.

```go
type s3Bucket struct { /* your S3 client */ }

func (b *s3Bucket) List(ctx context.Context, prefix string) ([]sandbox.MountEntry, error) { /* ... */ }
func (b *s3Bucket) Open(ctx context.Context, key string) (io.ReadCloser, error)            { /* ... */ }
func (b *s3Bucket) Put(ctx context.Context, key, mimeType string, size int64, data io.Reader, ifVersion string) (string, error) { /* ... */ }
func (b *s3Bucket) Delete(ctx context.Context, key string, ifVersion string) error         { /* ... */ }
func (b *s3Bucket) Stat(ctx context.Context, key string) (sandbox.MountEntry, error)       { /* ... */ }

func runSession(ctx context.Context, mgr sandbox.Manager) error {
    sb, err := mgr.Create(ctx, sandbox.CreateOpts{SessionID: "report-42"})
    if err != nil {
        return err
    }
    defer sb.Close()

    manifest := sandbox.NewManifest()
    mounts := []sandbox.MountSpec{{
        Path:         "/workspace/outputs",
        Backend:      &s3Bucket{},
        Mode:         sandbox.MountWriteOnly,
        FlushOnClose: true,
    }}

    tools := sandbox.Tools(sb, sandbox.WithMounts(mounts, manifest))

    agent := oasis.NewLLMAgent("analyst", "You are a data analyst.",
        provider,
        oasis.WithSandbox(sb, tools...),
    )

    _, err = agent.Execute(ctx, oasis.AgentTask{
        Input: "Analyse the CSV at /workspace/inputs/sales.csv and save a summary report to /workspace/outputs/report.md",
    })
    if err != nil {
        return err
    }

    // Flush any writes that happened through channels other than file_write/file_edit.
    return sandbox.FlushMounts(ctx, sb, mounts, manifest)
}
```

**Plain-English walkthrough:**

- `s3Bucket` implements `sandbox.FilesystemMount` — five methods that read/write keys
  in an S3 bucket. You write this once and reuse it across sessions.
- `MountWriteOnly` means files flow one direction: sandbox → S3. No prefetch.
- `FlushOnClose: true` means `FlushMounts` (called at the end) will scan
  `/workspace/outputs/` and upload any file the agent created there.
- Tool interception handles `file_write` and `file_edit` calls automatically —
  those publish immediately. `FlushMounts` catches anything written via `shell`.
- `manifest` is the shared version tracker that prevents overwriting a newer
  backend file with an older sandbox copy.

**Variations:**

- Use `MountReadWrite` with `PrefetchOnStart: true` to also copy files from S3 into
  the sandbox at the start of a session (bidirectional sync).
- Set `MirrorDeletes: true` to delete S3 files that the agent removes locally.
- Add `Include: []string{"*.md", "*.csv"}` to limit which files are synced.

---

## Recipe 3: Pre-load input files into the sandbox

**Goal:** Seed the sandbox with documents from S3 before the agent starts, so it can
read them directly at `/workspace/inputs/`.

```go
manifest := sandbox.NewManifest()
mounts := []sandbox.MountSpec{{
    Path:            "/workspace/inputs",
    Backend:         &s3Bucket{bucket: "my-docs"},
    Mode:            sandbox.MountReadOnly,
    PrefetchOnStart: true,
    Include:         []string{"*.pdf", "*.txt"},
}}

sb, err := mgr.Create(ctx, sandbox.CreateOpts{SessionID: "qa-session"})
if err != nil {
    return err
}
defer sb.Close()

// Copy all matching backend files into the container before the agent starts.
if err := sandbox.PrefetchMounts(ctx, sb, mounts, manifest); err != nil {
    return err
}

tools := sandbox.Tools(sb, sandbox.WithMounts(mounts, manifest))

agent := oasis.NewLLMAgent("qa", "You answer questions from documents.",
    provider,
    oasis.WithSandbox(sb, tools...),
)

result, _ := agent.Execute(ctx, oasis.AgentTask{
    Input: "Summarise the key findings from all the PDFs in /workspace/inputs/",
})
```

**Plain-English walkthrough:**

- `MountReadOnly` + `PrefetchOnStart: true` means: at start, download every key
  matching `*.pdf` or `*.txt` and write it to `/workspace/inputs/<key>` inside
  the container.
- `PrefetchMounts` does the actual download. Call it after `Create` but before
  the agent runs.
- Because the mode is `ReadOnly`, tool writes to this path stay local — they do not
  propagate back to S3. That is intentional: input files should not be overwritten.
- `manifest` records the version of each prefetched file for optimistic concurrency
  on any future read-write scenario.

**Variations:**

- Combine with a separate `MountWriteOnly` mount at `/workspace/outputs/` for a
  clean input-only / output-only split.

---

## Recipe 4: Browser-driven web research

**Goal:** Have the agent navigate a website, extract text, and summarise it — using
the browser when plain HTTP is blocked.

```go
agent := oasis.NewLLMAgent("researcher", `You are a web research assistant.
When http_fetch fails with a bot-protection error, use browser(navigate) + page_text instead.`,
    provider,
    oasis.WithSandbox(sb, sandbox.Tools(sb)...),
)

result, _ := agent.Execute(ctx, oasis.AgentTask{
    Input: "Go to https://example.com/pricing, find the Enterprise plan details, and give me a bullet-point summary.",
})
```

**Plain-English walkthrough:**

- No special Go code is needed — `sandbox.Tools(sb)` includes `browser`, `snapshot`,
  `page_text`, `browser_find`, and `screenshot` automatically.
- The system prompt hint tells the LLM to fall back from `http_fetch` (a plain GET)
  to the browser stack when it hits a WAF. The `http_fetch` tool itself surfaces
  guidance text in its error response when it detects a 403/502.
- The LLM will typically: call `browser(action=navigate, url=...)` → call `snapshot`
  to see the page structure → call `page_text` to extract readable text.

**Variations:**

- Add `screenshot` to the prompt instructions if you want the LLM to capture visual
  evidence of the page state.
- Use `browser_eval` to read JavaScript values (form field content, local storage,
  etc.) that do not appear in the accessibility tree.
- Use `browser_find` + `browser(action=click)` for reliable form interaction:
  `browser_find("submit button")` returns a `Ref` you can pass to
  `browser(action=click, ref=...)`.

---

## Recipe 5: Call an MCP tool running inside the sandbox

**Goal:** Use an MCP server that is started inside the container (not on the host),
exposed as a tool the agent can call.

```go
// The sandbox implementation starts MCP servers declared in its config.
// Here we assume "my-db-server" is an MCP server running inside the container.

agent := oasis.NewLLMAgent(
    "db-agent", "You can query the database via the mcp_call tool.",
    provider,
    oasis.WithSandbox(sb, sandbox.Tools(sb)...),
)

result, _ := agent.Execute(ctx, oasis.AgentTask{
    Input: "List all tables in the database and show me the row count for each.",
})

// The LLM will generate calls like:
//   mcp_call(server="my-db-server", tool="list_tables", args={})
//   mcp_call(server="my-db-server", tool="query", args={"sql": "SELECT ..."})
```

**Plain-English walkthrough:**

- `mcp_call` is included automatically in `sandbox.Tools(sb)`. No extra setup is
  needed on the Go side.
- The `Server` field is the MCP server name as registered inside the container. How
  servers are declared and started is determined by the implementation
  (e.g., `oasis-sandbox-ix` reads a server manifest from the container config).
- The LLM sees `mcp_call` in its tool list and calls it with `server`, `tool`, and
  `args`. The framework forwards the call to the sandbox, which dispatches it to the
  in-container MCP server and returns the result.

**Variations:**

- Pass custom MCP server config in `CreateOpts.Env` to point the in-container server
  at a different database or API.

---

## Recipe 6: Custom resource limits per sandbox

**Goal:** Give a data-processing sandbox more CPU and memory than the default.

```go
sb, err := mgr.Create(ctx, sandbox.CreateOpts{
    SessionID: "heavy-job-99",
    Resources: sandbox.ResourceSpec{
        CPU:    4,
        Memory: 8 * 1024 * 1024 * 1024, // 8 GB
        Disk:   50 * 1024 * 1024 * 1024, // 50 GB
    },
    TTL: 2 * time.Hour,
})
```

**Plain-English walkthrough:**

- `ResourceSpec` fields default to the manager's built-in defaults (1 CPU, 2 GB RAM,
  10 GB disk) when set to zero. Set non-zero values only for sandboxes that genuinely
  need more.
- `TTL` is a hard wall-clock limit on the sandbox lifetime. The container is
  automatically destroyed after `TTL` even if the agent is still running. Use this
  as a safety valve for long-running jobs.
- The implementation enforces these limits; the Go types are declarations, not
  enforcement — enforcement is in the container runtime.

**Variations:**

- Use the default `ResourceSpec{}` (all zeros) for most agents. Only customise
  for known heavy workloads.
- Set a short `TTL` (e.g., 5 minutes) for quick one-shot tasks to ensure the
  container is cleaned up promptly even on agent error.
