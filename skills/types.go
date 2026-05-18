package skills

import "context"

// SkillProvider discovers and loads skills from any backing store.
// Implementations must be safe for concurrent use.
type SkillProvider interface {
	// Discover returns lightweight summaries of all available skills.
	// Only name, description, and tags are loaded — full instructions remain unread.
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

// SkillSummary is a lightweight view of a skill for discovery.
// Contains only the metadata needed for an agent to decide whether to activate.
type SkillSummary struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Tags          []string `json:"tags,omitempty"`
	Compatibility string   `json:"compatibility,omitempty"`
}

// Skill is a stored instruction package that specializes agent behavior.
// Skills are folders on disk with a SKILL.md file containing YAML frontmatter
// (metadata) and markdown body (instructions). Compatible with the AgentSkills
// open specification (https://agentskills.io/specification.md).
type Skill struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Instructions  string            `json:"instructions"`
	Tools         []string          `json:"tools,omitempty"`
	Model         string            `json:"model,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	References    []string          `json:"references,omitempty"`
	Dir           string            `json:"-"`
	Compatibility string            `json:"compatibility,omitempty"`
	License       string            `json:"license,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}
