// Package skills provides skill discovery, loading, and management for agents.
//
// Skills are reusable instruction packages that can specialize agent behavior
// for specific tasks. Each skill is a directory containing a SKILL.md file with
// YAML frontmatter (metadata) and markdown instructions.
//
// Skill providers abstract the source of skills — built-in (embedded), file-based,
// or custom implementations. Chain multiple providers so user skills override
// built-in ones:
//
//	provider := skills.ChainSkillProviders(
//		skills.NewFileSkillProvider(dirs...),
//		skills.NewBuiltinSkillProvider(),
//	)
//
// Load a skill and apply any referenced skills:
//
//	skill, err := skills.ActivateWithReferences(ctx, provider, "code-reviewer")
//	if err != nil {
//		// handle error
//	}
//	// skill.Instructions now includes referenced skills
//
// Agents automatically register skill-management tools (skill_discover,
// skill_activate, skill_create, skill_update) when configured with WithSkills.
//
// See the main oasis package for agent configuration and types.
package skills
