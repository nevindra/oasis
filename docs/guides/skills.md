# Skills

Skills are file-based instruction packages that specialize agent behavior. They live on disk as folders containing a `SKILL.md` file and can be created, discovered, and activated at runtime — by both humans and agents.

## Architecture Overview

```mermaid
flowchart TB
    subgraph "Skill Sources"
        HUMAN["Human<br>(file on disk)"]
        AGENT["Agent<br>(skill tool)"]
    end

    subgraph "FileSkillProvider"
        DISCOVER["Discover()<br>names + descriptions"]
        ACTIVATE["Activate()<br>full instructions"]
        CREATE["Create()<br>writes SKILL.md"]
        DIR[("Skills directory<br>(folders on disk)")]
    end

    subgraph "Agent Runtime"
        TOOL["tools/skill<br>skill_discover<br>skill_activate<br>skill_create<br>skill_update"]
        INJECT["Inject Instructions<br>→ system prompt"]
    end

    HUMAN --> DIR
    AGENT --> TOOL
    TOOL --> DISCOVER
    TOOL --> ACTIVATE
    TOOL --> CREATE
    DISCOVER --> DIR
    ACTIVATE --> DIR
    CREATE --> DIR
    DIR -.-> INJECT

    style TOOL fill:#e1f5fe
```

Two entry points, one directory. Humans drop skill folders on disk. Agents get discover, activate, create, and update through the skill tool. The filesystem is the source of truth — no database, no embeddings, no migrations.

## SKILL.md Format

Each skill is a folder containing a `SKILL.md` file with YAML frontmatter and a markdown body:

```markdown
---
name: code-reviewer
description: Review code changes and suggest improvements
tools:
  - shell_exec
  - file_read
model: ""
tags:
  - dev
  - review
references:
  - frontend-review
  - backend-review
---

You are a code reviewer. Analyze code for style, correctness, and performance.

When reviewing:
- Check for common Go pitfalls: nil dereferences, goroutine leaks, error shadowing
- Suggest idiomatic patterns when you see non-idiomatic code
- Be constructive — explain why a change is better, not just what to change
- Focus on correctness first, performance second, style third
```

**Frontmatter fields:**

| Field          | Purpose                                                                                       |
| -------------- | --------------------------------------------------------------------------------------------- |
| `name`         | Unique identifier for the skill (matches the folder name by convention)                       |
| `description`  | Short summary used during discovery — agents read this to decide whether to activate          |
| `tools`        | Restrict available tools when this skill is active (empty = all tools available)              |
| `model`        | Override the agent's default LLM (e.g. use a stronger model for complex skills)               |
| `tags`         | Labels for categorization (e.g. `["dev", "review"]`)                                         |
| `references`   | Names of other skills this skill builds on, enabling composability                            |

**Markdown body:** The full instructions injected into the agent's system prompt when the skill is activated. No length limit — write as much as needed.

## Skill Directory Structure

```
skills/
├── code-reviewer/
│   ├── SKILL.md          # required — frontmatter + instructions
│   ├── scripts/          # optional — helper scripts the skill references
│   └── references/       # optional — external docs, examples
├── sql-optimizer/
│   ├── SKILL.md
│   └── assets/           # optional — diagrams, templates
└── go-debugger/
    └── SKILL.md
```

Each skill is a self-contained folder. Only `SKILL.md` is required. The optional subdirectories (`scripts/`, `references/`, `assets/`) are conventions — the framework does not interpret them, but agents can reference them in instructions.

## FileSkillProvider

`FileSkillProvider` reads skills from a directory on disk. No database, no caching — it reads from disk on every call, which means hot reload for free.

**Package:** `github.com/nevindra/oasis/skills`

```go
import "github.com/nevindra/oasis/skills"

// Create a provider pointing at a directory.
provider := skills.NewFileSkillProvider("./skills")
```

**Constructor:**

```go
func NewFileSkillProvider(dir string) *FileSkillProvider
```

`dir` is the path to the directory containing skill folders. Each subdirectory containing a `SKILL.md` file is loaded as a skill.

**Directory scanning:** On each `Discover` or `Activate` call, the provider scans the directory for `SKILL.md` files. There is no startup loading and no in-memory cache — reads are always from disk.

**Hot reload:** Because there is no cache, adding, editing, or deleting a skill folder takes effect on the next call. No restart required.

**`FileSkillProvider` also implements `SkillWriter`**, enabling agents to create and update skills at runtime:

```go
// FileSkillProvider implements both SkillProvider and SkillWriter.
var _ oasis.SkillProvider = (*skills.FileSkillProvider)(nil)
var _ oasis.SkillWriter   = (*skills.FileSkillProvider)(nil)
```

## Progressive Disclosure

Skills use a two-phase access pattern: **Discover** returns lightweight summaries, **Activate** loads full instructions. This keeps discovery cheap — agents get names and descriptions without loading every skill's full text.

```
Discover()  →  []SkillSummary   (name + description + tags only)
                     |
              Agent reads summaries,
              decides which skill fits
                     |
Activate(name)  →  Skill   (full instructions + all fields)
```

**Why this matters:** A skill library with 50 skills could have thousands of lines of instructions total. Discovery returns only the summaries — a few hundred tokens. The agent picks the right skill, then activates exactly one — loading only what it needs.

## Skill Tool (Agent-Facing)

The `tools/skill` package exposes skill management to agents through the standard Tool interface. This enables the self-improvement loop — agents can discover, activate, create, and refine skills at runtime.

**Package:** `github.com/nevindra/oasis/tools/skill`

### Actions

| Action           | Parameters                                                                               | Description                                              |
| ---------------- | ---------------------------------------------------------------------------------------- | -------------------------------------------------------- |
| `skill_discover` | _(none)_                                                                                 | List all skills as lightweight summaries (name + description + tags) |
| `skill_activate` | `name` (string, required)                                                                | Load full instructions for a named skill                 |
| `skill_create`   | `name`, `description`, `instructions` (required); `tags`, `tools`, `model`, `references` (optional) | Write a new SKILL.md to disk                 |
| `skill_update`   | `name` (required); all other fields optional                                             | Partial update — only provided fields change             |

### How Each Action Works

#### `skill_discover`

```mermaid
sequenceDiagram
    participant Agent
    participant SkillTool
    participant Provider as FileSkillProvider

    Agent->>SkillTool: skill_discover
    SkillTool->>Provider: Discover(ctx)
    Provider-->>SkillTool: []SkillSummary
    SkillTool-->>Agent: names, descriptions, tags<br>(no instructions)
```

Returns all skills as summaries — name, description, and tags only. No instruction text is loaded. The agent reads the list and decides which skill to activate.

#### `skill_activate`

```mermaid
sequenceDiagram
    participant Agent
    participant SkillTool
    participant Provider as FileSkillProvider

    Agent->>SkillTool: skill_activate("code-reviewer")
    SkillTool->>Provider: Activate(ctx, "code-reviewer")
    Provider-->>SkillTool: Skill (full instructions)
    SkillTool-->>Agent: name, description, instructions,<br>tools, model, tags, references
```

Loads a single skill by name. Returns the full `Skill` struct including the complete instruction text. The agent uses the instructions in its system prompt or reasoning.

#### `skill_create`

```mermaid
sequenceDiagram
    participant Agent
    participant SkillTool
    participant Writer as FileSkillProvider

    Agent->>SkillTool: skill_create(name, description, instructions, tags)
    SkillTool->>Writer: Create(ctx, skill)
    Note over Writer: Write skills/<name>/SKILL.md
    Writer-->>SkillTool: ok
    SkillTool-->>Agent: "created skill 'code-reviewer'"
```

Creates a new skill folder and `SKILL.md` on disk. The skill is immediately discoverable — no restart, no reload step.

#### `skill_update`

Uses a read-modify-write pattern. Reads the existing `SKILL.md`, applies only the provided fields, writes the result back.

```mermaid
sequenceDiagram
    participant Agent
    participant SkillTool
    participant Provider as FileSkillProvider

    Agent->>SkillTool: skill_update("code-reviewer", instructions="new text")
    SkillTool->>Provider: Activate(ctx, "code-reviewer")
    Provider-->>SkillTool: existing Skill
    Note over SkillTool: Merge: apply only provided fields
    SkillTool->>Provider: Update(ctx, merged skill)
    Provider-->>SkillTool: ok
    SkillTool-->>Agent: "updated skill 'code-reviewer': instructions"
```

Only provided fields are changed — omitted fields keep their current values. The updated `SKILL.md` is written to disk immediately.

---

## Agent Self-Improvement

The skill tool enables agents to encode reusable patterns by writing skills to disk:

```mermaid
flowchart LR
    A["Agent encounters<br>hard problem"] --> B["Agent solves it"]
    B --> C["Agent calls<br>skill_create"]
    C --> D["SKILL.md written<br>to disk"]
    D --> E["Later: similar<br>task arrives"]
    E --> F["Agent calls<br>skill_discover"]
    F --> G["Finds matching skill<br>in summary list"]
    G --> H["Agent calls<br>skill_activate"]
    H --> I["Full instructions<br>loaded"]
    I --> J["Better/faster<br>solution"]
    J -.->|"Agent may refine<br>via skill_update"| D
```

1. **Agent encounters a difficult task** and works through it
2. **Agent encodes the approach** by calling `skill_create` with a name, description, and instructions
3. **The skill is written to disk**, discoverable immediately
4. **Next time a similar task arrives**, the agent calls `skill_discover`
5. **The agent reads summaries** and recognizes the relevant skill
6. **Agent calls `skill_activate`** to load the full instructions
7. **Optionally**, the agent refines the skill via `skill_update` if it discovers improvements

This is not a framework feature you toggle on — it's an emergent behavior that arises when you give an agent the skill tool. The agent decides when to create and use skills based on its own judgment.

---

## Integration Pattern

Wire `FileSkillProvider` and the skill tool into an agent:

```go
import (
    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/skills"
    skilltool "github.com/nevindra/oasis/tools/skill"
)

// Create a provider pointing at your skills directory.
skillProvider := skills.NewFileSkillProvider("./skills")

// Create the skill tool — provider acts as both SkillProvider and SkillWriter.
tool := skilltool.New(skillProvider, skillProvider)

// Wire into an agent.
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", provider,
    oasis.WithTools(tool),
)
```

**Agent-driven skill creation flow** — a complete example of discover → activate → create:

```go
// The agent's system prompt should describe the self-improvement pattern:
const systemPrompt = `You are a helpful assistant.

At the start of complex tasks:
1. Call skill_discover to see available skills.
2. If a relevant skill exists, call skill_activate to load its instructions.
3. Apply the skill's instructions to improve your approach.

After solving a hard problem:
1. Call skill_create to encode your approach for future use.
2. Use a descriptive name and clear instructions.`

agent := oasis.NewLLMAgent("assistant", systemPrompt, provider,
    oasis.WithTools(tool),
)

// The agent will autonomously:
// 1. skill_discover → reads summary list
// 2. skill_activate("code-reviewer") → loads full instructions
// 3. Uses instructions to do the task
// 4. skill_create("new-pattern", ...) → writes SKILL.md for next time
result, _ := agent.Execute(ctx, oasis.AgentTask{
    Input: "Review this Go code for race conditions",
})
```

**Seeding skills from code:** Drop skill folders in your `skills/` directory before starting. They are picked up automatically — no initialization call needed:

```go
// Write a skill programmatically (e.g., for seeding).
skillProvider := skills.NewFileSkillProvider("./skills")

skill := oasis.Skill{
    Name:         "go-debugger",
    Description:  "Debug Go applications including race conditions and goroutine leaks",
    Instructions: "Use delve or print-based debugging. Check for goroutine leaks with runtime.NumGoroutine(). Look for data races with -race flag.",
    Tools:        []string{"shell_exec", "file_read"},
    Tags:         []string{"dev", "go", "debug"},
}

if err := skillProvider.Create(ctx, skill); err != nil {
    log.Fatal(err)
}
// skills/go-debugger/SKILL.md is now on disk, immediately discoverable.
```

---

## Skill Composability

The `References` field links skills into dependency chains. A composite skill references foundational skills by name, and the application can resolve those references at runtime to assemble a combined instruction set:

```go
// skills/full-stack-reviewer/SKILL.md references two other skills.
fullStack := oasis.Skill{
    Name:         "full-stack-reviewer",
    Description:  "Full-stack code review across frontend and backend",
    Instructions: "Review code holistically. Check API contracts, type safety, and UI consistency.",
    References:   []string{"frontend-review", "backend-review"},
    Tags:         []string{"dev", "review"},
}
```

To resolve references and merge instructions at runtime:

```go
func resolveSkill(ctx context.Context, provider oasis.SkillProvider, skill oasis.Skill) (string, error) {
    var parts []string

    // Load referenced skills first (depth-1 — no recursive resolution).
    for _, refName := range skill.References {
        ref, err := provider.Activate(ctx, refName)
        if err != nil {
            continue // skip broken references gracefully
        }
        parts = append(parts, fmt.Sprintf("## %s\n%s", ref.Name, ref.Instructions))
    }

    // Append the composite skill's own instructions last.
    parts = append(parts, skill.Instructions)
    return strings.Join(parts, "\n\n"), nil
}
```

The framework stores references as data — how you resolve them (depth-1, recursive, selective) is an application-level decision.

---

## Dynamic Tool Injection

The `Tools` field recommends which tools an agent should use when a skill is active. This is a recommendation, not enforcement — the application reads the field and configures the agent accordingly:

```go
// Read the active skill's tool list and filter.
func applySkillTools(skill oasis.Skill, allTools []oasis.Tool) []oasis.Tool {
    if len(skill.Tools) == 0 {
        return allTools // empty = no restriction
    }

    allowed := make(map[string]bool, len(skill.Tools))
    for _, name := range skill.Tools {
        allowed[name] = true
    }

    var filtered []oasis.Tool
    for _, tool := range allTools {
        for _, def := range tool.Definitions() {
            if allowed[def.Name] {
                filtered = append(filtered, tool)
                break
            }
        }
    }
    return filtered
}
```

---

## See Also

- [Store Concept](../concepts/store.md) — persistence layer
- [Tool Concept](../concepts/tool.md) — tool interface and built-in tools
