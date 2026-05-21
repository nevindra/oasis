# Extract ix Sandbox to a Separate Repository

**Date:** 2026-05-21
**Status:** Draft — awaiting approval
**Type:** Architecture / repo restructuring

## Context

The `ix` sandbox is currently split across three locations in the oasis repo:

- `sandbox/ix/` — Docker manager + HTTP/SSE client implementing `sandbox.Sandbox`.
- `internal/ixd/` — Go HTTP daemon (`ix` binary) that runs inside the container.
- `cmd/ix/` — main entrypoint + Dockerfile that produces the published image.

A single CI workflow (`.github/workflows/build-ix.yml`) builds and pushes the image to GHCR.

`sandbox/ix/` is a subpackage of the `sandbox/` module (no separate `go.mod`).
As a result, the `sandbox/` module's `go.mod` carries the full Docker SDK
(`docker/docker`, `docker/go-connections`, `google/uuid`) plus ~17 transitive
dependencies (Moby, containerd, OCI image-spec, go-winio, Azure ANSI term, OTEL
pieces). Anyone importing `github.com/nevindra/oasis/sandbox` for the interface,
DTOs, or `sandbox.Tools()` inherits all of it, whether or not they ever use ix.

The framework's architecture already supports multiple sandbox backends — the
`sandbox.Sandbox` interface and DTOs are pure, and `sandbox.Tools()` consumes the
interface, not a concrete type. The directory layout is what's blocking that
path: the only implementation is bolted to the same module as the interface.

## Decision

Extract the ix sandbox into its own repository,
`github.com/nevindra/oasis-sandbox-ix`, leaving the `Sandbox` interface, DTOs,
and `sandbox.Tools()` in oasis.

This is a relocation, not a redesign. The `Sandbox` interface stays as-is; ix's
behavior, API surface, and CLI stay as-is. Only the import path and Docker image
path change for end users.

## Non-Goals

- **No interface refactor.** The `Sandbox` interface stays a single fat
  interface. The capability-interface split (`Executor`, `FileSystem`,
  `BrowserClient`, etc.) is a separate, follow-up piece of work.
- **No new backends in this change.** E2B / Daytona / Modal adapters are not
  in scope. The point here is to make them *possible*, not to write them.
- **No CLI or API breaking change beyond the import path.** Method signatures,
  configuration options, Docker behavior, and image contents stay identical.

## Architecture After the Move

### Repository: `github.com/nevindra/oasis-sandbox-ix`

```
oasis-sandbox-ix/
├── go.mod                      # module github.com/nevindra/oasis-sandbox-ix
├── go.sum
├── README.md
├── LICENSE
├── CHANGELOG.md
├── client.go                   ← sandbox/ix/client.go
├── client_test.go              ← sandbox/ix/client_test.go
├── sandbox.go                  ← sandbox/ix/sandbox.go
├── sandbox_test.go             ← sandbox/ix/sandbox_test.go
├── manager.go                  ← sandbox/ix/manager.go
├── manager_test.go             ← sandbox/ix/manager_test.go
├── reaper.go                   ← sandbox/ix/reaper.go
├── health.go                   ← sandbox/ix/health.go
├── mem_linux.go                ← sandbox/ix/mem_linux.go
├── mem_other.go                ← sandbox/ix/mem_other.go
├── integration_test.go         ← sandbox/ix/integration_test.go
├── internal/
│   └── daemon/                 ← internal/ixd/  (renamed)
│       ├── server.go
│       ├── shell.go, code.go
│       ├── file.go, file_transfer.go
│       ├── browser.go, pinchtab.go
│       ├── search.go, tree.go
│       ├── fetch.go, process.go, sse.go
├── cmd/
│   └── ix/
│       ├── main.go             # imports oasis-sandbox-ix/internal/daemon
│       └── Dockerfile          # paths adjusted to new layout
└── .github/
    └── workflows/
        └── build.yml           ← build-ix.yml (paths adjusted)
```

**Package names**

- Top-level package: `ix` — users call `ix.NewManager(...)`, `ix.ManagerConfig`,
  etc. Exported types keep their current names (`IXSandbox`, `IXManager`).
- Daemon package: `daemon` (under `internal/`). Renaming from `ixd` makes sense
  because the new repo is already scoped to ix; `ixd` was meaningful when it
  sat next to other oasis internals.

### Dependency direction

The new repo depends on oasis (for the `sandbox` interface, DTOs, and `core`).
Oasis does not depend on the new repo.

`oasis-sandbox-ix/go.mod`:

```
module github.com/nevindra/oasis-sandbox-ix
go 1.26.1

require (
    github.com/nevindra/oasis              v0.x.y
    github.com/nevindra/oasis/sandbox      v0.x.y
    github.com/docker/docker               v28.5.2+incompatible
    github.com/docker/go-connections       v0.6.0
    github.com/google/uuid                 v1.6.0
)
```

### What stays in oasis

Unchanged:

- `sandbox/sandbox.go` — `Sandbox` interface and all request/result DTOs.
- `sandbox/tools.go` — `sandbox.Tools()`, options, framework-level tool wrappers.
- `sandbox/lifecycle.go`, `manager.go`, `mount.go`, `manifest.go`, `mounter.go` —
  generic primitives. (To verify they don't leak ix assumptions during
  implementation; expected clean.)

Slimmed:

- `sandbox/go.mod` — removes `docker/docker`, `docker/go-connections`,
  `google/uuid`, and ~17 transitive deps. Module becomes stdlib + `oasis` core.

### What is deleted from oasis

- `sandbox/ix/` (entire subtree).
- `internal/ixd/` (entire subtree).
- `cmd/ix/` (entire subtree).
- `.github/workflows/build-ix.yml`.

`go.work` requires no edit — it currently lists `./sandbox` but not `./sandbox/ix`.

## User-Facing API

Only the import path changes. Method signatures, options, behavior are
identical to today.

Before:

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/sandbox"
    "github.com/nevindra/oasis/sandbox/ix"
)
```

After:

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/sandbox"
    ix "github.com/nevindra/oasis-sandbox-ix"
)
```

Construction call sites are unchanged:

```go
mgr, _ := ix.NewManager(ctx, ix.ManagerConfig{
    Image: "ghcr.io/nevindra/oasis-sandbox-ix:latest",
})
sb, _ := mgr.Create(ctx, sandbox.CreateOptions{...})
ag := oasis.Spawn("coder", oasis.WithTools(sandbox.Tools(sb)...))
```

## Docker Image

| Field         | Before                                 | After                                                |
|---------------|----------------------------------------|------------------------------------------------------|
| Default image | `oasis-ix:latest`                      | `ghcr.io/nevindra/oasis-sandbox-ix:latest`           |
| Registry path | `ghcr.io/nevindra/oasis/ix`            | `ghcr.io/nevindra/oasis-sandbox-ix`                  |
| Built from    | `oasis` repo via `build-ix.yml`        | `oasis-sandbox-ix` repo via `build.yml`              |
| Triggered by  | sandbox/ ixd/ cmd/ix/ skills/ changes  | any push/PR to the new repo (skills no longer trigger; not bundled) |

Update the default in `ManagerConfig.applyDefaults` (currently
`sandbox/ix/manager.go:38`) to point at the new GHCR path.

The Dockerfile under `cmd/ix/Dockerfile` keeps its multi-stage build but adjusts
COPY paths for the new layout (no more `internal/ixd/`; copies `internal/daemon/`
and the top-level Go sources).

The `skills/` directory remains in oasis — the skills are framework-level and
do not need to be in the new repo. The Dockerfile does not bundle skills today,
so no change to the image content.

## Documentation Changes in oasis

Update import paths in:

- `docs/concepts/sandbox.md` (3 occurrences: L266, L273, L443)
- `docs/concepts/tool.md` (L901)
- `docs/guides/code-execution.md` (L32)
- `docs/api/constructors.md` (L111)
- `docs/api/options.md` (L269)
- `CLAUDE.md` (project-structure block lists `sandbox/ix/`)
- `CHANGELOG.md` (new entry under `[Unreleased]` documenting breaking change)

The new repo gets a fresh `README.md` covering: what ix is, the `oasis` link,
construction example, image registry path, and how the daemon fits in.

## Versioning and Breaking-Change Handling

This is a breaking import-path change for ix users. Per the project's semver
rules (v0.x.x: minor = breaking changes), it's a minor bump in oasis. The new
repo starts at `v0.1.0` for clarity (independently versioned; mirroring the
oasis version is unnecessary and confusing).

CHANGELOG entry for oasis (`[Unreleased]`):

> **BREAKING:** ix sandbox extracted to its own repository at
> `github.com/nevindra/oasis-sandbox-ix`. Replace
> `import "github.com/nevindra/oasis/sandbox/ix"` with
> `import ix "github.com/nevindra/oasis-sandbox-ix"`. Default image path
> updated to `ghcr.io/nevindra/oasis-sandbox-ix:latest`. The `Sandbox`
> interface, DTOs, and `sandbox.Tools()` remain in oasis unchanged.

## Order of Operations

1. **New repo bring-up.**
   - Create `github.com/nevindra/oasis-sandbox-ix`.
   - Copy ix files with new layout; rewrite imports
     (`oasis/internal/ixd` → `oasis-sandbox-ix/internal/daemon`;
     `oasis/sandbox/ix` package self-references → `oasis-sandbox-ix`).
   - Add `go.mod`, `go.sum`, `LICENSE`, initial `README.md`, `CHANGELOG.md`.
   - `go build ./... && go test ./...` green locally.
2. **CI + image bring-up.**
   - Port `build-ix.yml` to `build.yml` with paths fixed for the new layout.
   - Trigger a manual build; verify the image runs and `GET /health` responds.
   - Push `v0.1.0` tag; confirm image is pullable at the new GHCR path.
3. **oasis cleanup.**
   - Delete the four trees: `sandbox/ix/`, `internal/ixd/`, `cmd/ix/`,
     `.github/workflows/build-ix.yml`.
   - Prune Docker SDK + uuid lines from `sandbox/go.mod`; run `go mod tidy` in
     the `sandbox/` module.
   - Update the seven docs (listed above) + `CHANGELOG.md`.
   - Run full test suite: `go test ./...` from root and from each satellite.
4. **Release.**
   - Tag oasis with the minor bump.

Step 2 must complete before step 3 begins, so the image referenced by the new
default exists publicly before any oasis user upgrades.

## Open Questions

None. Both decisions previously open are now locked:

- Repo name: `oasis-sandbox-ix`.
- Daemon subpath: `internal/daemon/`.

## Future Work (Out of Scope)

These are intentional follow-ups, not blockers:

1. **Capability-interface split.** Refactor `sandbox.Sandbox` into
   `Executor`, `FileSystem`, `WebClient`, `BrowserClient`, `MCPClient`, and
   `Inspector` interfaces, with `Sandbox` becoming the composite. Allows
   adapters like E2B to implement only what they support; `sandbox.Tools()`
   filters via type assertion. Tracked separately; not blocked by this work.
2. **Second backend.** A reference adapter (E2B or Modal) once the capability
   split lands, to validate the interface holds up under a non-ix backend.
3. **Generic-primitive audit in `sandbox/`.** Light pass over `lifecycle.go`,
   `manager.go`, `mount.go`, `manifest.go` to confirm no ix-specific
   assumptions leak through. Expected clean; recorded here so it isn't
   forgotten during implementation.

## Risks

- **`sandbox/` still has Docker-shaped lifecycle types.** If `lifecycle.go` /
  `manager.go` carry container-runtime assumptions, the dep removal in step 3
  may not be as clean as expected. Mitigation: audit during implementation; if
  blockers surface, document them and either keep the deps or split lifecycle
  types into a separate sub-package.
- **Tagged-import drift.** Users on older oasis versions will see broken
  examples in older docs. Mitigation: pin the breaking-change CHANGELOG entry
  prominently and link to the migration in the new repo's README.
- **In-flight branches.** Any open work touching `sandbox/ix/` needs to land
  or rebase before the extraction; otherwise merges break. Mitigation:
  schedule the extraction at a point with no in-flight ix work, or coordinate
  with the relevant branch.
