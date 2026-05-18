package oasis

import "github.com/nevindra/oasis/skills"

// newSkillTools is the internal entry point used by llmagent.go when WithSkills
// is configured. Delegates to skills.NewSkillTools.
func newSkillTools(provider SkillProvider) []AnyTool {
	return skills.NewSkillTools(provider)
}
