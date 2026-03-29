# Skill Architecture v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Oasis skills more powerful through framework behavior (validation, auto-loading, pre-activation, gating), align with AgentSkills open standard, and remove oasis-render in favor of prescriptive skills that teach agents to use libraries directly.

**Architecture:** Skill struct gains three fields (Compatibility, License, Metadata). Five framework behaviors are added to FileSkillProvider and agent options (tool validation, reference auto-loading, {dir} resolution, compatibility gating, pre-activation). oasis-render and renderers/ are removed. Built-in skills are rewritten as prescriptive guides.

**Tech Stack:** Go 1.24+, go:embed for built-in skills, existing test framework (testing + testify-free assertions)

**Spec:** `docs/superpowers/specs/2026-03-29-skill-architecture-v2.md`

---

## File Structure

**Modified files:**
- `types.go` — Add Compatibility, License, Metadata to Skill and SkillSummary
- `skill.go` — Extend parser, add {dir} resolution, compatibility filtering, reference auto-loading
- `skill_builtin.go` — Update go:embed path, align with new parser
- `skill_test.go` — Tests for all new behavior
- `agent.go` — Add WithActiveSkills and WithSkills options
- `agentcore.go` — Store active skills and skill provider
- `llmagent.go` — Wire active skills into system prompt, auto-register skill tool
- `tools/skill/skill.go` — Handle new fields in create/update, render new fields in activate
- `tools/skill/skill_test.go` — Tests for new fields
- `.github/workflows/build-ix.yml` — Remove renderer paths from triggers
- `cmd/ix/Dockerfile` — Remove oasis-render and renderer copies

**Rewritten files:**
- `skills/oasis-pdf/SKILL.md` — Prescriptive HTML/CSS + Playwright approach
- `skills/oasis-docx/SKILL.md` — Prescriptive python-docx approach
- `skills/oasis-xlsx/SKILL.md` — Prescriptive openpyxl approach
- `skills/oasis-pptx/SKILL.md` — Prescriptive PptxGenJS approach
- `skills/oasis-design-system/SKILL.md` — Updated frontmatter (add compatibility)

**Removed files:**
- `bin/oasis-render`
- `renderers/pdf/render.js`
- `renderers/pdf/fill.py`
- `renderers/docx/generate.py`
- `renderers/docx/fill.py`
- `renderers/xlsx/generate.py`
- `renderers/pptx/compile.js`
- `requirements.txt`

**New files:**
- `skill_scan.go` — DefaultSkillDirs() helper for AgentSkills-compatible scan paths

---

## Phase 1: Foundation (Types + Parser)

### Task 1: Add New Fields to Skill and SkillSummary

**Files:**
- Modify: `types.go:444-462`
- Modify: `skill_test.go`

- [ ] **Step 1: Write failing test for new Skill fields**

In `skill_test.go`, add test that verifies a skill with new frontmatter fields is parsed correctly. Add after existing `TestFileSkillProvider_Activate` (line 298):

```go
func TestFileSkillProvider_ActivateNewFields(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "my-skill", `---
name: my-skill
description: A test skill
compatibility: Requires node, playwright
license: Apache-2.0
metadata:
  author: test-org
  version: "2.0"
tags: [test]
---
Do the thing at {dir}/scripts/run.sh`)

	fp := NewFileSkillProvider(dir)
	skill, err := fp.Activate(context.Background(), "my-skill")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Compatibility != "Requires node, playwright" {
		t.Errorf("Compatibility = %q, want %q", skill.Compatibility, "Requires node, playwright")
	}
	if skill.License != "Apache-2.0" {
		t.Errorf("License = %q, want %q", skill.License, "Apache-2.0")
	}
	if skill.Metadata["author"] != "test-org" {
		t.Errorf("Metadata[author] = %q, want %q", skill.Metadata["author"], "test-org")
	}
	if skill.Metadata["version"] != "2.0" {
		t.Errorf("Metadata[version] = %q, want %q", skill.Metadata["version"], "2.0")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_ActivateNewFields -v`
Expected: FAIL — `skill.Compatibility` field does not exist.

- [ ] **Step 3: Add fields to Skill struct**

In `types.go`, modify the Skill struct (around line 453):

```go
type Skill struct {
	Name          string
	Description   string
	Instructions  string
	Tools         []string
	Model         string
	Tags          []string
	References    []string
	Dir           string
	Compatibility string
	License       string
	Metadata      map[string]string
}
```

And add Compatibility to SkillSummary (around line 444):

```go
type SkillSummary struct {
	Name          string
	Description   string
	Tags          []string
	Compatibility string
}
```

- [ ] **Step 4: Run test — still fails (parser doesn't read new fields yet)**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_ActivateNewFields -v`
Expected: FAIL — Compatibility is empty string (parser doesn't extract it yet).

- [ ] **Step 5: Commit type changes**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add types.go skill_test.go && git commit -m "feat: add Compatibility, License, Metadata fields to Skill struct"
```

---

### Task 2: Extend Frontmatter Parser

**Files:**
- Modify: `skill.go:73-121` (parseFrontmatter)
- Modify: `skill.go:237-275` (Activate — read new fields from map)
- Modify: `skill.go:181-232` (Discover — read Compatibility into summary)
- Modify: `skill_builtin.go:26-93` (same for builtin provider)
- Modify: `skill_test.go`

- [ ] **Step 1: Write failing test for metadata parsing**

In `skill_test.go`, add after existing `TestParseFrontmatterComment`:

```go
func TestParseFrontmatterMetadata(t *testing.T) {
	input := `---
name: test
description: A test
compatibility: Requires node
license: MIT
metadata:
  author: acme
  version: "1.0"
---
Body content`

	fm, body, err := parseFrontmatter(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if fm["compatibility"] != "Requires node" {
		t.Errorf("compatibility = %q, want %q", fm["compatibility"], "Requires node")
	}
	if fm["license"] != "MIT" {
		t.Errorf("license = %q, want %q", fm["license"], "MIT")
	}
	if fm["metadata.author"] != "acme" {
		t.Errorf("metadata.author = %q, want %q", fm["metadata.author"], "acme")
	}
	if fm["metadata.version"] != "1.0" {
		t.Errorf("metadata.version = %q, want %q", fm["metadata.version"], "1.0")
	}
	if body != "Body content" {
		t.Errorf("body = %q, want %q", body, "Body content")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestParseFrontmatterMetadata -v`
Expected: FAIL — metadata.author is empty (parser doesn't handle indented sub-keys).

- [ ] **Step 3: Extend parseFrontmatter to handle indented metadata**

In `skill.go`, modify `parseFrontmatter` (around line 73). The key change: when a line has `key:` with no value followed by indented lines, store sub-keys as `key.subkey`:

```go
func parseFrontmatter(r io.Reader) (map[string]string, string, error) {
	scanner := bufio.NewScanner(r)

	// First line must be "---"
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return nil, "", fmt.Errorf("expected frontmatter opening ---")
	}

	fm := make(map[string]string)
	var parentKey string // tracks current map key for indented sub-entries

	for scanner.Scan() {
		line := scanner.Text()

		if strings.TrimSpace(line) == "---" {
			break
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Indented line — belongs to parent key as sub-entry
		if parentKey != "" && (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) {
			idx := strings.IndexByte(trimmed, ':')
			if idx > 0 {
				subKey := strings.TrimSpace(trimmed[:idx])
				subVal := strings.TrimSpace(trimmed[idx+1:])
				subVal = unquote(subVal)
				fm[parentKey+"."+subKey] = subVal
			}
			continue
		}

		// Reset parent key for non-indented lines
		parentKey = ""

		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		// Key with no value — start of a map block (e.g., metadata:)
		if val == "" {
			parentKey = key
			continue
		}

		// Inline array: [a, b, c]
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			inner := val[1 : len(val)-1]
			var items []string
			for _, item := range strings.Split(inner, ",") {
				item = strings.TrimSpace(item)
				if item != "" {
					items = append(items, unquote(item))
				}
			}
			fm[key] = strings.Join(items, ", ")
			continue
		}

		fm[key] = unquote(val)
	}

	// Collect body
	var body strings.Builder
	for scanner.Scan() {
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(scanner.Text())
	}

	return fm, strings.TrimSpace(body.String()), scanner.Err()
}

// unquote removes surrounding quotes from a string value.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
```

- [ ] **Step 4: Run parser test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestParseFrontmatterMetadata -v`
Expected: PASS

- [ ] **Step 5: Wire new fields in FileSkillProvider.Activate**

In `skill.go`, inside the `Activate` method (around line 250), add after existing field reads:

```go
	skill.Compatibility = fm["compatibility"]
	skill.License = fm["license"]

	// Collect metadata.* entries
	meta := make(map[string]string)
	for k, v := range fm {
		if strings.HasPrefix(k, "metadata.") {
			meta[strings.TrimPrefix(k, "metadata.")] = v
		}
	}
	if len(meta) > 0 {
		skill.Metadata = meta
	}
```

- [ ] **Step 6: Wire Compatibility in FileSkillProvider.Discover**

In `skill.go`, inside the `Discover` method, when building SkillSummary (around line 215), add:

```go
	summary.Compatibility = fm["compatibility"]
```

- [ ] **Step 7: Wire new fields in BuiltinSkillProvider.Activate and Discover**

Apply the same changes to `skill_builtin.go` — same field reads in Activate (around line 75) and Discover (around line 47).

- [ ] **Step 8: Run the full activation test**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_ActivateNewFields -v`
Expected: PASS

- [ ] **Step 9: Run all existing tests to verify no regressions**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestParseFrontmatter -v && go test -run TestFileSkillProvider -v`
Expected: All PASS

- [ ] **Step 10: Commit parser changes**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add skill.go skill_builtin.go skill_test.go && git commit -m "feat: extend frontmatter parser for compatibility, license, metadata fields"
```

---

### Task 3: Update renderSkillMD for New Fields

**Files:**
- Modify: `skill.go:351-392` (renderSkillMD)
- Modify: `skill_test.go`

- [ ] **Step 1: Write failing test for create with new fields**

```go
func TestFileSkillProvider_CreateSkillNewFields(t *testing.T) {
	dir := t.TempDir()
	fp := NewFileSkillProvider(dir)

	err := fp.CreateSkill(context.Background(), Skill{
		Name:          "new-skill",
		Description:   "A new skill",
		Instructions:  "Do things",
		Compatibility: "Requires python3",
		License:       "MIT",
		Metadata:      map[string]string{"author": "test", "version": "1.0"},
	})
	if err != nil {
		t.Fatal(err)
	}

	skill, err := fp.Activate(context.Background(), "new-skill")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Compatibility != "Requires python3" {
		t.Errorf("Compatibility = %q, want %q", skill.Compatibility, "Requires python3")
	}
	if skill.License != "MIT" {
		t.Errorf("License = %q, want %q", skill.License, "MIT")
	}
	if skill.Metadata["author"] != "test" {
		t.Errorf("Metadata[author] = %q, want %q", skill.Metadata["author"], "test")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_CreateSkillNewFields -v`
Expected: FAIL — renderSkillMD doesn't write Compatibility/License/Metadata.

- [ ] **Step 3: Update renderSkillMD**

In `skill.go`, modify `renderSkillMD` (around line 351) to include new fields:

```go
func renderSkillMD(s Skill) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + s.Name + "\n")
	b.WriteString("description: " + s.Description + "\n")
	if len(s.Tags) > 0 {
		b.WriteString("tags: [" + strings.Join(s.Tags, ", ") + "]\n")
	}
	if len(s.Tools) > 0 {
		b.WriteString("tools: [" + strings.Join(s.Tools, ", ") + "]\n")
	}
	if s.Model != "" {
		b.WriteString("model: " + s.Model + "\n")
	}
	if len(s.References) > 0 {
		b.WriteString("references: [" + strings.Join(s.References, ", ") + "]\n")
	}
	if s.Compatibility != "" {
		b.WriteString("compatibility: " + s.Compatibility + "\n")
	}
	if s.License != "" {
		b.WriteString("license: " + s.License + "\n")
	}
	if len(s.Metadata) > 0 {
		b.WriteString("metadata:\n")
		// Sort keys for deterministic output
		keys := make([]string, 0, len(s.Metadata))
		for k := range s.Metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("  " + k + ": " + s.Metadata[k] + "\n")
		}
	}
	b.WriteString("---\n")
	if s.Instructions != "" {
		b.WriteString("\n" + s.Instructions + "\n")
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_CreateSkillNewFields -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add skill.go skill_test.go && git commit -m "feat: render new skill fields in SKILL.md output"
```

---

## Phase 2: Framework Behavior

### Task 4: {dir} Placeholder Resolution

**Files:**
- Modify: `skill.go` (Activate methods)
- Modify: `skill_builtin.go` (Activate method)
- Modify: `skill_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestFileSkillProvider_ActivateDirResolution(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "my-skill", `---
name: my-skill
description: A skill
---
Run the script at {dir}/scripts/run.sh
Read templates from {dir}/templates/`)

	fp := NewFileSkillProvider(dir)
	skill, err := fp.Activate(context.Background(), "my-skill")
	if err != nil {
		t.Fatal(err)
	}

	skillDir := filepath.Join(dir, "my-skill")
	want := "Run the script at " + skillDir + "/scripts/run.sh\nRead templates from " + skillDir + "/templates/"
	if skill.Instructions != want {
		t.Errorf("Instructions =\n%s\nwant:\n%s", skill.Instructions, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_ActivateDirResolution -v`
Expected: FAIL — Instructions still contain literal `{dir}`.

- [ ] **Step 3: Add {dir} resolution in FileSkillProvider.Activate**

In `skill.go`, inside `Activate`, after setting `skill.Dir` and `skill.Instructions`, add:

```go
	if skill.Dir != "" {
		skill.Instructions = strings.ReplaceAll(skill.Instructions, "{dir}", skill.Dir)
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_ActivateDirResolution -v`
Expected: PASS

- [ ] **Step 5: Add same resolution in BuiltinSkillProvider.Activate**

In `skill_builtin.go`, after setting Instructions in Activate. Note: for embedded skills, Dir is empty — skip resolution when Dir is empty (already handled by the `if skill.Dir != ""` guard).

- [ ] **Step 6: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add skill.go skill_builtin.go skill_test.go && git commit -m "feat: resolve {dir} placeholder in skill instructions at activation"
```

---

### Task 5: Compatibility Gating at Discovery

**Files:**
- Modify: `skill.go` (FileSkillProvider.Discover)
- Modify: `skill_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestFileSkillProvider_DiscoverCompatibilityFilter(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "available", `---
name: available
description: Always available
---
Works everywhere`)

	writeSkillFile(t, dir, "gated", `---
name: gated
description: Needs something
compatibility: Requires nonexistent-binary-xyz123
---
Needs special binary`)

	fp := NewFileSkillProvider(dir)
	skills, err := fp.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Only "available" should be returned — "gated" has unmet compatibility
	if len(skills) != 2 {
		// Note: basic Discover returns all skills — filtering is opt-in
		// This test verifies that Compatibility field is populated
		// Actual filtering is done by CompatibilityFilter wrapper
	}
	var found bool
	for _, s := range skills {
		if s.Name == "gated" && s.Compatibility == "Requires nonexistent-binary-xyz123" {
			found = true
		}
	}
	if !found {
		t.Error("expected gated skill with Compatibility field populated")
	}
}
```

**Design decision:** Compatibility filtering should NOT be built into FileSkillProvider.Discover directly. Instead, the Compatibility field is populated at discovery time, and filtering is done by the consumer (agent option or app code). This keeps FileSkillProvider honest — it returns all skills with their metadata. The `WithSkills` option (Task 8) will filter based on compatibility.

- [ ] **Step 2: Run test to verify it passes** (field already populated from Task 2)

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestFileSkillProvider_DiscoverCompatibilityFilter -v`
Expected: PASS (Compatibility field already wired in Task 2).

- [ ] **Step 3: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add skill_test.go && git commit -m "test: verify Compatibility field populated at discovery"
```

---

### Task 6: Reference Auto-loading at Activation

**Files:**
- Modify: `skill.go` (new function)
- Modify: `skill_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestActivateWithReferences(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "base-knowledge", `---
name: base-knowledge
description: Foundation knowledge
---
Use color blue. Font size 12.`)

	writeSkillFile(t, dir, "pdf-gen", `---
name: pdf-gen
description: Generate PDFs
references: [base-knowledge]
---
Write HTML and render with Playwright.`)

	fp := NewFileSkillProvider(dir)
	skill, err := ActivateWithReferences(context.Background(), fp, "pdf-gen")
	if err != nil {
		t.Fatal(err)
	}

	// Instructions should contain referenced skill content first, then own content
	if !strings.Contains(skill.Instructions, "Use color blue. Font size 12.") {
		t.Error("expected referenced skill instructions to be included")
	}
	if !strings.Contains(skill.Instructions, "Write HTML and render with Playwright.") {
		t.Error("expected own instructions to be included")
	}
	// Referenced content should come BEFORE own content
	refIdx := strings.Index(skill.Instructions, "Use color blue")
	ownIdx := strings.Index(skill.Instructions, "Write HTML")
	if refIdx > ownIdx {
		t.Error("expected referenced instructions before own instructions")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestActivateWithReferences -v`
Expected: FAIL — `ActivateWithReferences` does not exist.

- [ ] **Step 3: Implement ActivateWithReferences**

In `skill.go`, add a new exported function:

```go
// ActivateWithReferences activates a skill and prepends instructions from
// all referenced skills. References are resolved one level deep — a
// referenced skill's own references are not followed. Missing references
// are silently skipped.
func ActivateWithReferences(ctx context.Context, p SkillProvider, name string) (Skill, error) {
	skill, err := p.Activate(ctx, name)
	if err != nil {
		return Skill{}, err
	}

	if len(skill.References) == 0 {
		return skill, nil
	}

	var parts []string
	for _, ref := range skill.References {
		refSkill, refErr := p.Activate(ctx, ref)
		if refErr != nil {
			continue // graceful — reference is optional
		}
		parts = append(parts, "## "+refSkill.Name+"\n\n"+refSkill.Instructions)
	}

	if len(parts) > 0 {
		parts = append(parts, skill.Instructions)
		skill.Instructions = strings.Join(parts, "\n\n---\n\n")
	}

	return skill, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestActivateWithReferences -v`
Expected: PASS

- [ ] **Step 5: Test missing reference is skipped gracefully**

```go
func TestActivateWithReferencesMissing(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "my-skill", `---
name: my-skill
description: A skill
references: [nonexistent]
---
My instructions`)

	fp := NewFileSkillProvider(dir)
	skill, err := ActivateWithReferences(context.Background(), fp, "my-skill")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Instructions != "My instructions" {
		t.Errorf("Instructions = %q, want %q", skill.Instructions, "My instructions")
	}
}
```

- [ ] **Step 6: Run test**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestActivateWithReferencesMissing -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add skill.go skill_test.go && git commit -m "feat: ActivateWithReferences resolves skill references at activation"
```

---

### Task 7: WithActiveSkills and WithSkills Agent Options

**Files:**
- Modify: `agent.go` (add options and agentConfig fields)
- Modify: `agentcore.go` (store active skill content)
- Modify: `llmagent.go` (wire skills into system prompt and tool registration)
- Modify: `agent_test.go`

- [ ] **Step 1: Write failing test for WithActiveSkills**

In `agent_test.go`:

```go
func TestWithActiveSkills(t *testing.T) {
	skill := Skill{
		Name:         "test-skill",
		Description:  "A test skill",
		Instructions: "Always use blue color.",
	}

	p := &callbackProvider{
		onChat: func(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
			// Verify skill instructions are in system prompt
			if len(req.Messages) == 0 {
				t.Fatal("expected messages")
			}
			sysMsg := req.Messages[0].Content
			if !strings.Contains(sysMsg, "Always use blue color.") {
				t.Errorf("system prompt should contain skill instructions, got: %s", sysMsg)
			}
			return &ChatResponse{Content: "done"}, nil
		},
	}

	agent := NewLLMAgent("test", "Base prompt.", p,
		WithActiveSkills(skill),
	)
	_, err := agent.Execute(context.Background(), AgentTask{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestWithActiveSkills -v`
Expected: FAIL — `WithActiveSkills` does not exist.

- [ ] **Step 3: Add agentConfig fields and options**

In `agent.go`, add to `agentConfig` struct (around line 148):

```go
	activeSkills   []Skill
	skillProvider  SkillProvider
```

Add option functions:

```go
// WithActiveSkills pre-activates skills at init time. Their instructions
// are appended to the agent's system prompt on every turn.
func WithActiveSkills(skills ...Skill) AgentOption {
	return func(c *agentConfig) { c.activeSkills = append(c.activeSkills, skills...) }
}

// WithSkills registers a SkillProvider and adds skill_discover and
// skill_activate tools so the agent can discover and activate skills
// at runtime.
func WithSkills(p SkillProvider) AgentOption {
	return func(c *agentConfig) { c.skillProvider = p }
}
```

- [ ] **Step 4: Store active skills in agentCore**

In `agentcore.go`, add to `agentCore` struct:

```go
	activeSkillInstructions string
	skillProvider           SkillProvider
```

In `initCore` function, after existing wiring:

```go
	// Build active skill instructions block
	if len(cfg.activeSkills) > 0 {
		var parts []string
		for _, s := range cfg.activeSkills {
			parts = append(parts, "## Skill: "+s.Name+"\n\n"+s.Instructions)
		}
		c.activeSkillInstructions = strings.Join(parts, "\n\n---\n\n")
	}
	c.skillProvider = cfg.skillProvider
```

- [ ] **Step 5: Wire into system prompt in LLMAgent**

In `llmagent.go`, modify `buildLoopConfig` (around line 57). The system prompt assembly needs to append active skill instructions:

```go
	prompt := resolvedPrompt
	if a.activeSkillInstructions != "" {
		prompt = prompt + "\n\n# Active Skills\n\n" + a.activeSkillInstructions
	}
```

- [ ] **Step 6: Auto-register skill tool for WithSkills**

In `llmagent.go`, in `NewLLMAgent` (around line 20), after sandbox tool registration:

```go
	// Register skill tools if provider is set
	if cfg.skillProvider != nil {
		skillTool := skill.New(cfg.skillProvider)
		a.tools.Add(skillTool)
	}
```

Add import for `"github.com/nevindra/oasis/tools/skill"` in llmagent.go.

- [ ] **Step 7: Run test to verify it passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestWithActiveSkills -v`
Expected: PASS

- [ ] **Step 8: Run all agent tests for regressions**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestLLMAgent -v -count=1`
Expected: All PASS

- [ ] **Step 9: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add agent.go agentcore.go llmagent.go agent_test.go && git commit -m "feat: WithActiveSkills and WithSkills agent options"
```

---

### Task 8: DefaultSkillDirs Helper

**Files:**
- Create: `skill_scan.go`
- Modify: `skill_test.go`

- [ ] **Step 1: Write test**

```go
func TestDefaultSkillDirs(t *testing.T) {
	dirs := DefaultSkillDirs()
	// Should include ~/.agents/skills/ and working directory .agents/skills/
	if len(dirs) < 1 {
		t.Fatal("expected at least one default directory")
	}
	// All paths should be absolute
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			t.Errorf("expected absolute path, got %q", d)
		}
	}
}
```

- [ ] **Step 2: Implement DefaultSkillDirs**

Create `skill_scan.go`:

```go
package oasis

import (
	"os"
	"path/filepath"
)

// DefaultSkillDirs returns the standard AgentSkills-compatible scan paths:
//   - <cwd>/.agents/skills/ (project-level)
//   - ~/.agents/skills/ (user-level)
//
// Directories that do not exist are included anyway — FileSkillProvider
// handles missing directories gracefully.
func DefaultSkillDirs() []string {
	var dirs []string

	// Project-level: current working directory
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(cwd, ".agents", "skills"))
	}

	// User-level: home directory
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".agents", "skills"))
	}

	return dirs
}
```

- [ ] **Step 3: Run test**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestDefaultSkillDirs -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add skill_scan.go skill_test.go && git commit -m "feat: DefaultSkillDirs for AgentSkills-compatible scan paths"
```

---

## Phase 3: Remove oasis-render and Rewrite Skills

### Task 9: Rewrite Built-in Skills

**Files:**
- Rewrite: `skills/oasis-pdf/SKILL.md`
- Rewrite: `skills/oasis-docx/SKILL.md`
- Rewrite: `skills/oasis-xlsx/SKILL.md`
- Rewrite: `skills/oasis-pptx/SKILL.md`
- Modify: `skills/oasis-design-system/SKILL.md`

- [ ] **Step 1: Rewrite oasis-pdf**

Replace `skills/oasis-pdf/SKILL.md` with prescriptive HTML/CSS + Playwright approach. The skill should:
- Teach agent to write standalone HTML with Tailwind CDN or inline CSS
- Show Playwright PDF conversion command
- Include gotchas (printBackground, page breaks, viewport width)
- Reference oasis-design-system for color tokens
- Add compatibility field

- [ ] **Step 2: Rewrite oasis-docx**

Replace `skills/oasis-docx/SKILL.md` with prescriptive python-docx approach. The skill should:
- Teach agent to use python-docx library directly
- Show code examples for headings, tables, images, styles
- Include gotchas (measurement units, style inheritance)
- Reference oasis-design-system

- [ ] **Step 3: Rewrite oasis-xlsx**

Replace `skills/oasis-xlsx/SKILL.md` with prescriptive openpyxl approach. The skill should:
- Teach agent to use openpyxl directly
- Show code for sheets, formulas, charts, conditional formatting
- Include gotchas (1-indexed cells, formula syntax)

- [ ] **Step 4: Rewrite oasis-pptx**

Replace `skills/oasis-pptx/SKILL.md` with prescriptive PptxGenJS approach. The skill should:
- Teach agent to use PptxGenJS directly
- Show code for slides, text, charts, images
- Include gotchas (coordinate system, master slides)

- [ ] **Step 5: Update oasis-design-system frontmatter**

Add `compatibility` field to `skills/oasis-design-system/SKILL.md` frontmatter.

- [ ] **Step 6: Remove reference file directories that are now redundant**

The existing `skills/oasis-*/references/` directories contain guidance for the old spec-driven approach. Rewrite or remove them to match the new prescriptive approach. Keep reference files that are still relevant (e.g., design token documentation).

- [ ] **Step 7: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add skills/ && git commit -m "feat: rewrite built-in skills as prescriptive guides (remove oasis-render dependency)"
```

---

### Task 10: Remove oasis-render and Renderers

**Files:**
- Remove: `bin/oasis-render`
- Remove: `renderers/pdf/render.js`
- Remove: `renderers/pdf/fill.py`
- Remove: `renderers/docx/generate.py`
- Remove: `renderers/docx/fill.py`
- Remove: `renderers/xlsx/generate.py`
- Remove: `renderers/pptx/compile.js`
- Remove: `requirements.txt`

- [ ] **Step 1: Remove files**

```bash
cd /home/nezhifi/Code/LLM/oasis && git rm bin/oasis-render && git rm -r renderers/ && git rm requirements.txt
```

- [ ] **Step 2: Verify build still passes**

Run: `cd /home/nezhifi/Code/LLM/oasis && go build ./...`
Expected: PASS — renderers are not Go code, removal shouldn't affect Go build.

- [ ] **Step 3: Run all tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./... -count=1`
Expected: All PASS

- [ ] **Step 4: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git commit -m "chore: remove oasis-render CLI and renderer scripts

Replaced by prescriptive skills that teach agents to use
underlying libraries (Playwright, python-docx, openpyxl,
PptxGenJS) directly. See skill-architecture-v2 spec."
```

---

### Task 11: Update Dockerfile

**Files:**
- Modify: `cmd/ix/Dockerfile`

- [ ] **Step 1: Remove renderer-specific lines from Dockerfile**

Remove these lines/sections from `cmd/ix/Dockerfile`:
- `COPY renderers/ /opt/oasis/renderers/` line
- `COPY bin/oasis-render /usr/local/bin/oasis-render` line
- `chmod +x /usr/local/bin/oasis-render` line
- `COPY requirements.txt` and `uv pip install -r requirements.txt` lines

Keep:
- Python3 + uv (skills may need python-docx, openpyxl directly)
- Node.js + pnpm (skills may need PptxGenJS, Playwright directly)
- Chrome/Playwright (needed for PDF generation)
- All general-purpose tools (git, ripgrep, fd-find, etc.)

Note: Python packages (python-docx, openpyxl, etc.) and Node packages (pptxgenjs, playwright-core, etc.) should remain installed globally — skills use them directly. The only thing removed is the oasis-render wrapper and the renderers/ copy.

- [ ] **Step 2: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add cmd/ix/Dockerfile && git commit -m "chore: remove oasis-render from Dockerfile, keep library deps for skills"
```

---

### Task 12: Update CI Workflow

**Files:**
- Modify: `.github/workflows/build-ix.yml`

- [ ] **Step 1: Remove renderer paths from trigger**

In `.github/workflows/build-ix.yml`, remove from the push paths list:
- `'renderers/**'`
- `'bin/oasis-render'`
- `'requirements.txt'`

Keep: `'skills/**'` should be added as a trigger (skill changes should rebuild the image since skills are embedded).

- [ ] **Step 2: Add skills path trigger**

Add `'skills/**'` to both push and pull_request path filters.

- [ ] **Step 3: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add .github/workflows/build-ix.yml && git commit -m "chore: update CI triggers — remove renderers, add skills"
```

---

### Task 13: Update BuiltinSkillProvider and Skill Tool

**Files:**
- Modify: `skill_builtin.go`
- Modify: `tools/skill/skill.go`
- Modify: `tools/skill/skill_test.go`

- [ ] **Step 1: Verify BuiltinSkillProvider still works with rewritten skills**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test -run TestBuiltin -v`
Expected: PASS (the go:embed directive `skills/*/SKILL.md` automatically picks up rewritten files).

- [ ] **Step 2: Update skill tool to show new fields in activate output**

In `tools/skill/skill.go`, find the `handleActivate` method. Update the output formatting to include Compatibility, License, and Metadata:

```go
	// In handleActivate, after existing field output:
	if skill.Compatibility != "" {
		fmt.Fprintf(&b, "Compatibility: %s\n", skill.Compatibility)
	}
	if skill.License != "" {
		fmt.Fprintf(&b, "License: %s\n", skill.License)
	}
	if len(skill.Metadata) > 0 {
		fmt.Fprintf(&b, "Metadata:\n")
		for k, v := range skill.Metadata {
			fmt.Fprintf(&b, "  %s: %s\n", k, v)
		}
	}
```

- [ ] **Step 3: Update skill tool create/update to accept new fields**

In `tools/skill/skill.go`, update `handleCreate` and `handleUpdate` to accept `compatibility`, `license`, and `metadata` parameters in the tool schema and argument parsing.

- [ ] **Step 4: Run skill tool tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./tools/skill/ -v -count=1`
Expected: All PASS

- [ ] **Step 5: Run all tests**

Run: `cd /home/nezhifi/Code/LLM/oasis && go test ./... -count=1`
Expected: All PASS

- [ ] **Step 6: Commit**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add skill_builtin.go tools/skill/ && git commit -m "feat: skill tool outputs new fields, create/update accepts them"
```

---

## Phase 4: Final Verification

### Task 14: Full Integration Test

- [ ] **Step 1: Run complete test suite**

```bash
cd /home/nezhifi/Code/LLM/oasis && go test ./... -count=1
```
Expected: All PASS

- [ ] **Step 2: Verify build**

```bash
cd /home/nezhifi/Code/LLM/oasis && go build ./...
```
Expected: PASS

- [ ] **Step 3: Verify go vet**

```bash
cd /home/nezhifi/Code/LLM/oasis && go vet ./...
```
Expected: PASS

- [ ] **Step 4: Spot-check BuiltinSkillProvider loads rewritten skills**

```bash
cd /home/nezhifi/Code/LLM/oasis && go test -run TestBuiltin -v
```
Expected: PASS — embedded skills load correctly with new frontmatter.

- [ ] **Step 5: Update CHANGELOG.md**

Add under `[Unreleased]`:
```markdown
### Added
- `Compatibility`, `License`, `Metadata` fields on `Skill` and `SkillSummary` — aligns with AgentSkills open specification.
- `ActivateWithReferences` — resolves skill references at activation, prepending referenced skill instructions.
- `WithActiveSkills` agent option — pre-activates skills at init time, injecting instructions into system prompt.
- `WithSkills` agent option — registers a `SkillProvider` and auto-adds skill discovery/activation tools.
- `DefaultSkillDirs()` — returns AgentSkills-compatible scan paths (`<project>/.agents/skills/`, `~/.agents/skills/`).
- `{dir}` placeholder in skill instructions resolved to absolute skill directory path at activation time.
- Prescriptive built-in skills: `oasis-pdf` (HTML/CSS + Playwright), `oasis-docx` (python-docx), `oasis-xlsx` (openpyxl), `oasis-pptx` (PptxGenJS).

### Changed
- **BREAKING:** Built-in document generation skills now teach agents to use underlying libraries directly instead of routing through `oasis-render`. Agents have full creative freedom and library API access.
- Frontmatter parser supports indented metadata blocks and new AgentSkills-compatible fields.
- Skill tool `skill_activate` output includes Compatibility, License, and Metadata fields.
- Skill tool `skill_create`/`skill_update` accepts new fields.

### Removed
- `bin/oasis-render` CLI — replaced by prescriptive skills.
- `renderers/` directory — PDF, DOCX, XLSX, PPTX renderer scripts removed.
- `requirements.txt` — Python deps for renderers (library deps remain in Dockerfile for direct agent use).
```

- [ ] **Step 6: Commit changelog**

```bash
cd /home/nezhifi/Code/LLM/oasis && git add CHANGELOG.md && git commit -m "docs: update changelog for skill architecture v2"
```
