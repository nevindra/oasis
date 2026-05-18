package oasis

import "github.com/nevindra/oasis/skills"

// Re-exports from github.com/nevindra/oasis/skills.

type BuiltinSkillProvider = skills.BuiltinSkillProvider

// NewBuiltinSkillProvider returns a provider that reads the framework's
// embedded skills (oasis-pdf, oasis-docx, oasis-xlsx, oasis-pptx, etc.).
func NewBuiltinSkillProvider() *BuiltinSkillProvider {
	return skills.NewBuiltinSkillProvider()
}
