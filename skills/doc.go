// Package skills provides skill discovery, loading, and management for agents.
//
// Skills are reusable instruction packages that can specialize agent behavior
// for specific tasks. Each skill is a directory containing a SKILL.md file with
// YAML frontmatter (metadata) and markdown instructions.
//
// Skill providers abstract the source of skills — file-based or custom
// implementations. Skill content is supplied by the application, not bundled
// into the framework. Chain multiple providers so project skills override
// user-level ones:
//
//	provider := skills.Chain(
//		skills.FromDir("./project-skills"),
//		skills.FromDir(skills.DefaultSkillDirs()...),
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
// Agents automatically register skill-management tools when configured with
// WithSkills: skills_list (listing + ranked search) and skill_view (load a
// skill's instructions and file list, or read a companion file) are always
// registered; skill_manage (create/update/delete) only when the provider
// implements SkillWriter. Companion-file reads through skill_view require the
// provider to implement SkillResources.
//
// See the main oasis package for agent configuration and types.
package skills
