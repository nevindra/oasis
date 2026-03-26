package oasis

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkillFile writes a SKILL.md with the given content inside
// <dir>/<name>/SKILL.md, creating the subdirectory if needed.
func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("writeSkillFile: mkdir %s: %v", skillDir, err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("writeSkillFile: write: %v", err)
	}
}

// --- Task 3: parseFrontmatter ---

func TestParseFrontmatter(t *testing.T) {
	input := `---
name: my-skill
description: "A test skill"
tags: [go, testing, ai]
tools: [search, shell]
model: gpt-4o
references: [https://example.com]
---
This is the body.
It has multiple lines.
`
	fm, body, err := parseFrontmatter(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cases := map[string]string{
		"name":        "my-skill",
		"description": "A test skill",
		"tags":        "go, testing, ai",
		"tools":       "search, shell",
		"model":       "gpt-4o",
		"references":  "https://example.com",
	}
	for k, want := range cases {
		if got := fm[k]; got != want {
			t.Errorf("fm[%q] = %q, want %q", k, got, want)
		}
	}

	wantBody := "This is the body.\nIt has multiple lines."
	if got := strings.TrimSpace(body); got != wantBody {
		t.Errorf("body = %q, want %q", got, wantBody)
	}
}

func TestParseFrontmatterNoFrontmatter(t *testing.T) {
	input := `This is just plain text.
No frontmatter here.
`
	_, _, err := parseFrontmatter(strings.NewReader(input))
	if err == nil {
		t.Fatal("expected error for missing ---, got nil")
	}
}

func TestParseFrontmatterEmpty(t *testing.T) {
	input := `---
---
This is the body only.
`
	fm, body, err := parseFrontmatter(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fm) != 0 {
		t.Errorf("expected empty frontmatter, got %v", fm)
	}
	if want := "This is the body only."; !strings.Contains(strings.TrimSpace(body), want) {
		t.Errorf("body %q does not contain %q", body, want)
	}
}

func TestParseFrontmatterComment(t *testing.T) {
	input := `---
# This is a comment
name: skill-with-comment

# Another comment
description: has comments
---
body
`
	fm, _, err := parseFrontmatter(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm["name"] != "skill-with-comment" {
		t.Errorf("name = %q, want %q", fm["name"], "skill-with-comment")
	}
	if fm["description"] != "has comments" {
		t.Errorf("description = %q, want %q", fm["description"], "has comments")
	}
	// Comments should not appear as keys.
	for k := range fm {
		if strings.HasPrefix(k, "#") {
			t.Errorf("comment line parsed as key: %q", k)
		}
	}
}

// --- Task 4: FileSkillProvider Discover + Activate ---

func TestFileSkillProvider_Discover(t *testing.T) {
	dir := t.TempDir()

	writeSkillFile(t, dir, "alpha", `---
name: alpha
description: Alpha skill
tags: [go, testing]
---
Alpha instructions.
`)
	writeSkillFile(t, dir, "beta", `---
name: beta
description: Beta skill
---
Beta instructions.
`)

	p := NewFileSkillProvider(dir)
	summaries, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}

	// Should be sorted by name.
	if summaries[0].Name != "alpha" {
		t.Errorf("summaries[0].Name = %q, want alpha", summaries[0].Name)
	}
	if summaries[1].Name != "beta" {
		t.Errorf("summaries[1].Name = %q, want beta", summaries[1].Name)
	}
	if summaries[0].Description != "Alpha skill" {
		t.Errorf("summaries[0].Description = %q, want 'Alpha skill'", summaries[0].Description)
	}
	if len(summaries[0].Tags) != 2 || summaries[0].Tags[0] != "go" || summaries[0].Tags[1] != "testing" {
		t.Errorf("summaries[0].Tags = %v, want [go testing]", summaries[0].Tags)
	}
}

func TestFileSkillProvider_DiscoverEmpty(t *testing.T) {
	dir := t.TempDir()
	p := NewFileSkillProvider(dir)
	summaries, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(summaries) != 0 {
		t.Errorf("expected 0 summaries, got %d", len(summaries))
	}
}

func TestFileSkillProvider_DiscoverMultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	writeSkillFile(t, dir1, "alpha", `---
name: alpha
description: Alpha in dir1
---
`)
	writeSkillFile(t, dir2, "beta", `---
name: beta
description: Beta in dir2
---
`)

	p := NewFileSkillProvider(dir1, dir2)
	summaries, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d: %v", len(summaries), summaries)
	}

	names := map[string]bool{}
	for _, s := range summaries {
		names[s.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("missing expected skills; got names: %v", names)
	}

	// First dir wins for same name.
	writeSkillFile(t, dir2, "alpha", `---
name: alpha
description: Alpha in dir2 (should be shadowed)
---
`)
	summaries2, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var alphaDesc string
	for _, s := range summaries2 {
		if s.Name == "alpha" {
			alphaDesc = s.Description
		}
	}
	if alphaDesc != "Alpha in dir1" {
		t.Errorf("expected dir1 to win; got description %q", alphaDesc)
	}
}

func TestFileSkillProvider_DiscoverSkipsNonDirs(t *testing.T) {
	dir := t.TempDir()

	// Create a regular file (not a directory) inside the search dir.
	if err := os.WriteFile(filepath.Join(dir, "notaskill.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSkillFile(t, dir, "real-skill", `---
name: real-skill
description: A real skill
---
`)

	p := NewFileSkillProvider(dir)
	summaries, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d: %v", len(summaries), summaries)
	}
	if summaries[0].Name != "real-skill" {
		t.Errorf("expected real-skill, got %q", summaries[0].Name)
	}
}

func TestFileSkillProvider_Activate(t *testing.T) {
	dir := t.TempDir()

	writeSkillFile(t, dir, "my-skill", `---
name: my-skill
description: Full skill
tags: [ai, agent]
tools: [search, shell]
model: claude-3
references: [https://docs.example.com]
---
Do something useful.
This is the second line.
`)

	p := NewFileSkillProvider(dir)
	skill, err := p.Activate(context.Background(), "my-skill")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if skill.Name != "my-skill" {
		t.Errorf("Name = %q, want my-skill", skill.Name)
	}
	if skill.Description != "Full skill" {
		t.Errorf("Description = %q, want 'Full skill'", skill.Description)
	}
	if skill.Model != "claude-3" {
		t.Errorf("Model = %q, want claude-3", skill.Model)
	}
	if want := "Do something useful.\nThis is the second line."; skill.Instructions != want {
		t.Errorf("Instructions = %q, want %q", skill.Instructions, want)
	}
	if len(skill.Tools) != 2 || skill.Tools[0] != "search" || skill.Tools[1] != "shell" {
		t.Errorf("Tools = %v, want [search shell]", skill.Tools)
	}
	if len(skill.Tags) != 2 || skill.Tags[0] != "ai" || skill.Tags[1] != "agent" {
		t.Errorf("Tags = %v, want [ai agent]", skill.Tags)
	}
	if len(skill.References) != 1 || skill.References[0] != "https://docs.example.com" {
		t.Errorf("References = %v, want [https://docs.example.com]", skill.References)
	}
	expectedDir := filepath.Join(dir, "my-skill")
	if skill.Dir != expectedDir {
		t.Errorf("Dir = %q, want %q", skill.Dir, expectedDir)
	}
}

func TestFileSkillProvider_ActivateNotFound(t *testing.T) {
	dir := t.TempDir()
	p := NewFileSkillProvider(dir)
	_, err := p.Activate(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
}

func TestFileSkillProvider_ActivateMultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	writeSkillFile(t, dir2, "remote-skill", `---
name: remote-skill
description: In second dir
---
Instructions from dir2.
`)

	p := NewFileSkillProvider(dir1, dir2)
	skill, err := p.Activate(context.Background(), "remote-skill")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if skill.Name != "remote-skill" {
		t.Errorf("Name = %q, want remote-skill", skill.Name)
	}
	if skill.Description != "In second dir" {
		t.Errorf("Description = %q, want 'In second dir'", skill.Description)
	}
}

// --- Task 5: SkillWriter ---

func TestFileSkillProvider_CreateSkill(t *testing.T) {
	dir := t.TempDir()
	p := NewFileSkillProvider(dir)

	skill := Skill{
		Name:         "new-skill",
		Description:  "A brand new skill",
		Instructions: "Do the thing.",
		Tags:         []string{"go", "test"},
		Tools:        []string{"search"},
		Model:        "gpt-4o",
		References:   []string{"https://example.com"},
	}

	if err := p.CreateSkill(context.Background(), skill); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	// Verify file on disk.
	skillPath := filepath.Join(dir, "new-skill", "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "name: new-skill") {
		t.Errorf("SKILL.md missing name field: %s", content)
	}
	if !strings.Contains(content, "tags: [go, test]") {
		t.Errorf("SKILL.md missing tags field: %s", content)
	}
	if !strings.Contains(content, "Do the thing.") {
		t.Errorf("SKILL.md missing instructions body: %s", content)
	}

	// Skill should be immediately discoverable.
	summaries, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	found := false
	for _, s := range summaries {
		if s.Name == "new-skill" {
			found = true
		}
	}
	if !found {
		t.Error("newly created skill not discoverable via Discover")
	}
}

func TestFileSkillProvider_CreateSkillAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	p := NewFileSkillProvider(dir)

	skill := Skill{Name: "dup-skill", Description: "first"}
	if err := p.CreateSkill(context.Background(), skill); err != nil {
		t.Fatalf("first CreateSkill: %v", err)
	}

	err := p.CreateSkill(context.Background(), Skill{Name: "dup-skill", Description: "second"})
	if err == nil {
		t.Fatal("expected error for duplicate skill, got nil")
	}
}

func TestFileSkillProvider_CreateSkillNoDirs(t *testing.T) {
	p := NewFileSkillProvider() // no dirs
	err := p.CreateSkill(context.Background(), Skill{Name: "test"})
	if err == nil {
		t.Fatal("expected error with no dirs configured, got nil")
	}
}

func TestFileSkillProvider_UpdateSkill(t *testing.T) {
	dir := t.TempDir()
	p := NewFileSkillProvider(dir)

	writeSkillFile(t, dir, "updatable", `---
name: updatable
description: Original description
---
Original instructions.
`)

	updated := Skill{
		Name:         "updatable",
		Description:  "Updated description",
		Instructions: "Updated instructions.",
	}
	if err := p.UpdateSkill(context.Background(), "updatable", updated); err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}

	// Verify via Activate.
	skill, err := p.Activate(context.Background(), "updatable")
	if err != nil {
		t.Fatalf("Activate after update: %v", err)
	}
	if skill.Description != "Updated description" {
		t.Errorf("Description = %q, want 'Updated description'", skill.Description)
	}
	if skill.Instructions != "Updated instructions." {
		t.Errorf("Instructions = %q, want 'Updated instructions.'", skill.Instructions)
	}
}

func TestFileSkillProvider_UpdateSkillNotFound(t *testing.T) {
	dir := t.TempDir()
	p := NewFileSkillProvider(dir)
	err := p.UpdateSkill(context.Background(), "ghost", Skill{Name: "ghost"})
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
}

func TestFileSkillProvider_DeleteSkill(t *testing.T) {
	dir := t.TempDir()
	p := NewFileSkillProvider(dir)

	writeSkillFile(t, dir, "to-delete", `---
name: to-delete
description: Will be gone
---
Bye.
`)

	if err := p.DeleteSkill(context.Background(), "to-delete"); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}

	// Verify the folder is gone.
	if _, err := os.Stat(filepath.Join(dir, "to-delete")); !os.IsNotExist(err) {
		t.Error("expected skill directory to be removed, but it still exists")
	}

	// Verify not discoverable.
	summaries, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, s := range summaries {
		if s.Name == "to-delete" {
			t.Error("deleted skill still appears in Discover results")
		}
	}
}

func TestFileSkillProvider_DeleteSkillNotFound(t *testing.T) {
	dir := t.TempDir()
	p := NewFileSkillProvider(dir)
	err := p.DeleteSkill(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent skill, got nil")
	}
}
