# Skills

Skills are stored instruction packages that specialize agent behavior. They live in the database and can be managed at runtime.

## What's in a Skill

```go
type Skill struct {
    ID           string    // unique identifier
    Name         string    // "code-reviewer"
    Description  string    // "Review code changes and suggest improvements"
    Instructions string    // injected into the agent's system prompt
    Tools        []string  // restrict available tools (empty = all)
    Model        string    // override default LLM model (empty = default)
    Embedding    []float32 // for semantic search
    CreatedAt    int64
    UpdatedAt    int64
}
```

## Creating a Skill

```go
skill := oasis.Skill{
    ID:           oasis.NewID(),
    Name:         "code-reviewer",
    Description:  "Review code changes and suggest improvements",
    Instructions: "You are a code reviewer. Analyze code for style, correctness, and performance.",
    Tools:        []string{"shell_exec", "file_read"},  // only these tools
    CreatedAt:    oasis.NowUnix(),
    UpdatedAt:    oasis.NowUnix(),
}

// Embed for semantic search
vectors, _ := embedding.Embed(ctx, []string{skill.Description})
skill.Embedding = vectors[0]

// Store
store.CreateSkill(ctx, skill)
```

## Searching Skills

Find skills by semantic similarity to a user's message:

```go
queryVec, _ := embedding.Embed(ctx, []string{"review my pull request"})
matches, _ := store.SearchSkills(ctx, queryVec[0], 3)
// matches[0].Name == "code-reviewer"
```

## Skill Resolution Pattern

The reference app uses two-stage resolution:

1. Embed user message and `SearchSkills` for top candidates
2. Ask an intent LLM to pick the best match (or "none")

This is an application-level pattern — the framework provides storage and search, you decide how to select and apply skills.

## See Also

- [Store Concept](../concepts/store.md)
