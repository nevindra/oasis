# File-Based Skills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace database-stored skills with file-based skills (folders with `SKILL.md`) to enable progressive disclosure, bundled resources, git-native versioning, and hot-reload without restart.

**Architecture:** Skills become folders on disk. A `SkillProvider` interface abstracts discovery and activation. `FileSkillProvider` is the first (and only) implementation -- it scans directories, parses YAML frontmatter for lightweight discovery, and reads full markdown on activation. The skill tool delegates to `SkillProvider` instead of `Store` + `EmbeddingProvider`. All DB-backed skill storage is removed from `Store`.

**Tech Stack:** Go stdlib (`os`, `path/filepath`, `bufio`, `strings`) + hand-rolled YAML frontmatter parser (~80 lines for the flat subset we need). No new dependencies.

---

## File Structure

```
Modified:
  types.go                          -- Remove skill methods from Store, add SkillProvider/SkillWriter interfaces + SkillSummary type, update Skill struct
  testhelpers_test.go               -- Remove nopStore skill method stubs
  tools/skill/skill.go              -- Rewrite to use SkillProvider instead of Store+Embedding
  tools/skill/skill_test.go         -- Rewrite tests against file-based provider
  store/sqlite/sqlite.go            -- Remove skills table from Init schema
  store/postgres/postgres.go        -- Remove skills table from Init schema

Created:
  skill.go                          -- FileSkillProvider implementation + frontmatter parser
  skill_test.go                     -- Tests for FileSkillProvider (discover, activate, create, update, delete)

Deleted:
  store/sqlite/skills.go            -- SQLite skill CRUD + search (entire file)
  store/postgres/skills.go          -- Postgres skill CRUD + search (entire file)

Docs updated:
  docs/guides/skills.md             -- Full rewrite for file-based model
  docs/api/types.md                 -- Update Skill struct, add SkillProvider/SkillWriter
  docs/api/interfaces.md            -- Add SkillProvider/SkillWriter interface docs
  CHANGELOG.md                      -- Breaking change entry
```

### SKILL.md Format

```markdown
---
name: code-reviewer
description: Review code changes for quality, correctness, and style
tags: [dev, review]
tools: [shell_exec, file_read]
model: ""
references: [go-debugger]
---

# Code Reviewer

## When to use
Use this skill when reviewing pull requests or code changes...

## Instructions
1. Read the changed files...
```

Frontmatter fields:
- `name` (required) -- short identifier, must match folder name
- `description` (required) -- when to use this skill (used for discovery)
- `tags` (optional) -- categorization labels
- `tools` (optional) -- restrict available tools
- `model` (optional) -- override default LLM model
- `references` (optional) -- names of other skills this builds on

The markdown body below the frontmatter is the `Instructions` field.

### YAML Frontmatter Parser Decision

No YAML library exists in go.mod. The agentskills.io frontmatter is a flat subset of YAML: string scalars and inline arrays (`[a, b, c]`). A hand-rolled parser handles this in ~80 lines without adding a dependency, per ENGINEERING.md: "Can stdlib or <200 lines hand-rolled solve it? Don't add the dep."

Supported subset:
- `key: value` -- string scalar
- `key: [a, b, c]` -- inline array
- `key: ""` -- empty string
- Lines starting with `#` inside frontmatter -- comments (ignored)

---

## Task 1: Define SkillProvider Interface and Update Skill Type

**Files:**
- Modify: `types.go:215-249` (remove skill methods from Store, add interfaces)
- Modify: `types.go:430-443` (update Skill struct)
- Test: compile check only (interfaces + types)

- [ ] **Step 1: Remove skill methods from Store interface**

In `types.go`, remove lines 215-223 (the `// --- Skills ---` section) from the `Store` interface:

```go
// Remove this entire block from Store interface:
// --- Skills ---
CreateSkill(ctx context.Context, skill Skill) error
GetSkill(ctx context.Context, id string) (Skill, error)
ListSkills(ctx context.Context) ([]Skill, error)
UpdateSkill(ctx context.Context, skill Skill) error
DeleteSkill(ctx context.Context, id string) error
// SearchSkills performs semantic similarity search over stored skills.
// Results are sorted by Score descending.
SearchSkills(ctx context.Context, embedding []float32, topK int) ([]ScoredSkill, error)
```

- [ ] **Step 2: Add SkillProvider and SkillWriter interfaces**

Add after the `Store` interface definition in `types.go`:

```go
// SkillProvider discovers and loads skills from any backing store.
// Implementations must be safe for concurrent use.
type SkillProvider interface {
	// Discover returns lightweight summaries of all available skills.
	// Only name, description, and tags are loaded -- full instructions remain unread.
	// Results are rescanned on every call (no caching), so newly created skills
	// are immediately visible without restart.
	Discover(ctx context.Context) ([]SkillSummary, error)

	// Activate loads the full skill by name, including instructions and metadata.
	// Returns an error if the skill does not exist.
	Activate(ctx context.Context, name string) (Skill, error)
}

// SkillWriter creates and modifies skills. File-based providers implement this
// to let agents author skills at runtime. Check via type assertion:
//
//	if w, ok := provider.(SkillWriter); ok { ... }
type SkillWriter interface {
	// CreateSkill writes a new skill. The Name field determines the folder name.
	CreateSkill(ctx context.Context, skill Skill) error

	// UpdateSkill modifies an existing skill identified by name.
	UpdateSkill(ctx context.Context, name string, skill Skill) error

	// DeleteSkill removes a skill and its entire folder.
	DeleteSkill(ctx context.Context, name string) error
}
```

- [ ] **Step 3: Add SkillSummary type**

Add near the Skill type in `types.go`:

```go
// SkillSummary is a lightweight view of a skill for discovery.
// Contains only the metadata needed for an agent to decide whether to activate.
type SkillSummary struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
}
```

- [ ] **Step 4: Update Skill struct**

Replace the current Skill struct with:

```go
// Skill is a stored instruction package that specializes agent behavior.
// Skills are folders on disk with a SKILL.md file containing YAML frontmatter
// (metadata) and markdown body (instructions).
type Skill struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Tools        []string `json:"tools,omitempty"`
	Model        string   `json:"model,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	References   []string `json:"references,omitempty"`
	Dir          string   `json:"-"` // filesystem path to skill directory
}
```

Removed fields: `ID`, `Embedding`, `CreatedBy`, `CreatedAt`, `UpdatedAt` (all DB-specific).
Added field: `Dir` (path to skill folder on disk).

- [ ] **Step 5: Remove ScoredSkill type**

Delete the `ScoredSkill` struct (no longer needed -- discovery returns all skills, no scoring):

```go
// Delete this:
// ScoredSkill is a Skill paired with its cosine similarity score.
type ScoredSkill struct {
	Skill
	Score float32
}
```

- [ ] **Step 6: Verify compilation**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./...`
Expected: FAIL -- Store implementations and skill tool still reference removed methods. This is expected; subsequent tasks fix each consumer.

---

## Task 2: Remove Skill Methods from Store Implementations and Test Helpers

**Files:**
- Delete: `store/sqlite/skills.go` (entire file)
- Delete: `store/postgres/skills.go` (entire file)
- Modify: `store/sqlite/sqlite.go:170-190` (remove skills table from Init)
- Modify: `store/postgres/postgres.go:237-251` (remove skills table from Init)
- Modify: `testhelpers_test.go:38-43` (remove nopStore skill stubs)
- Modify: `tools/skill/skill_test.go:113-120` (remove nopStore skill stubs)

- [ ] **Step 1: Delete store/sqlite/skills.go**

Run: `rm /home/nezhifi/Code/LLM/oasis/store/sqlite/skills.go`

- [ ] **Step 2: Delete store/postgres/skills.go**

Run: `rm /home/nezhifi/Code/LLM/oasis/store/postgres/skills.go`

- [ ] **Step 3: Remove skills table creation from SQLite Init**

In `store/sqlite/sqlite.go`, find and remove the skills table creation block (around line 170):

```go
// Remove this block:
// Skills
_, err = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS skills (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    instructions TEXT NOT NULL,
    tools TEXT,
    model TEXT,
    tags TEXT,
    created_by TEXT,
    refs TEXT,
    embedding BLOB,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
)`)
if err != nil {
    return fmt.Errorf("create skills table: %w", err)
}
```

Note: Read the exact content of `store/sqlite/sqlite.go:165-195` before editing to get the precise block boundaries and error variable reuse.

- [ ] **Step 4: Remove skills table creation from Postgres Init**

In `store/postgres/postgres.go`, find and remove the skills table creation from the schema statements array (around line 237). Read `store/postgres/postgres.go:230-255` for exact content.

- [ ] **Step 5: Remove nopStore skill stubs from testhelpers_test.go**

In `testhelpers_test.go`, remove lines 38-43:

```go
// Remove these 6 lines:
func (nopStore) CreateSkill(_ context.Context, _ Skill) error                        { return nil }
func (nopStore) GetSkill(_ context.Context, _ string) (Skill, error)                 { return Skill{}, nil }
func (nopStore) ListSkills(_ context.Context) ([]Skill, error)                       { return nil, nil }
func (nopStore) UpdateSkill(_ context.Context, _ Skill) error                        { return nil }
func (nopStore) DeleteSkill(_ context.Context, _ string) error                       { return nil }
func (nopStore) SearchSkills(_ context.Context, _ []float32, _ int) ([]ScoredSkill, error) { return nil, nil }
```

- [ ] **Step 6: Verify Store implementations compile**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./store/... && go build .`
Expected: PASS for store packages. Root package may still fail if skill tool references removed types.

- [ ] **Step 7: Run store tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./store/sqlite/ -v -count=1`
Expected: PASS (skill tests gone, remaining tests unaffected)

- [ ] **Step 8: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis
git add types.go testhelpers_test.go store/sqlite/skills.go store/sqlite/sqlite.go store/postgres/skills.go store/postgres/postgres.go
git commit -m "feat(skills)!: remove DB-backed skills from Store interface

BREAKING: Skill CRUD and SearchSkills removed from Store interface.
Skills are moving to file-based SkillProvider (next commits).
Migration: replace store.CreateSkill/SearchSkills with SkillProvider.Discover/Activate."
```

---

## Task 3: Implement FileSkillProvider -- Frontmatter Parser

**Files:**
- Create: `skill.go`
- Create: `skill_test.go` (add frontmatter parser tests)

- [ ] **Step 1: Write failing test for frontmatter parsing**

Create `skill_test.go`:

```go
package oasis

import (
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	input := `---
name: code-reviewer
description: Review code changes for quality
tags: [dev, review]
tools: [shell_exec, file_read]
model: gpt-4
references: [go-debugger, testing-best-practices]
---

# Code Reviewer

Use this skill when reviewing PRs.`

	fm, body, err := parseFrontmatter(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseFrontmatter error: %v", err)
	}

	if fm["name"] != "code-reviewer" {
		t.Errorf("name = %q, want %q", fm["name"], "code-reviewer")
	}
	if fm["description"] != "Review code changes for quality" {
		t.Errorf("description = %q", fm["description"])
	}
	if fm["tags"] != "dev, review" {
		t.Errorf("tags = %q, want %q", fm["tags"], "dev, review")
	}
	if fm["tools"] != "shell_exec, file_read" {
		t.Errorf("tools = %q, want %q", fm["tools"], "shell_exec, file_read")
	}
	if fm["model"] != "gpt-4" {
		t.Errorf("model = %q", fm["model"])
	}
	if fm["references"] != "go-debugger, testing-best-practices" {
		t.Errorf("references = %q", fm["references"])
	}

	wantBody := "# Code Reviewer\n\nUse this skill when reviewing PRs."
	if strings.TrimSpace(body) != wantBody {
		t.Errorf("body = %q, want %q", strings.TrimSpace(body), wantBody)
	}
}

func TestParseFrontmatterNoFrontmatter(t *testing.T) {
	input := "# Just markdown\n\nNo frontmatter here."
	_, _, err := parseFrontmatter(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseFrontmatterEmpty(t *testing.T) {
	input := "---\n---\n\nBody only."
	fm, body, err := parseFrontmatter(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fm) != 0 {
		t.Errorf("expected empty frontmatter, got %v", fm)
	}
	if strings.TrimSpace(body) != "Body only." {
		t.Errorf("body = %q", strings.TrimSpace(body))
	}
}

func TestParseFrontmatterComment(t *testing.T) {
	input := "---\nname: test\n# this is a comment\ndescription: desc\n---\n"
	fm, _, err := parseFrontmatter(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm["name"] != "test" {
		t.Errorf("name = %q", fm["name"])
	}
	if fm["description"] != "desc" {
		t.Errorf("description = %q", fm["description"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestParseFrontmatter -v -count=1`
Expected: FAIL -- `parseFrontmatter` undefined

- [ ] **Step 3: Implement frontmatter parser**

Create `skill.go`:

```go
package oasis

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// parseFrontmatter extracts YAML frontmatter and body from a reader.
// Frontmatter must be delimited by "---" lines. Returns key-value pairs
// where array values like [a, b] are stored as "a, b" strings.
func parseFrontmatter(r io.Reader) (map[string]string, string, error) {
	scanner := bufio.NewScanner(r)

	// First line must be "---".
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return nil, "", fmt.Errorf("missing frontmatter: expected '---' on first line")
	}

	fm := make(map[string]string)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		// Skip comments.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		// Parse "key: value".
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// Unwrap inline arrays: [a, b, c] -> "a, b, c".
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			val = val[1 : len(val)-1]
		}
		// Strip surrounding quotes.
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		fm[key] = val
	}

	// Remaining lines are the body.
	var body strings.Builder
	for scanner.Scan() {
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(scanner.Text())
	}

	return fm, body.String(), scanner.Err()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestParseFrontmatter -v -count=1`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis
git add skill.go skill_test.go
git commit -m "feat(skills): add YAML frontmatter parser for SKILL.md files"
```

---

## Task 4: Implement FileSkillProvider -- Discover and Activate

**Files:**
- Modify: `skill.go` (add FileSkillProvider, Discover, Activate)
- Modify: `skill_test.go` (add FileSkillProvider tests with temp directories)

- [ ] **Step 1: Write failing test for Discover**

Add to `skill_test.go`:

```go
import (
	"context"
	"os"
	"path/filepath"
	// ... existing imports
)

func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileSkillProvider_Discover(t *testing.T) {
	dir := t.TempDir()

	writeSkillFile(t, dir, "code-reviewer", `---
name: code-reviewer
description: Review code changes
tags: [dev, review]
---

# Code Reviewer instructions here.
`)

	writeSkillFile(t, dir, "sql-optimizer", `---
name: sql-optimizer
description: Optimize SQL queries
---

# SQL Optimizer instructions here.
`)

	provider := NewFileSkillProvider(dir)
	summaries, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(summaries))
	}

	// Check that summaries contain expected data.
	found := map[string]SkillSummary{}
	for _, s := range summaries {
		found[s.Name] = s
	}
	cr, ok := found["code-reviewer"]
	if !ok {
		t.Fatal("missing code-reviewer")
	}
	if cr.Description != "Review code changes" {
		t.Errorf("description = %q", cr.Description)
	}
	if len(cr.Tags) != 2 || cr.Tags[0] != "dev" {
		t.Errorf("tags = %v", cr.Tags)
	}
}

func TestFileSkillProvider_DiscoverEmpty(t *testing.T) {
	dir := t.TempDir()
	provider := NewFileSkillProvider(dir)
	summaries, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected 0 skills, got %d", len(summaries))
	}
}

func TestFileSkillProvider_DiscoverMultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	writeSkillFile(t, dir1, "skill-a", "---\nname: skill-a\ndescription: A\n---\nBody A")
	writeSkillFile(t, dir2, "skill-b", "---\nname: skill-b\ndescription: B\n---\nBody B")

	provider := NewFileSkillProvider(dir1, dir2)
	summaries, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(summaries))
	}
}

func TestFileSkillProvider_DiscoverSkipsNonDirs(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "valid-skill", "---\nname: valid-skill\ndescription: Valid\n---\nBody")
	// Create a regular file (not a directory) -- should be skipped.
	os.WriteFile(filepath.Join(dir, "not-a-dir.txt"), []byte("ignore me"), 0o644)

	provider := NewFileSkillProvider(dir)
	summaries, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(summaries) != 1 {
		t.Errorf("expected 1 skill, got %d", len(summaries))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_Discover -v -count=1`
Expected: FAIL -- `NewFileSkillProvider` undefined

- [ ] **Step 3: Implement FileSkillProvider with Discover**

Add to `skill.go`:

```go
import (
	"context"
	"os"
	"path/filepath"
	"sort"
	// ... existing imports
)

// FileSkillProvider discovers and manages skills stored as folders on disk.
// Each skill is a directory containing a SKILL.md file with YAML frontmatter
// and markdown instructions. Discovery rescans on every call -- no caching,
// no restart needed when skills are added or removed.
type FileSkillProvider struct {
	dirs []string
}

// NewFileSkillProvider creates a provider that scans the given directories
// for skill folders. Directories are scanned in order; if the same skill name
// appears in multiple directories, the first one wins. Non-existent directories
// are silently skipped.
func NewFileSkillProvider(dirs ...string) *FileSkillProvider {
	return &FileSkillProvider{dirs: dirs}
}

// Discover scans all configured directories and returns a summary for each
// valid skill folder. Only frontmatter is parsed -- instructions are not loaded.
func (p *FileSkillProvider) Discover(_ context.Context) ([]SkillSummary, error) {
	seen := make(map[string]bool)
	var summaries []SkillSummary

	for _, dir := range p.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read skill dir %s: %w", dir, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if seen[name] {
				continue
			}

			skillPath := filepath.Join(dir, name, "SKILL.md")
			f, err := os.Open(skillPath)
			if err != nil {
				continue // no SKILL.md -- skip
			}

			fm, _, err := parseFrontmatter(f)
			f.Close()
			if err != nil {
				continue // malformed frontmatter -- skip
			}

			skillName := fm["name"]
			if skillName == "" {
				skillName = name // fall back to folder name
			}

			seen[name] = true
			summaries = append(summaries, SkillSummary{
				Name:        skillName,
				Description: fm["description"],
				Tags:        splitCSV(fm["tags"]),
			})
		}
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Name < summaries[j].Name
	})
	return summaries, nil
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 4: Run Discover tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_Discover -v -count=1`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Write failing test for Activate**

Add to `skill_test.go`:

```go
func TestFileSkillProvider_Activate(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "code-reviewer", `---
name: code-reviewer
description: Review code changes
tags: [dev, review]
tools: [shell_exec, file_read]
model: gpt-4
references: [go-debugger]
---

# Code Reviewer

Analyze code for correctness and style.`)

	provider := NewFileSkillProvider(dir)
	skill, err := provider.Activate(context.Background(), "code-reviewer")
	if err != nil {
		t.Fatalf("Activate error: %v", err)
	}

	if skill.Name != "code-reviewer" {
		t.Errorf("name = %q", skill.Name)
	}
	if skill.Description != "Review code changes" {
		t.Errorf("description = %q", skill.Description)
	}
	if skill.Instructions != "# Code Reviewer\n\nAnalyze code for correctness and style." {
		t.Errorf("instructions = %q", skill.Instructions)
	}
	if len(skill.Tools) != 2 || skill.Tools[0] != "shell_exec" {
		t.Errorf("tools = %v", skill.Tools)
	}
	if skill.Model != "gpt-4" {
		t.Errorf("model = %q", skill.Model)
	}
	if len(skill.Tags) != 2 {
		t.Errorf("tags = %v", skill.Tags)
	}
	if len(skill.References) != 1 || skill.References[0] != "go-debugger" {
		t.Errorf("references = %v", skill.References)
	}
	expectedDir := filepath.Join(dir, "code-reviewer")
	if skill.Dir != expectedDir {
		t.Errorf("dir = %q, want %q", skill.Dir, expectedDir)
	}
}

func TestFileSkillProvider_ActivateNotFound(t *testing.T) {
	dir := t.TempDir()
	provider := NewFileSkillProvider(dir)
	_, err := provider.Activate(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}

func TestFileSkillProvider_ActivateMultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeSkillFile(t, dir2, "only-in-dir2", "---\nname: only-in-dir2\ndescription: D2\n---\nBody")

	provider := NewFileSkillProvider(dir1, dir2)
	skill, err := provider.Activate(context.Background(), "only-in-dir2")
	if err != nil {
		t.Fatalf("Activate error: %v", err)
	}
	if skill.Name != "only-in-dir2" {
		t.Errorf("name = %q", skill.Name)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_Activate -v -count=1`
Expected: FAIL -- `Activate` method not found

- [ ] **Step 7: Implement Activate**

Add to `skill.go`:

```go
// Activate reads the full SKILL.md for the named skill and returns a complete
// Skill value. Searches directories in order; returns the first match.
func (p *FileSkillProvider) Activate(_ context.Context, name string) (Skill, error) {
	for _, dir := range p.dirs {
		skillDir := filepath.Join(dir, name)
		skillPath := filepath.Join(skillDir, "SKILL.md")

		f, err := os.Open(skillPath)
		if err != nil {
			continue
		}
		fm, body, err := parseFrontmatter(f)
		f.Close()
		if err != nil {
			return Skill{}, fmt.Errorf("parse skill %s: %w", name, err)
		}

		skillName := fm["name"]
		if skillName == "" {
			skillName = name
		}

		return Skill{
			Name:         skillName,
			Description:  fm["description"],
			Instructions: strings.TrimSpace(body),
			Tools:        splitCSV(fm["tools"]),
			Model:        fm["model"],
			Tags:         splitCSV(fm["tags"]),
			References:   splitCSV(fm["references"]),
			Dir:          skillDir,
		}, nil
	}
	return Skill{}, fmt.Errorf("skill not found: %s", name)
}
```

- [ ] **Step 8: Run tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_Activate -v -count=1`
Expected: PASS (all 3 tests)

- [ ] **Step 9: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis
git add skill.go skill_test.go
git commit -m "feat(skills): implement FileSkillProvider with Discover and Activate"
```

---

## Task 5: Implement FileSkillProvider -- Create, Update, Delete (SkillWriter)

**Files:**
- Modify: `skill.go` (add SkillWriter methods + `renderSkillMD` helper)
- Modify: `skill_test.go` (add write operation tests)

- [ ] **Step 1: Write failing test for CreateSkill**

Add to `skill_test.go`:

```go
func TestFileSkillProvider_CreateSkill(t *testing.T) {
	dir := t.TempDir()
	provider := NewFileSkillProvider(dir)

	err := provider.CreateSkill(context.Background(), Skill{
		Name:         "new-skill",
		Description:  "A brand new skill",
		Instructions: "# New Skill\n\nDo the thing.",
		Tags:         []string{"test"},
		Tools:        []string{"shell_exec"},
	})
	if err != nil {
		t.Fatalf("CreateSkill error: %v", err)
	}

	// Verify it exists on disk.
	content, err := os.ReadFile(filepath.Join(dir, "new-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("SKILL.md not found: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, "name: new-skill") {
		t.Errorf("missing name in SKILL.md:\n%s", s)
	}
	if !strings.Contains(s, "description: A brand new skill") {
		t.Errorf("missing description in SKILL.md:\n%s", s)
	}
	if !strings.Contains(s, "# New Skill") {
		t.Errorf("missing body in SKILL.md:\n%s", s)
	}

	// Verify it's immediately discoverable (no restart).
	summaries, err := provider.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Name != "new-skill" {
		t.Errorf("expected to find new-skill via Discover, got %v", summaries)
	}
}

func TestFileSkillProvider_CreateSkillAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	provider := NewFileSkillProvider(dir)
	writeSkillFile(t, dir, "existing", "---\nname: existing\ndescription: Old\n---\nOld body")

	err := provider.CreateSkill(context.Background(), Skill{
		Name:        "existing",
		Description: "New",
	})
	if err == nil {
		t.Fatal("expected error for duplicate skill name")
	}
}

func TestFileSkillProvider_CreateSkillNoDirs(t *testing.T) {
	provider := NewFileSkillProvider() // no directories
	err := provider.CreateSkill(context.Background(), Skill{Name: "x", Description: "x"})
	if err == nil {
		t.Fatal("expected error when no directories configured")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_CreateSkill -v -count=1`
Expected: FAIL -- `CreateSkill` method not found

- [ ] **Step 3: Implement CreateSkill and renderSkillMD**

Add to `skill.go`:

```go
// Compile-time check: FileSkillProvider implements both interfaces.
var _ SkillProvider = (*FileSkillProvider)(nil)
var _ SkillWriter = (*FileSkillProvider)(nil)

// CreateSkill writes a new skill folder with a SKILL.md file.
// Uses the first configured directory as the write target.
// Returns an error if the skill already exists or no directories are configured.
func (p *FileSkillProvider) CreateSkill(_ context.Context, skill Skill) error {
	if len(p.dirs) == 0 {
		return fmt.Errorf("no skill directories configured")
	}
	if skill.Name == "" {
		return fmt.Errorf("skill name is required")
	}

	skillDir := filepath.Join(p.dirs[0], skill.Name)
	if _, err := os.Stat(skillDir); err == nil {
		return fmt.Errorf("skill already exists: %s", skill.Name)
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}

	content := renderSkillMD(skill)
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		os.RemoveAll(skillDir) // clean up on failure
		return fmt.Errorf("write SKILL.md: %w", err)
	}
	return nil
}

// renderSkillMD produces the SKILL.md content from a Skill value.
func renderSkillMD(skill Skill) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + skill.Name + "\n")
	b.WriteString("description: " + skill.Description + "\n")
	if len(skill.Tags) > 0 {
		b.WriteString("tags: [" + strings.Join(skill.Tags, ", ") + "]\n")
	}
	if len(skill.Tools) > 0 {
		b.WriteString("tools: [" + strings.Join(skill.Tools, ", ") + "]\n")
	}
	if skill.Model != "" {
		b.WriteString("model: " + skill.Model + "\n")
	}
	if len(skill.References) > 0 {
		b.WriteString("references: [" + strings.Join(skill.References, ", ") + "]\n")
	}
	b.WriteString("---\n")
	if skill.Instructions != "" {
		b.WriteString("\n" + skill.Instructions + "\n")
	}
	return b.String()
}
```

- [ ] **Step 4: Run CreateSkill tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_CreateSkill -v -count=1`
Expected: PASS (all 3 tests)

- [ ] **Step 5: Write failing test for UpdateSkill**

Add to `skill_test.go`:

```go
func TestFileSkillProvider_UpdateSkill(t *testing.T) {
	dir := t.TempDir()
	provider := NewFileSkillProvider(dir)
	writeSkillFile(t, dir, "my-skill", "---\nname: my-skill\ndescription: Old desc\ntags: [old]\n---\n\nOld instructions.")

	err := provider.UpdateSkill(context.Background(), "my-skill", Skill{
		Name:         "my-skill",
		Description:  "New desc",
		Instructions: "New instructions.",
		Tags:         []string{"new", "updated"},
	})
	if err != nil {
		t.Fatalf("UpdateSkill error: %v", err)
	}

	// Verify update took effect.
	skill, err := provider.Activate(context.Background(), "my-skill")
	if err != nil {
		t.Fatalf("Activate error: %v", err)
	}
	if skill.Description != "New desc" {
		t.Errorf("description = %q, want %q", skill.Description, "New desc")
	}
	if skill.Instructions != "New instructions." {
		t.Errorf("instructions = %q", skill.Instructions)
	}
	if len(skill.Tags) != 2 || skill.Tags[0] != "new" {
		t.Errorf("tags = %v", skill.Tags)
	}
}

func TestFileSkillProvider_UpdateSkillNotFound(t *testing.T) {
	dir := t.TempDir()
	provider := NewFileSkillProvider(dir)
	err := provider.UpdateSkill(context.Background(), "nonexistent", Skill{Name: "x"})
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_UpdateSkill -v -count=1`
Expected: FAIL -- `UpdateSkill` method not found

- [ ] **Step 7: Implement UpdateSkill**

Add to `skill.go`:

```go
// UpdateSkill rewrites the SKILL.md for an existing skill.
// Returns an error if the skill directory does not exist in any configured dir.
func (p *FileSkillProvider) UpdateSkill(_ context.Context, name string, skill Skill) error {
	for _, dir := range p.dirs {
		skillDir := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			continue
		}
		content := renderSkillMD(skill)
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
			return fmt.Errorf("write SKILL.md: %w", err)
		}
		return nil
	}
	return fmt.Errorf("skill not found: %s", name)
}
```

- [ ] **Step 8: Run UpdateSkill tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_UpdateSkill -v -count=1`
Expected: PASS

- [ ] **Step 9: Write failing test for DeleteSkill**

Add to `skill_test.go`:

```go
func TestFileSkillProvider_DeleteSkill(t *testing.T) {
	dir := t.TempDir()
	provider := NewFileSkillProvider(dir)
	writeSkillFile(t, dir, "doomed", "---\nname: doomed\ndescription: Bye\n---\nGone soon.")

	// Verify it exists.
	_, err := provider.Activate(context.Background(), "doomed")
	if err != nil {
		t.Fatalf("skill should exist before delete: %v", err)
	}

	err = provider.DeleteSkill(context.Background(), "doomed")
	if err != nil {
		t.Fatalf("DeleteSkill error: %v", err)
	}

	// Verify it's gone.
	_, err = provider.Activate(context.Background(), "doomed")
	if err == nil {
		t.Fatal("expected error after delete")
	}

	// Verify folder is removed.
	if _, err := os.Stat(filepath.Join(dir, "doomed")); !os.IsNotExist(err) {
		t.Error("expected skill directory to be removed")
	}
}

func TestFileSkillProvider_DeleteSkillNotFound(t *testing.T) {
	dir := t.TempDir()
	provider := NewFileSkillProvider(dir)
	err := provider.DeleteSkill(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent skill")
	}
}
```

- [ ] **Step 10: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_DeleteSkill -v -count=1`
Expected: FAIL -- `DeleteSkill` method not found

- [ ] **Step 11: Implement DeleteSkill**

Add to `skill.go`:

```go
// DeleteSkill removes a skill folder and all its contents.
// Returns an error if the skill does not exist in any configured directory.
func (p *FileSkillProvider) DeleteSkill(_ context.Context, name string) error {
	for _, dir := range p.dirs {
		skillDir := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			continue
		}
		if err := os.RemoveAll(skillDir); err != nil {
			return fmt.Errorf("delete skill %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("skill not found: %s", name)
}
```

- [ ] **Step 12: Run all FileSkillProvider tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider -v -count=1`
Expected: PASS (all tests)

- [ ] **Step 13: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis
git add skill.go skill_test.go
git commit -m "feat(skills): implement SkillWriter (create, update, delete) for FileSkillProvider"
```

---

## Task 6: Rewrite Skill Tool to Use SkillProvider

**Files:**
- Modify: `tools/skill/skill.go` (complete rewrite)
- Modify: `tools/skill/skill_test.go` (complete rewrite)

- [ ] **Step 1: Write failing tests for new skill tool**

Replace the entire contents of `tools/skill/skill_test.go`:

```go
package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oasis "github.com/nevindra/oasis"
)

func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestProvider(t *testing.T) (*oasis.FileSkillProvider, string) {
	t.Helper()
	dir := t.TempDir()
	return oasis.NewFileSkillProvider(dir), dir
}

func TestDefinitions(t *testing.T) {
	provider, _ := newTestProvider(t)
	tool := New(provider)
	defs := tool.Definitions()
	if len(defs) != 4 {
		t.Fatalf("expected 4 definitions, got %d", len(defs))
	}
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, want := range []string{"skill_discover", "skill_activate", "skill_create", "skill_update"} {
		if !names[want] {
			t.Errorf("missing definition %q", want)
		}
	}
}

func TestDiscover(t *testing.T) {
	provider, dir := newTestProvider(t)
	writeSkillFile(t, dir, "code-reviewer", "---\nname: code-reviewer\ndescription: Review code\ntags: [dev]\n---\nInstructions")
	writeSkillFile(t, dir, "sql-opt", "---\nname: sql-opt\ndescription: Optimize SQL\n---\nSQL stuff")

	tool := New(provider)
	result, err := tool.Execute(context.Background(), "skill_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "code-reviewer") {
		t.Errorf("expected code-reviewer in output: %s", result.Content)
	}
	if !strings.Contains(result.Content, "sql-opt") {
		t.Errorf("expected sql-opt in output: %s", result.Content)
	}
	// Instructions should NOT appear in discover output.
	if strings.Contains(result.Content, "Instructions") || strings.Contains(result.Content, "SQL stuff") {
		t.Errorf("discover should not include instructions: %s", result.Content)
	}
}

func TestDiscoverEmpty(t *testing.T) {
	provider, _ := newTestProvider(t)
	tool := New(provider)
	result, err := tool.Execute(context.Background(), "skill_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "no skills") {
		t.Errorf("expected 'no skills' message: %s", result.Content)
	}
}

func TestActivate(t *testing.T) {
	provider, dir := newTestProvider(t)
	writeSkillFile(t, dir, "code-reviewer", "---\nname: code-reviewer\ndescription: Review code\ntools: [file_read]\n---\n\n# Instructions\n\nReview carefully.")

	tool := New(provider)
	args, _ := json.Marshal(map[string]string{"name": "code-reviewer"})
	result, err := tool.Execute(context.Background(), "skill_activate", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "Review carefully") {
		t.Errorf("expected instructions in output: %s", result.Content)
	}
}

func TestActivateNotFound(t *testing.T) {
	provider, _ := newTestProvider(t)
	tool := New(provider)
	args, _ := json.Marshal(map[string]string{"name": "nonexistent"})
	result, err := tool.Execute(context.Background(), "skill_activate", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for nonexistent skill")
	}
}

func TestCreate(t *testing.T) {
	provider, dir := newTestProvider(t)
	tool := New(provider)

	args, _ := json.Marshal(map[string]any{
		"name":         "new-skill",
		"description":  "A new skill",
		"instructions": "# New\n\nDo the thing.",
		"tags":         []string{"test"},
	})
	result, err := tool.Execute(context.Background(), "skill_create", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "new-skill") {
		t.Errorf("expected skill name in output: %s", result.Content)
	}

	// Verify on disk.
	if _, err := os.Stat(filepath.Join(dir, "new-skill", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not created: %v", err)
	}
}

func TestCreateMissingFields(t *testing.T) {
	provider, _ := newTestProvider(t)
	tool := New(provider)
	args, _ := json.Marshal(map[string]string{"name": "incomplete"})
	result, err := tool.Execute(context.Background(), "skill_create", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for missing required fields")
	}
}

func TestUpdate(t *testing.T) {
	provider, dir := newTestProvider(t)
	writeSkillFile(t, dir, "my-skill", "---\nname: my-skill\ndescription: Old\n---\nOld body")

	tool := New(provider)
	args, _ := json.Marshal(map[string]any{
		"name":         "my-skill",
		"description":  "Updated desc",
		"instructions": "Updated body",
	})
	result, err := tool.Execute(context.Background(), "skill_update", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}

	// Verify update.
	skill, _ := provider.Activate(context.Background(), "my-skill")
	if skill.Description != "Updated desc" {
		t.Errorf("description = %q", skill.Description)
	}
	if skill.Instructions != "Updated body" {
		t.Errorf("instructions = %q", skill.Instructions)
	}
}

func TestUnknownAction(t *testing.T) {
	provider, _ := newTestProvider(t)
	tool := New(provider)
	result, err := tool.Execute(context.Background(), "skill_delete", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for unknown action")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./tools/skill/ -v -count=1`
Expected: FAIL -- constructor signature mismatch, missing actions

- [ ] **Step 3: Rewrite skill tool**

Replace the entire contents of `tools/skill/skill.go`:

```go
// Package skill exposes skill management to agents through the standard Tool interface.
// Agents can discover, activate, create, and update skills stored as folders on disk.
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// Tool manages skills -- file-based instruction packages that specialize agent behavior.
type Tool struct {
	provider oasis.SkillProvider
}

// Compile-time interface check.
var _ oasis.Tool = (*Tool)(nil)

// New creates a skill Tool backed by the given SkillProvider.
// If the provider also implements SkillWriter, create and update actions are enabled.
func New(provider oasis.SkillProvider) *Tool {
	return &Tool{provider: provider}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	defs := []oasis.ToolDefinition{
		{
			Name:        "skill_discover",
			Description: "List all available skills with their names and descriptions. Use this to find relevant skills before activating one.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "skill_activate",
			Description: "Load a skill's full instructions by name. Call skill_discover first to see available skills, then activate the most relevant one.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"name":{"type":"string","description":"Name of the skill to activate"}
			},"required":["name"]}`),
		},
	}

	// Only expose write operations if the provider supports them.
	if _, ok := t.provider.(oasis.SkillWriter); ok {
		defs = append(defs,
			oasis.ToolDefinition{
				Name:        "skill_create",
				Description: "Create a new skill as a folder with a SKILL.md file. The skill becomes immediately discoverable.",
				Parameters: json.RawMessage(`{"type":"object","properties":{
					"name":{"type":"string","description":"Short identifier (becomes folder name, e.g. code-reviewer)"},
					"description":{"type":"string","description":"When to use this skill -- shown during discovery"},
					"instructions":{"type":"string","description":"Full markdown instructions loaded when the skill is activated"},
					"tags":{"type":"array","items":{"type":"string"},"description":"Optional categorization labels"},
					"tools":{"type":"array","items":{"type":"string"},"description":"Optional tool names this skill should use (empty = all)"},
					"model":{"type":"string","description":"Optional model override"},
					"references":{"type":"array","items":{"type":"string"},"description":"Optional names of skills this builds on"}
				},"required":["name","description","instructions"]}`),
			},
			oasis.ToolDefinition{
				Name:        "skill_update",
				Description: "Update an existing skill's SKILL.md. Provide all fields -- they replace the current content entirely.",
				Parameters: json.RawMessage(`{"type":"object","properties":{
					"name":{"type":"string","description":"Name of the skill to update"},
					"description":{"type":"string","description":"Updated description"},
					"instructions":{"type":"string","description":"Updated instructions"},
					"tags":{"type":"array","items":{"type":"string"},"description":"Updated tags"},
					"tools":{"type":"array","items":{"type":"string"},"description":"Updated tool list"},
					"model":{"type":"string","description":"Updated model override"},
					"references":{"type":"array","items":{"type":"string"},"description":"Updated references"}
				},"required":["name"]}`),
			},
		)
	}

	return defs
}

func (t *Tool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
	var result string
	var err error

	switch name {
	case "skill_discover":
		result, err = t.handleDiscover(ctx)
	case "skill_activate":
		result, err = t.handleActivate(ctx, args)
	case "skill_create":
		result, err = t.handleCreate(ctx, args)
	case "skill_update":
		result, err = t.handleUpdate(ctx, args)
	default:
		return oasis.ToolResult{Error: "unknown skill action: " + name}, nil
	}

	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}
	return oasis.ToolResult{Content: result}, nil
}

func (t *Tool) handleDiscover(ctx context.Context) (string, error) {
	summaries, err := t.provider.Discover(ctx)
	if err != nil {
		return "", err
	}
	if len(summaries) == 0 {
		return "no skills available", nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%d skill(s) available:\n\n", len(summaries))
	for i, s := range summaries {
		fmt.Fprintf(&out, "%d. **%s** -- %s\n", i+1, s.Name, s.Description)
		if len(s.Tags) > 0 {
			fmt.Fprintf(&out, "   Tags: %s\n", strings.Join(s.Tags, ", "))
		}
	}
	out.WriteString("\nUse skill_activate to load full instructions for a skill.")
	return out.String(), nil
}

func (t *Tool) handleActivate(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	skill, err := t.provider.Activate(ctx, p.Name)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	fmt.Fprintf(&out, "# Skill: %s\n\n", skill.Name)
	fmt.Fprintf(&out, "**Description:** %s\n", skill.Description)
	if len(skill.Tools) > 0 {
		fmt.Fprintf(&out, "**Tools:** %s\n", strings.Join(skill.Tools, ", "))
	}
	if skill.Model != "" {
		fmt.Fprintf(&out, "**Model:** %s\n", skill.Model)
	}
	if len(skill.References) > 0 {
		fmt.Fprintf(&out, "**References:** %s\n", strings.Join(skill.References, ", "))
	}
	fmt.Fprintf(&out, "\n---\n\n%s", skill.Instructions)
	return out.String(), nil
}

func (t *Tool) handleCreate(ctx context.Context, args json.RawMessage) (string, error) {
	w, ok := t.provider.(oasis.SkillWriter)
	if !ok {
		return "", fmt.Errorf("skill provider does not support creation")
	}

	var p struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Instructions string   `json:"instructions"`
		Tags         []string `json:"tags"`
		Tools        []string `json:"tools"`
		Model        string   `json:"model"`
		References   []string `json:"references"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Name == "" || p.Description == "" || p.Instructions == "" {
		return "", fmt.Errorf("name, description, and instructions are required")
	}

	skill := oasis.Skill{
		Name:         p.Name,
		Description:  p.Description,
		Instructions: p.Instructions,
		Tools:        p.Tools,
		Model:        p.Model,
		Tags:         p.Tags,
		References:   p.References,
	}

	if err := w.CreateSkill(ctx, skill); err != nil {
		return "", err
	}
	return fmt.Sprintf("created skill %q", skill.Name), nil
}

func (t *Tool) handleUpdate(ctx context.Context, args json.RawMessage) (string, error) {
	w, ok := t.provider.(oasis.SkillWriter)
	if !ok {
		return "", fmt.Errorf("skill provider does not support updates")
	}

	var p struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Instructions string   `json:"instructions"`
		Tags         []string `json:"tags"`
		Tools        []string `json:"tools"`
		Model        string   `json:"model"`
		References   []string `json:"references"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	// Read existing, merge, write back.
	existing, err := t.provider.Activate(ctx, p.Name)
	if err != nil {
		return "", fmt.Errorf("skill not found: %w", err)
	}

	var changes []string
	if p.Description != "" {
		existing.Description = p.Description
		changes = append(changes, "description")
	}
	if p.Instructions != "" {
		existing.Instructions = p.Instructions
		changes = append(changes, "instructions")
	}
	if p.Tags != nil {
		existing.Tags = p.Tags
		changes = append(changes, "tags")
	}
	if p.Tools != nil {
		existing.Tools = p.Tools
		changes = append(changes, "tools")
	}
	if p.Model != "" {
		existing.Model = p.Model
		changes = append(changes, "model")
	}
	if p.References != nil {
		existing.References = p.References
		changes = append(changes, "references")
	}

	if len(changes) == 0 {
		return "no changes specified", nil
	}

	if err := w.UpdateSkill(ctx, p.Name, existing); err != nil {
		return "", err
	}
	return fmt.Sprintf("updated skill %q: %s", p.Name, strings.Join(changes, ", ")), nil
}
```

- [ ] **Step 4: Run all skill tool tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./tools/skill/ -v -count=1`
Expected: PASS (all tests)

- [ ] **Step 5: Verify full project compiles**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./...`
Expected: PASS -- all consumers now use the new interfaces.

- [ ] **Step 6: Run all tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./... -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis
git add tools/skill/skill.go tools/skill/skill_test.go
git commit -m "feat(skills): rewrite skill tool to use SkillProvider with progressive disclosure

skill_search -> skill_discover (lightweight, names + descriptions only)
New: skill_activate (load full instructions on demand)
skill_create/skill_update now write files via SkillWriter interface."
```

---

## Task 7: Update Documentation

**Files:**
- Modify: `docs/guides/skills.md` (major rewrite)
- Modify: `docs/api/types.md` (update Skill struct, add SkillSummary)
- Modify: `docs/api/interfaces.md` (add SkillProvider, SkillWriter)
- Modify: `CHANGELOG.md` (add breaking change entry)

- [ ] **Step 1: Read current docs to understand structure**

Read: `docs/guides/skills.md`, `docs/api/types.md`, `docs/api/interfaces.md`, `CHANGELOG.md`

- [ ] **Step 2: Rewrite docs/guides/skills.md**

Replace with content covering:
1. **File-based architecture** -- skills as folders with SKILL.md
2. **SKILL.md format** -- frontmatter fields + markdown body
3. **Progressive disclosure** -- discover (lightweight) then activate (full)
4. **FileSkillProvider** -- constructor, directory scanning, hot reload
5. **Skill Tool** -- 4 actions: discover, activate, create, update
6. **Agent self-improvement** -- agents create skills via skill_create
7. **Directory conventions** -- project-local `./skills/`, user-global `~/.config/oasis/skills/`
8. **Skill folder structure** -- SKILL.md + optional scripts/, references/, assets/
9. **Integration pattern** -- wiring FileSkillProvider into agent construction
10. **Migration from DB-backed skills** -- export existing skills as SKILL.md files

Include code examples for:
- Creating a FileSkillProvider
- Wiring the skill tool
- Writing a SKILL.md by hand
- Agent-driven skill creation flow

- [ ] **Step 3: Update docs/api/types.md**

Replace Skill struct definition with new version (no ID, Embedding, CreatedBy, timestamps; add Dir).
Add SkillSummary type.

- [ ] **Step 4: Update docs/api/interfaces.md**

Add SkillProvider and SkillWriter interface documentation.
Remove skill methods from Store interface docs.

- [ ] **Step 5: Update CHANGELOG.md**

Add under `[Unreleased]`:

```markdown
### Changed
- **BREAKING:** Skills are now file-based (folders with `SKILL.md`) instead of database-stored. Skill CRUD methods removed from `Store` interface. Use `SkillProvider` and `FileSkillProvider` instead.
- Skill tool now exposes `skill_discover` and `skill_activate` instead of `skill_search`. Progressive disclosure: discover returns names only, activate loads full instructions.

### Added
- `SkillProvider` interface for discovering and activating skills.
- `SkillWriter` interface for creating, updating, and deleting skills.
- `FileSkillProvider` -- reads skills from directories, hot-reloads without restart.
- `SkillSummary` type for lightweight discovery results.

### Removed
- `Store.CreateSkill`, `Store.GetSkill`, `Store.ListSkills`, `Store.UpdateSkill`, `Store.DeleteSkill`, `Store.SearchSkills` -- replaced by `SkillProvider`.
- `ScoredSkill` type -- no longer needed (no embedding-based search).
- `Skill.ID`, `Skill.Embedding`, `Skill.CreatedBy`, `Skill.CreatedAt`, `Skill.UpdatedAt` fields -- replaced by filesystem metadata.
- `store/sqlite/skills.go`, `store/postgres/skills.go` -- DB skill implementations.
```

- [ ] **Step 6: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis
git add docs/guides/skills.md docs/api/types.md docs/api/interfaces.md CHANGELOG.md
git commit -m "docs: update skills guide, API docs, and changelog for file-based skills"
```

---

## Summary

| Task | What | Files changed |
|------|------|---------------|
| 1 | Define interfaces, update types | `types.go` |
| 2 | Remove DB skill storage | `store/sqlite/skills.go` (delete), `store/postgres/skills.go` (delete), `store/sqlite/sqlite.go`, `store/postgres/postgres.go`, `testhelpers_test.go` |
| 3 | Frontmatter parser | `skill.go`, `skill_test.go` |
| 4 | FileSkillProvider discover + activate | `skill.go`, `skill_test.go` |
| 5 | FileSkillProvider create + update + delete | `skill.go`, `skill_test.go` |
| 6 | Rewrite skill tool | `tools/skill/skill.go`, `tools/skill/skill_test.go` |
| 7 | Documentation | `docs/guides/skills.md`, `docs/api/types.md`, `docs/api/interfaces.md`, `CHANGELOG.md` |

**Total new code:** ~300 lines (skill.go) + ~250 lines (tests) + ~100 lines (tool rewrite)
**Total deleted code:** ~460 lines (sqlite/skills.go + postgres/skills.go + Store skill methods + nopStore stubs)
**Net:** ~190 lines added, significant reduction in complexity (no embeddings, no DB schema, no JSON serialization)
**New dependencies:** none
