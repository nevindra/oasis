# Skills API

Import path: `github.com/nevindra/oasis/skills`

The root `oasis` package re-exports `Skill`, `WithSkills`, and `WithActiveSkills` for convenience.

---

## Types

### `Skill`

A loaded skill. The full representation including instructions.

| Field | Type | Notes |
|---|---|---|
| `Name` | `string` | Canonical identifier. Matches the folder name on disk. |
| `Description` | `string` | Short summary used during discovery. |
| `Instructions` | `string` | Full markdown instructions injected into the agent context. After `Activate`, any `{dir}` placeholder is replaced with the absolute folder path. |
| `Tools` | `[]string` | Optional list of tool names the skill recommends. Advisory — the agent is not forced to use only these. |
| `Model` | `string` | Optional model override hint. Not enforced by the framework automatically. |
| `Tags` | `[]string` | Categorization labels. Useful for discovery filtering. |
| `References` | `[]string` | Names of other skills whose instructions should be prepended. Resolved by `ActivateWithReferences`. |
| `Dir` | `string` | Absolute path to the skill folder on disk. Empty for built-in (embedded) skills. Not serialized to JSON. |
| `Compatibility` | `string` | Free-form compatibility string (e.g., `"oasis >= 0.30"`). |
| `License` | `string` | SPDX license identifier. |
| `Metadata` | `map[string]string` | Arbitrary key-value pairs from the `metadata:` block in frontmatter. |

Zero value is valid but not useful — `Name` and `Instructions` are the meaningful fields.

---

### `SkillSummary`

A lightweight view returned by `Discover`. Only the fields needed for an agent to decide whether to activate.

| Field | Type | Notes |
|---|---|---|
| `Name` | `string` | Folder name; the value to pass to `Activate`. |
| `Description` | `string` | Short summary. |
| `Tags` | `[]string` | Categorization labels. Omitted if empty. |
| `Compatibility` | `string` | Compatibility string. |

Full instructions are not loaded during discovery — that's intentional for performance.

---

### `SkillProvider` (interface)

Abstracts the source of skills. Implementations must be safe for concurrent use.

```go
type SkillProvider interface {
    Discover(ctx context.Context) ([]SkillSummary, error)
    Activate(ctx context.Context, name string) (Skill, error)
}
```

**`Discover`** returns all available skills as summaries. Results are rescanned on every call — no caching — so newly created skills are immediately visible without restart. Returns an empty slice (not an error) when no skills exist.

**`Activate`** loads the full `Skill` for the given name. Returns a non-nil error if the skill does not exist. The `{dir}` placeholder in `Instructions` is replaced with the absolute folder path before returning.

---

### `SkillWriter` (interface)

Optional capability. File-based providers implement this; built-in (embedded) providers do not. Check via type assertion:

```go
if w, ok := provider.(skills.SkillWriter); ok {
    _ = w.CreateSkill(ctx, skill)
}
```

```go
type SkillWriter interface {
    CreateSkill(ctx context.Context, skill Skill) error
    UpdateSkill(ctx context.Context, name string, skill Skill) error
    DeleteSkill(ctx context.Context, name string) error
}
```

**`CreateSkill`** writes a new skill folder and `SKILL.md` in the first configured directory. Errors if `Name` is empty, `Metadata` is missing, or the skill already exists. On write failure the partially created folder is cleaned up.

**`UpdateSkill`** rewrites the `SKILL.md` of an existing skill. Searches all configured directories in order. Errors if the skill is not found.

**`DeleteSkill`** removes the skill folder and all its contents. Errors if the skill is not found.

---

## Constructors

### `FromDir(dirs ...string) SkillProvider`

Returns a `SkillProvider` (also a `SkillWriter`) that reads skills from the given directories. Searches in order — first directory wins on name collisions. Write operations target the first directory. Non-existent directories are silently skipped.

```go
provider := skills.FromDir("./skills", "./team-skills")
```

`FromDir` with no arguments is valid — it creates an empty provider that never finds anything. Useful as a placeholder in tests.

### `Builtin() SkillProvider`

Returns a read-only `SkillProvider` backed by skills compiled into the framework binary. Does not implement `SkillWriter`. Ships with `oasis-pdf`, `oasis-docx`, `oasis-xlsx`, `oasis-pptx`, `oasis-design-system`.

```go
provider := skills.Builtin()
```

### `Chain(providers ...SkillProvider) SkillProvider`

Merges multiple providers. `Discover` returns the union, sorted by name, with the first provider winning on name collisions. `Activate` searches in order and returns the first match.

```go
provider := skills.Chain(
    skills.FromDir("./skills"),
    skills.Builtin(),
)
```

The returned provider does not implement `SkillWriter` even if some of its members do.

### `DefaultSkillDirs() []string`

Returns the standard AgentSkills-compatible scan paths:

- `<cwd>/.agents/skills/` — project-level
- `~/.agents/skills/` — user-level

Directories that do not exist are included in the list — `FromDir` handles missing directories gracefully.

```go
provider := skills.FromDir(skills.DefaultSkillDirs()...)
```

---

## Functions

### `ActivateWithReferences(ctx, provider, name) (Skill, error)`

Activates a skill by name and prepends instructions from any skills listed in its `References` field. References are resolved one level deep — a referenced skill's own references are not followed. Missing references are silently skipped.

```go
skill, err := skills.ActivateWithReferences(ctx, provider, "pdf-gen")
// skill.Instructions now starts with base-knowledge instructions,
// followed by a separator, followed by pdf-gen's own instructions.
```

The combined instructions format is:
```
## <referenced-skill-name>

<referenced instructions>

---

## <referenced-skill-2-name>

<referenced instructions>

---

<main skill instructions>
```

### `NewSkillTools(provider SkillProvider) []core.AnyTool`

Returns the set of skill-management tools backed by the given provider. Called automatically by the framework when you use `WithSkills` — you do not normally call this directly.

- Always returns `skill_discover` and `skill_activate`.
- Also returns `skill_create` and `skill_update` if `provider` implements `SkillWriter`.

---

## Options (agent configuration)

### `WithSkills(provider SkillProvider) AgentOption`

Registers a skill provider. The framework calls `NewSkillTools(provider)` at agent build time and registers the resulting tools. The LLM can then discover and activate skills autonomously.

```go
ag := oasis.NewAgent(llm, oasis.WithSkills(provider))
```

### `WithActiveSkills(skills ...Skill) AgentOption`

Pre-activates one or more skills. Their instructions are appended to the system prompt on every LLM call — no tool call required. Use when you know upfront which skills are always relevant.

```go
skill, _ := provider.Activate(ctx, "data-analyst")
ag := oasis.NewAgent(llm, oasis.WithActiveSkills(skill))
```

`WithActiveSkills` and `WithSkills` can be combined: some skills are always active, others are discoverable on demand.

---

## The `SKILL.md` file format

Skills are stored as directories. The directory name is the skill's canonical identifier. The `SKILL.md` file uses YAML frontmatter (delimited by `---`) followed by the instruction body.

```markdown
---
name: data-analyst
description: Analyze datasets, produce summaries, identify trends.
tags: [data, analytics, csv]
tools: [shell, file_read]
model: gpt-4o
references: [base-statistics]
compatibility: oasis >= 0.30
license: MIT
metadata:
  author: your-name
  version: 1.0.0
---

You are an expert data analyst. When given a dataset:

1. Inspect the schema first — `head`, column names, data types.
2. Look for nulls, outliers, and format inconsistencies before drawing conclusions.
3. Summarize findings in plain language the user can act on.
```

**Frontmatter field reference:**

| Key | Required | Notes |
|---|---|---|
| `name` | Recommended | Falls back to folder name if absent. |
| `description` | Yes (for discovery) | Shown in `skill_discover` output; used by the LLM to decide whether to activate. |
| `tags` | No | Inline array: `[go, data, pdf]` |
| `tools` | No | Inline array of tool names the skill recommends. |
| `model` | No | Model override hint. |
| `references` | No | Inline array of skill names to prepend when using `ActivateWithReferences`. |
| `compatibility` | No | Free-form compatibility string. |
| `license` | No | SPDX identifier. |
| `metadata` | No | Nested key-value block. Accessible as `Skill.Metadata`. |

The `{dir}` placeholder in the instructions body is replaced at activation time with the absolute path to the skill folder. Use it to reference assets like templates or config files:

```markdown
Load the invoice template from {dir}/templates/invoice.html.
```

---

## Errors

| Situation | Behavior |
|---|---|
| `Activate` with unknown name | Returns `error: skill "X" not found` |
| `CreateSkill` with empty `Name` | Returns error immediately; no disk write |
| `CreateSkill` when skill already exists | Returns error; no overwrite |
| `UpdateSkill` / `DeleteSkill` with unknown name | Returns error |
| `Discover` on non-existent directory | Silently skipped; no error returned |
| `parseFrontmatter` on malformed file | Skill is silently skipped during `Discover`; `Activate` returns error |
| `ActivateWithReferences` with missing reference | Missing reference is skipped; skill still activates successfully |

---

## Thread safety

All exported types (`fileSkillProvider`, `builtinSkillProvider`, `chainedSkillProvider`) are safe for concurrent use. `Discover` rescans the filesystem on every call with no shared mutable state. `CreateSkill` / `UpdateSkill` / `DeleteSkill` do not hold locks across file operations — concurrent writes to the same skill name are not protected; callers should serialize writes if needed.
