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

// writeSkillFile creates a skill directory with a SKILL.md file in dir.
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

// newTestProvider creates a FileSkillProvider backed by a temporary directory.
func newTestProvider(t *testing.T) (*oasis.FileSkillProvider, string) {
	t.Helper()
	dir := t.TempDir()
	return oasis.NewFileSkillProvider(dir), dir
}

// --- tests ---

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

	writeSkillFile(t, dir, "coding", `---
name: coding
description: Write and review code
tags: [dev, code]
---
You are an expert programmer. Write clean, idiomatic code.
`)
	writeSkillFile(t, dir, "research", `---
name: research
description: Research topics on the web
---
Search the web for relevant information.
`)

	tool := New(provider)
	result, err := tool.Execute(context.Background(), "skill_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}

	// Names and descriptions should appear.
	if !strings.Contains(result.Content, "coding") {
		t.Errorf("expected 'coding' in output, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "research") {
		t.Errorf("expected 'research' in output, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Write and review code") {
		t.Errorf("expected description in output, got: %s", result.Content)
	}

	// Instructions must NOT appear in discover output.
	if strings.Contains(result.Content, "expert programmer") {
		t.Errorf("instructions must not appear in discover output, got: %s", result.Content)
	}
}

func TestDiscoverEmpty(t *testing.T) {
	provider, _ := newTestProvider(t)
	tool := New(provider)

	result, err := tool.Execute(context.Background(), "skill_discover", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "no skills") {
		t.Errorf("expected 'no skills' message, got: %s", result.Content)
	}
}

func TestActivate(t *testing.T) {
	provider, dir := newTestProvider(t)

	writeSkillFile(t, dir, "coding", `---
name: coding
description: Write and review code
tags: [dev]
---
You are an expert programmer. Write clean, idiomatic code.
`)

	tool := New(provider)
	args, _ := json.Marshal(map[string]string{"name": "coding"})
	result, err := tool.Execute(context.Background(), "skill_activate", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}

	// Full instructions should appear.
	if !strings.Contains(result.Content, "expert programmer") {
		t.Errorf("expected instructions in activate output, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "coding") {
		t.Errorf("expected skill name in output, got: %s", result.Content)
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
		"name":         "code-reviewer",
		"description":  "Review code changes for correctness and style",
		"instructions": "Analyze code for correctness and style.",
		"tags":         []string{"dev", "review"},
		"references":   []string{"coding"},
	})
	result, err := tool.Execute(context.Background(), "skill_create", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "code-reviewer") {
		t.Errorf("expected skill name in result, got: %s", result.Content)
	}

	// Verify file exists on disk.
	skillPath := filepath.Join(dir, "code-reviewer", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("expected skill file to exist at %s: %v", skillPath, err)
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

	writeSkillFile(t, dir, "coding", `---
name: coding
description: Write code
tags: [dev]
---
Original instructions.
`)

	tool := New(provider)

	newDesc := "Write and review code"
	newInstructions := "Updated instructions for writing code."
	args, _ := json.Marshal(map[string]any{
		"name":         "coding",
		"description":  newDesc,
		"instructions": newInstructions,
		"tags":         []string{"dev", "review"},
	})
	result, err := tool.Execute(context.Background(), "skill_update", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("tool error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "description") || !strings.Contains(result.Content, "instructions") {
		t.Errorf("expected changed fields in result, got: %s", result.Content)
	}

	// Verify changes via Activate.
	activateArgs, _ := json.Marshal(map[string]string{"name": "coding"})
	activateResult, err := tool.Execute(context.Background(), "skill_activate", activateArgs)
	if err != nil {
		t.Fatalf("unexpected error on activate: %v", err)
	}
	if activateResult.Error != "" {
		t.Fatalf("activate error: %s", activateResult.Error)
	}
	if !strings.Contains(activateResult.Content, newDesc) {
		t.Errorf("expected updated description %q in activated skill, got: %s", newDesc, activateResult.Content)
	}
	if !strings.Contains(activateResult.Content, newInstructions) {
		t.Errorf("expected updated instructions in activated skill, got: %s", activateResult.Content)
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
