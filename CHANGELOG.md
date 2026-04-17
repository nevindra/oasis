# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- **`tools/todo` package** — Claude-Code-style `todo_write` tool for agent task
  tracking. Exposes a single tool function (`todo_write`) that accepts a list
  of `{content, activeForm, status}` items (status ∈ `pending` /
  `in_progress` / `completed`). Validates length (max 50 items, 1000-char
  content, 200-char activeForm) and auto-clears the stored list when every
  item is `completed` so downstream UIs can hide the panel.
- **`todo.Backend` interface** — storage adapter (`Get`/`Set` by key) so
  embedders can persist task lists to whatever fits (in-memory, JSONB column,
  file, etc.). Implementations must serialize concurrent `Set` on the same
  key.
- **`todo.New(backend, keyFn)` constructor** — `keyFn(ctx)` extracts the
  scoping identifier (conversation ID, session ID, …) from the agent's
  execution context, letting a single tool instance serve many concurrent
  conversations.
- **`todo.ToolDescription` constant** — full prompt ported from Claude
  Code's `TodoWriteTool/prompt.ts` so the LLM actually uses the tool. The
  port replaces the `${FILE_EDIT_TOOL_NAME}` template with a literal
  "file edit tool"; the verification-agent nudge logic is not part of the
  prompt text and is not ported.

## [0.15.0] - 2026-04-16

### Added
- `Compactor` interface and `StructuredCompactor` default implementation for
  per-thread conversation compaction with a 9-section structured summary
  format (primary intent, technical concepts, files, errors, problem solving,
  all user messages, pending tasks, current work, next step).
- `CompactRequest`, `CompactResult`, `CompactSection` types for compaction.
- `EstimateContextTokens(messages, model)` helper for token estimation.
- `StripMediaBlocks(messages)` helper to remove image/document attachments
  before compaction LLM calls.
- `CompactableToolNames()` helper returning the default whitelist of tool
  names whose results are safe to compact (callers extend this list).
- `BuildCompactPrompt(extras, focusHint, isRecompact)` prompt template builder.
- `WithCompaction(Compactor, threshold)` ConversationOption for opt-in
  auto-trigger during `buildMessages`.
- `provider/catalog.StaticContextWindow(modelID)` — cross-provider static
  InputContext lookup. Returns 0 when the model ID isn't in the registry.
  Useful for `threshold × effectiveWindow` math when the caller's provider
  key doesn't match the static data's provider identifier.

### Changed
- `WithCompressThreshold` default changed from 200_000 (enabled) to 0
  (disabled). Per-turn LLM compression must now be opted into explicitly.
  Per-thread compaction is the preferred strategy.
- Updated docstrings on `WithCompressModel` and `WithCompressThreshold` to
  cross-reference the new compaction primitives.

## [0.14.0] - 2026-04-10

### Added
- **Sandbox filesystem mounts** — new `FilesystemMount` interface in `sandbox/` lets apps back specific sandbox paths with external storage. `MountSpec` declares the path, mode (read-only, write-only, read-write), and lifecycle policy (`PrefetchOnStart`, `FlushOnClose`, `MirrorDeletes`, `Include`/`Exclude` globs). `PrefetchMounts` copies backend files into the sandbox at start; `FlushMounts` scans the sandbox at close and publishes deltas. Tool-level interception in `file_write`, `file_edit`, and `deliver_file` publishes writes to the backend immediately with optimistic version checks. Conflicts surface as tool errors via `ErrVersionMismatch` so the agent can re-read and retry.
- **`WithMounts(specs, manifest)` ToolsOption** — wires a slice of `MountSpec` and a shared `Manifest` into the tool layer.
- **`Manifest` type** — concurrent-safe per-sandbox tracking of `(mountPath, key) → MountEntry` so Layer 2 publishes and Layer 3 flush can send the correct precondition.
- **`FilesystemMounter` capability stub** (`sandbox/mounter.go`) — optional interface for sandbox runtimes to opt into live FUSE/virtio-fs mounting. No implementation ships today.
- **`ErrKeyNotFound` sentinel** — distinct from `sandbox.ErrNotFound` (sandbox-session-level), used by `FilesystemMount.Stat`/`Open` for missing keys.
- `Compatibility`, `License`, `Metadata map[string]string` fields on `Skill` and `SkillSummary` — aligns with the [AgentSkills open specification](https://agentskills.io).
- `ActivateWithReferences()` function — resolves skill references at activation time, prepending referenced skill instructions (one level deep, missing refs silently skipped).
- `WithActiveSkills(skills ...Skill)` agent option — pre-activates skills at init time, injecting their instructions into the system prompt on every LLM call.
- `WithSkills(p SkillProvider)` agent option — registers a `SkillProvider` and auto-adds `skill_discover`/`skill_activate` tools (plus `skill_create`/`skill_update` if the provider implements `SkillWriter`).
- `DefaultSkillDirs()` — returns AgentSkills-compatible scan paths (`<cwd>/.agents/skills/`, `~/.agents/skills/`).
- `{dir}` placeholder in skill instructions resolved to absolute skill directory path at activation time.
- Frontmatter parser supports indented metadata blocks (for `metadata:` with sub-keys).
- Prescriptive built-in skills: `oasis-pdf` (HTML/CSS + Playwright), `oasis-docx` (python-docx), `oasis-xlsx` (openpyxl), `oasis-pptx` (PptxGenJS). Agents use underlying libraries directly with full creative freedom and API access.
- **`Attachments` field on `ToolResult`** — tools can return binary attachments (images, PDFs, etc.) alongside text content. Attachments flow through `DispatchResult` into the agent's accumulated attachments and are passed to the LLM as multimodal input.
- **Tool-loop streaming for single agents** — `LLMAgent` now uses `ChatStream` during tool-loop iterations, providing real-time `EventToolCallDelta` events as arguments arrive. Networks continue using non-streaming `Chat()` to preserve text-delta deduplication with sub-agent streaming.
- **Embedding provider fallback** — unknown embedding provider names in `resolve.EmbeddingProvider` now fall back to OpenAI-compatible when `BaseURL` is provided, matching the existing chat provider behavior.

### Fixed
- **Sandbox and skill tools on Network** — `NewNetwork` was missing the sandbox tool and skill provider registration that `NewLLMAgent` performs, causing "unknown tool" errors for `execute_code`, `shell`, and other sandbox tools when `WithSandbox` was passed to a Network. Also wires `activeSkillInstructions` into the Network's loop config.
- **Router text-delta after child delegation** — the router's final `text-delta` was incorrectly suppressed when a child agent had already streamed, preventing the router from synthesizing or contextualizing the child's output.
- **Qwen provider resolver** — `qwen` and `qwen-cn` were defined in the model catalog but missing from the resolver's known-provider list, causing "embedding provider not supported" errors when configured without an explicit `BaseURL`.
- **HNSW index for high-dimension embeddings** — pgvector HNSW and IVFFlat indexes max out at 2000 dimensions. The Postgres store now skips index creation and falls back to sequential scan when embedding dimensions exceed this limit, instead of failing on init.

### Changed
- **BREAKING:** Built-in document generation skills now teach agents to use underlying libraries directly instead of routing through `oasis-render`. Agents write code that calls python-docx, openpyxl, Playwright, or PptxGenJS — no intermediate JSON spec format.
- Skill tool `skill_activate` output includes `Compatibility`, `License`, and `Metadata` fields.
- Skill tool `skill_create`/`skill_update` accepts `Compatibility`, `License`, `Metadata` parameters.
- **`deliver_file` tool routing** — now consults the mount table to publish files. Falls back to the legacy `FileDelivery` if no mount covers the path. Errors with a clear message if neither is configured.

### Deprecated
- **`FileDelivery` interface** — superseded by `FilesystemMount` with `MountWriteOnly` mode. Continues to work via the fallback path in `deliver_file`. Will be removed in a future release.

### Removed
- `bin/oasis-render` CLI — replaced by prescriptive skills that teach agents to use libraries directly.
- `renderers/` directory — PDF, DOCX, XLSX, PPTX renderer scripts removed.
- `requirements.txt` — Python deps for renderers (library deps remain in Dockerfile for direct agent use).

## [0.13.0] - 2026-03-29

### Added
- **ix — sandbox execution daemon** (`internal/ixd/`, `cmd/ix/`). Go stdlib HTTP daemon that runs inside sandbox containers, replacing gem-server/execd. Provides shell execution (SSE streaming), stateless code execution (Python, JS, Bash), and comprehensive file operations via REST API. Zero external dependencies.
- **Enhanced file operations** — `EditFile`, `GlobFiles`, `GrepFiles` methods on `Sandbox` interface. `file_edit` (surgical string replacement), `file_glob` (pattern search via `fd` with `**` support), `file_grep` (content search via `rg` with regex and context lines) tools. `ReadFile` now uses buffered line-by-line reading with line numbers (`cat -n` format) instead of loading entire files. `GlobFiles` and `GrepFiles` support `Exclude`, `Limit`, and `Context` parameters. 10-50x token savings vs read+rewrite for file edits.
- **Workspace tools for ix sandbox** — 3 new endpoints and 3 fixes to make the sandbox a fast, complete workspace for AI agents:
  - `POST /v1/file/tree` — recursive directory listing with depth control and exclude patterns. Uses `tree` command with native Go fallback.
  - `POST /v1/http/fetch` — URL fetching with readability text extraction (via `go-readability`). Clean text by default, `raw: true` for HTML.
  - `GET /v1/workspace/info` — environment discovery: OS, arch, working directory, installed tools (`rg`, `fd`, `git`, `python3`, `node`, etc.), and browser availability.
  - `file_tree`, `http_fetch`, `workspace_info` sandbox tools registered by default via `sandbox.Tools()`.
- `TreeRequest`, `TreeResult`, `HTTPFetchRequest`, `HTTPFetchResult`, `WorkspaceInfoResult` types on `sandbox` package.
- `Tree`, `HTTPFetch`, `WorkspaceInfo` methods on `Sandbox` interface.
- **File delivery** — `FileDelivery` interface + `deliver_file` tool. Agents can deliver sandbox files to users as downloadable chat attachments. Framework-level capability: apps implement `FileDelivery` to choose storage backend (S3, disk, etc.). Tool conditionally registered via `WithFileDelivery()` option.
- `ToolsOption` functional options for `sandbox.Tools()` — extensible tool registration without breaking the function signature.
- `EventFileAttachment` stream event type for file delivery notifications.
- `SkillProvider` interface for discovering and activating skills.
- `SkillWriter` interface for creating, updating, and deleting skills.
- `FileSkillProvider` — reads skills from directories, hot-reloads without restart.
- `SkillSummary` type for lightweight discovery results.
- **Document generation skills** — `oasis-design-system`, `oasis-pdf`, `oasis-docx`, `oasis-xlsx`, `oasis-pptx` skills in `skills/`. Agents generate PDF (HTML+Tailwind+Playwright), Word (JSON+python-docx), Excel (JSON+openpyxl), and PowerPoint (JSON+PptxGenJS) documents via the `oasis-render` CLI inside the sandbox.
- **`oasis-render` CLI** — unified entry point for document rendering (`bin/oasis-render`). Routes to format-specific renderers: `pdf`, `pdf-fill`, `docx`, `docx-fill`, `xlsx`, `pptx`.
- **Renderer scripts** — `renderers/pdf/render.js` (Playwright HTML->PDF), `renderers/pdf/fill.py` (pypdf form fill), `renderers/docx/generate.py` (python-docx), `renderers/docx/fill.py` (template fill), `renderers/xlsx/generate.py` (openpyxl), `renderers/pptx/compile.js` (PptxGenJS).
- **Sandbox Dockerfile** — single unified `cmd/ix/Dockerfile` with Chrome, Pinchtab, uv, document generation deps (python-docx, openpyxl, pypdf, PptxGenJS, Playwright), and oasis-render CLI.
- **`BuiltinSkillProvider`** — embedded skill provider that serves framework skills (`oasis-pdf`, `oasis-docx`, `oasis-xlsx`, `oasis-pptx`, `oasis-design-system`) from the compiled binary via `//go:embed`. Consumers get document generation skills without filesystem setup.
- **`ChainSkillProviders`** — merges multiple `SkillProvider` implementations. User file-based skills take priority over built-in ones. Typical usage: `ChainSkillProviders(fileProvider, builtinProvider)`.

### Changed
- **Sandbox Dockerfile** — Node.js 22 (nodesource) replaced with Node.js 25 (fnm), npm replaced with pnpm (via corepack), uv pinned to 0.11.2 with `--break-system-packages` for PEP 668 compliance on Ubuntu 24.04.
- **BREAKING:** `sandbox/aio` package renamed to `sandbox/ix`. `AIOSandbox` → `IXSandbox`, `AIOManager` → `IXManager`. Import path: `github.com/nevindra/oasis/sandbox/ix`.
- **BREAKING:** `IXSandbox` now communicates via SSE for shell/code execution (previously synchronous JSON). Client-side change only — `Sandbox` interface unchanged.
- **BREAKING:** `Sandbox.ReadFile` now accepts `ReadFileRequest` instead of a plain path string. `FileContent` gains `TotalLines` field.
- **BREAKING:** `Sandbox.GlobFiles` now returns `GlobResult` (with `Files` and `Truncated`) instead of `[]string`. `GlobRequest` gains `Exclude` and `Limit` fields.
- **BREAKING:** `Sandbox.GrepFiles` now returns `GrepResult` (with `Matches` and `Truncated`) instead of `[]GrepMatch`. `GrepRequest` gains `Context` and `Limit` fields. `GrepMatch` gains `ContextBefore` and `ContextAfter`.
- **BREAKING:** Skills are now file-based (folders with `SKILL.md`) instead of database-stored. Skill CRUD methods removed from `Store` interface. Use `SkillProvider` and `FileSkillProvider` instead.
- Skill tool now exposes `skill_discover` and `skill_activate` instead of `skill_search`. Progressive disclosure: discover returns names only, activate loads full instructions.
- All sandbox tool descriptions updated to guide agents toward dedicated tools and away from shell workarounds (e.g., "Use file_read instead of cat via shell").
- Default sandbox image changed from `ghcr.io/agent-infra/sandbox:latest` to `oasis-ix:latest`.
- Health check endpoint changed from `GET /v1/shell/sessions` to `GET /health`.
- Added GitHub CI workflow (`build-ix.yml`) to build and push ix sandbox Docker image on sandbox-related changes.

### Removed
- `sandbox/aio` package — replaced by `sandbox/ix`.
- `Store.CreateSkill`, `Store.GetSkill`, `Store.ListSkills`, `Store.UpdateSkill`, `Store.DeleteSkill`, `Store.SearchSkills` — replaced by `SkillProvider`.
- `ScoredSkill` type — no longer needed (no embedding-based search).
- `Skill.ID`, `Skill.Embedding`, `Skill.CreatedBy`, `Skill.CreatedAt`, `Skill.UpdatedAt` fields — replaced by filesystem metadata.
- `store/sqlite/skills.go`, `store/postgres/skills.go` — DB skill implementations.
- `cmd/sandbox/` — legacy sandbox service, superseded by `cmd/ix/`.
- `cmd/ix/Dockerfile.browser` — merged into `cmd/ix/Dockerfile` (single image with Chrome + document generation).

## [0.12.1] - 2026-03-19

### Fixed

- **`modelgen` output path** — generated files were written to a nested `provider/catalog/provider/catalog/` path instead of `provider/catalog/`. Default `-out` flag now uses relative paths since `go generate` runs from the package directory
- **Regenerated static registry** — 3836 models from models.dev (was 646 from OpenRouter) with full pricing, capabilities, modalities, and metadata. 82 auto-discovered platforms with base URLs and env vars

## [0.12.0] - 2026-03-19

### Added

- **models.dev integration** — model registry generator (`cmd/modelgen`) now fetches from `models.dev/api.json` instead of OpenRouter. 2600+ models with richer metadata: pricing, capabilities, modalities, cache pricing, model family, knowledge cutoff, and release dates
- **`Reasoning`, `StructuredOutput`, `Attachment` capabilities** — `ModelCapabilities` now tracks whether a model supports chain-of-thought reasoning (o3, DeepSeek-R1, Claude thinking), JSON structured output, and file/media attachments
- **`InputModalities`, `OutputModalities` on `ModelInfo`** — granular modality tracking (text, image, audio, pdf, video) replaces the coarse `Vision` boolean for model selection
- **`Family`, `OpenWeights`, `KnowledgeCutoff`, `ReleaseDate` on `ModelInfo`** — model family grouping, open-source flag, and temporal metadata for informed model selection
- **`CacheReadPerMillion`, `CacheWritePerMillion` on `ModelPricing`** — cache-aware pricing from models.dev. Enables accurate cost tracking for Gemini, Claude, and other providers with prompt caching
- **`EnvVars` on `Platform`** — standard environment variable names for API keys (e.g., `["OPENAI_API_KEY"]`), sourced from models.dev provider data
- **`PricingMap()` in `provider/catalog`** — returns `map[string]ModelPricing` from the static registry for initializing `CostCalculator` without API calls
- **`NewCostCalculatorFromModels`** — creates a `CostCalculator` from `[]ModelInfo` entries, with optional overrides. Bridges the catalog and observer packages
- **`platforms_gen.go` generated output** — `modelgen` now generates a second file with provider platform data (base URLs, env vars) discovered from models.dev, merged with manually curated builtins at catalog construction

### Changed

- **`CostCalculator.Calculate` is now cache-aware** — signature changed from `(model, input, output)` to `(model, input, output, cached)`. When cached tokens > 0 and the model has cache pricing, cached tokens are billed at the lower `CacheReadPerMillion` rate. **Breaking:** callers must add the 4th argument (pass `0` to preserve old behavior)
- **Observer emits `llm.tokens.cached` attribute** — span attributes and structured logs now include cached token counts alongside input/output
- **`enrichLiveWithStatic` handles new fields** — live/static merge now preserves Family, Modalities, KnowledgeCutoff, ReleaseDate, and OpenWeights from static data when live API doesn't provide them
- **CI workflow references models.dev** — `update-models.yml` now checks both `models_gen.go` and `platforms_gen.go` for changes, and PR descriptions reference models.dev

[Unreleased]: https://github.com/nevindra/oasis/compare/v0.14.0...HEAD
[0.14.0]: https://github.com/nevindra/oasis/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/nevindra/oasis/compare/v0.12.1...v0.13.0
[0.12.1]: https://github.com/nevindra/oasis/compare/v0.12.0...v0.12.1
[0.12.0]: https://github.com/nevindra/oasis/releases/tag/v0.12.0
