package oasis

import (
	"context"

	"github.com/nevindra/oasis/skills"
)

// Re-exports from github.com/nevindra/oasis/skills.
// The canonical types live in the skills subpackage; these aliases keep the
// root-package API stable.

type FileSkillProvider = skills.FileSkillProvider
type ChainedSkillProvider = skills.ChainedSkillProvider

// NewFileSkillProvider creates a skill provider that loads SKILL.md files
// from the given directories. Falls back to DefaultSkillDirs() when no dirs
// are given.
func NewFileSkillProvider(dirs ...string) *FileSkillProvider {
	return skills.NewFileSkillProvider(dirs...)
}

// ChainSkillProviders merges multiple SkillProviders.
func ChainSkillProviders(providers ...SkillProvider) *ChainedSkillProvider {
	return skills.ChainSkillProviders(providers...)
}

// ActivateWithReferences loads a skill and recursively loads all referenced skills,
// appending their instructions to the root skill's instructions.
func ActivateWithReferences(ctx context.Context, p SkillProvider, name string) (Skill, error) {
	return skills.ActivateWithReferences(ctx, p, name)
}
