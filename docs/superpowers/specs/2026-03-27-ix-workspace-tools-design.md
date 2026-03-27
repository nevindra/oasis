# IX Workspace Tools — Design Spec

**Date:** 2026-03-27
**Goal:** Make the ix sandbox a fast, complete workspace for AI agents — enabling people to build their own ChatGPT or Claude Code on top of oasis.

**Framing:** The sandbox is the agent's workspace. It lives there, reads files, edits code, navigates directories. Code execution is one thing it *can* do, not the primary purpose. The hot path is read → grep → edit → read cycles.

---

## Scope

3 new endpoints, 3 fixes to existing endpoints, smart tool descriptions in `sandbox/tools.go`.

---

## 1. Fix: `file/read` — Buffered Reading with Line Numbers

**Problem:** Current implementation uses `os.ReadFile` which loads the entire file into memory, then splits all lines, then slices. A 10MB file read for 50 lines = 10MB allocation + full string split.

**Fix:** Buffered line-by-line reading. Seek to offset, read only `limit` lines. Return with line numbers (`cat -n` style).

**Endpoint:** `POST /v1/file/read` (unchanged URL)

**Request:**
```json
{
  "path": "/workspace/main.go",
  "offset": 0,
  "limit": 2000
}
```

**Response:**
```json
{
  "path": "/workspace/main.go",
  "content": "     1\tpackage main\n     2\t\n     3\timport (\n...",
  "total_lines": 450
}
```

**Implementation:**
- Open file with `os.Open`, use `bufio.Scanner` to skip `offset` lines, read `limit` lines.
- Format each line as `printf("%6d\t%s", lineNum, line)` — matches `cat -n` format.
- To get `total_lines` without reading the full file: count newlines with a byte-level scan after reading the requested range. For files under 1MB, this is instant. For larger files, use `wc -l` via exec or accept the cost (it's still I/O-bound, not memory-bound since we don't hold the full content).
- Never allocate the full file content as a single string.

---

## 2. Fix: `file/glob` — `**` Support, Limit, Exclude

**Problem:** Native fallback uses `filepath.Match` which only matches basenames. `**/*.go` doesn't work. No way to limit results or exclude directories like `node_modules` and `.git`.

**Endpoint:** `POST /v1/file/glob` (unchanged URL)

**Request:**
```json
{
  "pattern": "**/*.go",
  "path": "/workspace",
  "exclude": ["node_modules", ".git", "vendor"],
  "limit": 200
}
```

**Response:**
```json
{
  "files": ["main.go", "agent.go", "..."],
  "truncated": false
}
```

**Implementation:**
- `fd` path (preferred): Add `--exclude` flags from the `exclude` array. Pipe through `head -n limit` or read only `limit` lines from stdout.
- Native fallback: Replace `filepath.Match` with `doublestar.Match` from `github.com/bmatcuk/doublestar/v4` (or hand-roll recursive matching). Skip directories matching `exclude` list in `WalkDir` by returning `fs.SkipDir`. Stop after `limit` results.
- Default `exclude`: `[".git"]` (always skip `.git` unless explicitly included).
- Default `limit`: `1000`.

---

## 3. Fix: `file/grep` — Regex Fallback, Context Lines, Limit

**Problem:** Native fallback is substring-only (`strings.Contains`), no regex. No context lines. No result limit — can return 10,000 matches and blow up the agent's context window.

**Endpoint:** `POST /v1/file/grep` (unchanged URL)

**Request:**
```json
{
  "pattern": "func\\s+Test",
  "path": "/workspace",
  "glob": "*.go",
  "context": 2,
  "limit": 50
}
```

**Response:**
```json
{
  "matches": [
    {"path": "agent_test.go", "line": 15, "content": "func TestAgent(t *testing.T) {", "context_before": ["", "// TestAgent verifies..."], "context_after": ["\tctx := context.Background()", "\tagent := NewAgent(..."]}
  ],
  "truncated": false
}
```

**Implementation:**
- `rg` path (preferred): Add `-C <context>` for context lines. Add `--max-count <limit>` to cap results. Parse `rg --json` output which already includes context when `-C` is used.
- Native fallback: Replace `strings.Contains` with `regexp.Compile(pattern)` + `regex.MatchString(line)`. For context lines, buffer the last N lines during scan. Cap total matches at `limit`.
- Default `limit`: `100`.
- Default `context`: `0` (no context lines, same as current behavior).
- Default `exclude`: skip `.git` directory in both paths.

---

## 4. New: `file/tree` — Recursive Directory Listing

**Problem:** `file/ls` returns one level only. Agents need project structure overview and currently resort to `shell find . -type f` or multiple `file/ls` calls.

**Endpoint:** `POST /v1/file/tree`

**Request:**
```json
{
  "path": "/workspace",
  "depth": 3,
  "exclude": ["node_modules", ".git", "vendor", "__pycache__"]
}
```

**Response:**
```json
{
  "tree": "workspace/\n  agent.go\n  go.mod\n  go.sum\n  cmd/\n    bot_example/\n      main.go\n  tools/\n    file/\n      file.go\n    shell/\n      shell.go",
  "stats": {"files": 42, "dirs": 8}
}
```

**Implementation:**
- Try `tree` command if installed: `tree -L <depth> --noreport -I '<exclude patterns>'`. Parse output directly (it's already formatted).
- Native fallback: `filepath.WalkDir` with depth tracking. Format as indented tree (2 spaces per level). Skip directories in `exclude` list via `fs.SkipDir`. Cap output at 500 entries to prevent unbounded results.
- Default `depth`: `3`.
- Default `exclude`: `[".git", "node_modules", "__pycache__", ".venv", "vendor"]`.

---

## 5. New: `http/fetch` — URL Fetching with Readability

**Problem:** Agents in the sandbox have no way to fetch URLs without shelling out to `curl` or writing Python with `requests`. Raw HTML is 90% noise.

**Endpoint:** `POST /v1/http/fetch`

**Request:**
```json
{
  "url": "https://docs.example.com/api",
  "raw": false,
  "max_chars": 8000
}
```

**Response:**
```json
{
  "url": "https://docs.example.com/api",
  "title": "API Documentation",
  "content": "clean readable text extracted by readability..."
}
```

**Implementation:**
- HTTP GET with 15-second timeout, 1MB body limit, `User-Agent: OasisBot/1.0`.
- Default: extract readable text via `go-shiori/go-readability` (already a dependency in `tools/http`). Fallback to `ingest.StripHTML` if readability fails.
- `raw: true`: return raw HTML body (for scraping/inspection use cases).
- `max_chars`: truncate output (default 8000). Prevents token blowup.
- Return HTTP 4xx/5xx as error responses, not as content.

**Note:** The ix daemon runs inside the Docker container. Network access depends on Docker network config — the container must have outbound network access for this to work. This is a deployment concern, not a code concern.

---

## 6. New: `workspace/info` — Environment Discovery

**Problem:** Agents starting a session don't know what tools are available, what OS they're on, or what the working directory is. They waste tokens running `which rg && which fd && which git && uname -a && pwd`.

**Endpoint:** `GET /v1/workspace/info`

**Response:**
```json
{
  "os": "linux",
  "arch": "amd64",
  "working_dir": "/workspace",
  "tools": {
    "rg": true,
    "fd": true,
    "git": true,
    "python3": true,
    "node": true,
    "tree": true,
    "curl": false
  },
  "browser": true
}
```

**Implementation:**
- `os` and `arch` from `runtime.GOOS` and `runtime.GOARCH`.
- `working_dir` from `os.Getwd()`.
- `tools`: `exec.LookPath` for each known tool name. Check: `rg`, `fd`/`fdfind`, `git`, `python3`, `node`, `tree`, `curl`, `wget`.
- `browser`: check Pinchtab availability via `s.pt.isAvailable()`.
- This endpoint is cheap (all lookups are cached or fast). Can be called once at session start.

---

## 7. Tool Descriptions in `sandbox/tools.go`

Smart descriptions that guide agents toward the right tool. Not framework-level opinions — just good tool descriptions at the sandbox tool layer.

**Shell tool — explicitly lists what NOT to use it for:**
```
Execute a shell command in the sandbox. Use for system tasks, running builds,
git operations, installing packages, and commands that don't have a dedicated tool.
Do NOT use shell for: reading files (use file_read), searching file contents
(use file_grep), finding files (use file_glob), writing files (use file_write),
editing files (use file_edit), or fetching URLs (use http_fetch).
```

**File read — explains when to use it:**
```
Read file content with line numbers. Supports offset and limit for reading
specific line ranges. Use this instead of running cat, head, tail, or sed
via shell. Returns content in cat -n format with line numbers for precise editing.
```

**File grep — explains its advantage:**
```
Search file contents for a regex pattern. Returns matching lines with file paths,
line numbers, and optional context lines. Use this instead of running grep or rg
via shell — results are structured and token-efficient.
```

**File glob — explains its advantage:**
```
Find files matching a glob pattern. Supports ** for recursive matching.
Use this instead of running find or ls via shell.
```

**HTTP fetch — explains readability:**
```
Fetch a URL and extract readable text content. Returns clean text by default
(HTML noise removed). Use raw=true to get unprocessed HTML. Use this instead of
running curl via shell or writing Python to fetch URLs.
```

**Apply the same pattern to all sandbox tools** — each description says what it does AND what the agent should NOT use instead.

---

## Changes by File

| File | Change |
|---|---|
| `internal/ixd/file.go` | Rewrite `handleFileRead` (buffered + line numbers). Fix `globFilesNative` (doublestar support). Fix `grepFilesNative` (regex). Add limit/exclude/context params to glob and grep. |
| `internal/ixd/tree.go` | New file. `handleFileTree` endpoint. |
| `internal/ixd/fetch.go` | New file. `handleHTTPFetch` endpoint. |
| `internal/ixd/server.go` | Register new routes: `POST /v1/file/tree`, `POST /v1/http/fetch`, `GET /v1/workspace/info`. |
| `sandbox/sandbox.go` | Add `Tree`, `HTTPFetch`, `WorkspaceInfo` methods to Sandbox interface. |
| `sandbox/ix/sandbox.go` | Implement new methods against ix client. |
| `sandbox/ix/client.go` | Add client methods for new endpoints. |
| `sandbox/tools.go` | Add `treeTool`, `httpFetchTool`, `workspaceInfoTool`. Update all tool descriptions. |
| `sandbox/tools_test.go` | Tests for new tools + updated descriptions. |
| `go.mod` | Add `github.com/bmatcuk/doublestar/v4` (if we use it for native glob fallback). |

---

## Dependencies

- `github.com/go-shiori/go-readability` — already in `go.mod` (used by `tools/http`).
- `github.com/bmatcuk/doublestar/v4` — new dependency for native `**` glob support. Small, well-maintained, no transitive deps. Alternative: hand-roll recursive matching, but doublestar is battle-tested.

---

## Out of Scope

- Git-specific endpoints (shell works fine for git)
- Process management (shell handles `ps`/`kill`)
- File watch/notify (different paradigm)
- Batch edit endpoint (localhost HTTP overhead is negligible)
- Archive operations (shell handles tar/zip)
- Framework-level tool categories (can add later if descriptions prove insufficient)
