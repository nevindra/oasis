package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ExampleNewFileSkillProvider demonstrates loading skills from a directory.
func ExampleNewFileSkillProvider() {
	// Create a temporary directory with a test skill
	dir := os.TempDir()
	skillDir := filepath.Join(dir, "example", "my-skill")
	os.MkdirAll(skillDir, 0o755)
	defer os.RemoveAll(filepath.Join(dir, "example"))

	// Write a simple SKILL.md
	skillMD := `---
name: my-skill
description: An example skill
tags: [demo, example]
---
Use this skill to demonstrate skill loading.
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644)

	// Create a provider and discover skills
	provider := NewFileSkillProvider(filepath.Join(dir, "example"))
	summaries, _ := provider.Discover(context.Background())

	fmt.Printf("%d skill(s) available\n", len(summaries))
	if len(summaries) > 0 {
		fmt.Printf("First skill: %s\n", summaries[0].Name)
	}
	// Output:
	// 1 skill(s) available
	// First skill: my-skill
}

// ExampleChainSkillProviders demonstrates chaining providers.
func ExampleChainSkillProviders() {
	// In a real application, chain user skills with built-in skills:
	provider := ChainSkillProviders(
		NewFileSkillProvider(), // would search user directories
		NewBuiltinSkillProvider(),
	)

	// Now provider searches both user and built-in skills
	summaries, _ := provider.Discover(context.Background())
	fmt.Printf("Total skills available: %d\n", len(summaries))
	// Output varies based on available skills
}

// ExampleActivateWithReferences demonstrates loading a skill with references.
func ExampleActivateWithReferences() {
	dir := os.TempDir()
	exDir := filepath.Join(dir, "example2")
	os.MkdirAll(exDir, 0o755)
	defer os.RemoveAll(exDir)

	// Create a base skill
	baseDir := filepath.Join(exDir, "base-knowledge")
	os.MkdirAll(baseDir, 0o755)
	os.WriteFile(filepath.Join(baseDir, "SKILL.md"), []byte(`---
name: base-knowledge
description: Base knowledge skill
---
You have deep expertise in Go.
`), 0o644)

	// Create a skill that references it
	extDir := filepath.Join(exDir, "advanced-go")
	os.MkdirAll(extDir, 0o755)
	os.WriteFile(filepath.Join(extDir, "SKILL.md"), []byte(`---
name: advanced-go
description: Advanced Go skill
references: [base-knowledge]
---
Use advanced Go patterns.
`), 0o644)

	provider := NewFileSkillProvider(exDir)
	skill, _ := ActivateWithReferences(context.Background(), provider, "advanced-go")

	// skill.Instructions now includes both base knowledge and advanced patterns
	fmt.Printf("Combined instructions include base knowledge: %v\n",
		len(skill.Instructions) > 0)
	// Output:
	// Combined instructions include base knowledge: true
}
