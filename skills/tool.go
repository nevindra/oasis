package skills

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nevindra/oasis/core"
)

// maxResourceBytes caps skill_view file output to protect the model context window.
const maxResourceBytes = 64 * 1024

// NewSkillTools returns the consolidated skill-management tools backed by the
// given SkillProvider:
//
//   - skills_list — list all skills, or rank them against a free-text query
//     (provider's own SkillSearcher when present, else built-in BM25).
//   - skill_view — load a skill's full instructions (its companion files are
//     listed alongside when the provider implements SkillResources), or read
//     one companion file by passing file_path.
//   - skill_manage — create/update/delete skills; registered only when the
//     provider implements SkillWriter.
func NewSkillTools(provider SkillProvider) []core.AnyTool {
	searcher, ok := provider.(SkillSearcher)
	if !ok {
		searcher = NewBM25Searcher(provider)
	}

	tools := []core.AnyTool{
		core.Erase[skillsListIn, string](&skillsListTool{provider: provider, searcher: searcher}),
		core.Erase[skillViewIn, string](&skillViewTool{provider: provider}),
	}

	if w, ok := provider.(SkillWriter); ok {
		tools = append(tools, core.Erase[skillManageIn, string](&skillManageTool{provider: provider, writer: w}))
	}
	return tools
}

// --- skills_list ---

type skillsListIn struct {
	Query string `json:"query,omitempty" describe:"Optional free-text task or topic; when set, results are ranked by relevance instead of listing everything"`
	Limit int    `json:"limit,omitempty" describe:"Maximum number of ranked results when query is set (default 5)"`
}

type skillsListTool struct {
	provider SkillProvider
	searcher SkillSearcher
}

func (t *skillsListTool) Definition() core.ToolMeta {
	return core.ToolMeta{
		Name:        "skills_list",
		Description: "List the available skills (name, description, tags). Pass query to rank them against a task or topic instead. Use skill_view to load one.",
	}
}

func (t *skillsListTool) Execute(ctx context.Context, in skillsListIn) (string, error) {
	if in.Query != "" {
		limit := in.Limit
		if limit <= 0 {
			limit = 5
		}
		results, err := t.searcher.SearchSkills(ctx, in.Query, limit)
		if err != nil {
			return "", fmt.Errorf("search failed: %w", err)
		}
		if len(results) == 0 {
			return "no matching skills found", nil
		}
		var out strings.Builder
		fmt.Fprintf(&out, "%d matching skill(s):\n\n", len(results))
		for i, r := range results {
			fmt.Fprintf(&out, "%d. %s (score %.2f)\n   %s\n", i+1, r.Name, r.Score, r.Description)
			if len(r.Tags) > 0 {
				fmt.Fprintf(&out, "   Tags: %s\n", strings.Join(r.Tags, ", "))
			}
			fmt.Fprintln(&out)
		}
		return out.String(), nil
	}

	summaries, err := t.provider.Discover(ctx)
	if err != nil {
		return "", fmt.Errorf("discover failed: %w", err)
	}
	if len(summaries) == 0 {
		return "no skills available", nil
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%d skill(s) available:\n\n", len(summaries))
	for i, s := range summaries {
		fmt.Fprintf(&out, "%d. %s\n   %s\n", i+1, s.Name, s.Description)
		if len(s.Tags) > 0 {
			fmt.Fprintf(&out, "   Tags: %s\n", strings.Join(s.Tags, ", "))
		}
		fmt.Fprintln(&out)
	}
	return out.String(), nil
}

// --- skill_view ---

type skillViewIn struct {
	Name     string `json:"name" describe:"The name of the skill to view"`
	FilePath string `json:"file_path,omitempty" describe:"Skill-relative path of a companion file to read (e.g. references/api.md). Omit to load the skill's instructions and file list."`
}

type skillViewTool struct {
	provider SkillProvider
}

func (t *skillViewTool) Definition() core.ToolMeta {
	return core.ToolMeta{
		Name:        "skill_view",
		Description: "Load the full instructions for a skill by name (activating it for the current task) plus the list of its companion files, or read one companion file by passing file_path.",
	}
}

func (t *skillViewTool) Execute(ctx context.Context, in skillViewIn) (string, error) {
	if in.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	if in.FilePath != "" {
		r, ok := t.provider.(SkillResources)
		if !ok {
			return "", fmt.Errorf("skill provider does not expose companion files")
		}
		data, err := r.ReadResource(ctx, in.Name, in.FilePath)
		if err != nil {
			return "", err
		}
		if !utf8.Valid(data) {
			return fmt.Sprintf("binary file %q (%d bytes); not shown", in.FilePath, len(data)), nil
		}
		if len(data) > maxResourceBytes {
			// Trim back to a rune boundary so truncation never splits a UTF-8 rune.
			cut := maxResourceBytes
			for cut > 0 && !utf8.RuneStart(data[cut]) {
				cut--
			}
			return string(data[:cut]) + "\n\n[truncated: file exceeds 64KB]", nil
		}
		return string(data), nil
	}

	sk, err := t.provider.Activate(ctx, in.Name)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Skill: %s\n", sk.Name)
	fmt.Fprintf(&out, "Description: %s\n", sk.Description)
	if len(sk.Tags) > 0 {
		fmt.Fprintf(&out, "Tags: %s\n", strings.Join(sk.Tags, ", "))
	}
	if len(sk.Tools) > 0 {
		fmt.Fprintf(&out, "Tools: %s\n", strings.Join(sk.Tools, ", "))
	}
	if sk.Model != "" {
		fmt.Fprintf(&out, "Model: %s\n", sk.Model)
	}
	if len(sk.References) > 0 {
		fmt.Fprintf(&out, "References: %s\n", strings.Join(sk.References, ", "))
	}
	// Companion files ride along on activation (best-effort) so a separate
	// listing call is never needed before reading one via file_path.
	if r, ok := t.provider.(SkillResources); ok {
		if files, err := r.ListResources(ctx, in.Name); err == nil && len(files) > 0 {
			fmt.Fprintf(&out, "Files (read with skill_view file_path):\n")
			for _, f := range files {
				fmt.Fprintf(&out, "- %s\n", f)
			}
		}
	}
	fmt.Fprintf(&out, "\nInstructions:\n%s\n", sk.Instructions)
	return out.String(), nil
}

// --- skill_manage ---

type skillManageIn struct {
	Action       string   `json:"action" enum:"create,update,delete" describe:"What to do: create a new skill, update fields of an existing one, or delete one"`
	Name         string   `json:"name" describe:"Skill name (short identifier, e.g. code-reviewer)"`
	Description  *string  `json:"description,omitempty" describe:"What this skill does, used for discovery matching (required for create)"`
	Instructions *string  `json:"instructions,omitempty" describe:"Detailed instructions injected into the agent system prompt when the skill is active (required for create)"`
	Tags         []string `json:"tags,omitempty" describe:"Categorization labels (update replaces existing)"`
	Tools        []string `json:"tools,omitempty" describe:"Tool names this skill should use, empty = all (update replaces existing)"`
	Model        *string  `json:"model,omitempty" describe:"Optional model override"`
	References   []string `json:"references,omitempty" describe:"Skill names this skill builds on (update replaces existing)"`
}

type skillManageTool struct {
	provider SkillProvider
	writer   SkillWriter
}

func (t *skillManageTool) Definition() core.ToolMeta {
	return core.ToolMeta{
		Name:        "skill_manage",
		Description: "Manage stored skills (procedural memory): create a new skill, update an existing one (only provided fields change), or delete one.",
	}
}

func (t *skillManageTool) Execute(ctx context.Context, in skillManageIn) (string, error) {
	if in.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	switch in.Action {
	case "create":
		if in.Description == nil || *in.Description == "" || in.Instructions == nil || *in.Instructions == "" {
			return "", fmt.Errorf("description and instructions are required for create")
		}
		sk := Skill{
			Name:         in.Name,
			Description:  *in.Description,
			Instructions: *in.Instructions,
			Tags:         in.Tags,
			Tools:        in.Tools,
			References:   in.References,
		}
		if in.Model != nil {
			sk.Model = *in.Model
		}
		if err := t.writer.CreateSkill(ctx, sk); err != nil {
			return "", err
		}
		return fmt.Sprintf("created skill %q", sk.Name), nil

	case "update":
		existing, err := t.provider.Activate(ctx, in.Name)
		if err != nil {
			return "", fmt.Errorf("cannot update: %w", err)
		}
		var changes []string
		if in.Description != nil {
			existing.Description = *in.Description
			changes = append(changes, "description")
		}
		if in.Instructions != nil {
			existing.Instructions = *in.Instructions
			changes = append(changes, "instructions")
		}
		if in.Tags != nil {
			existing.Tags = in.Tags
			changes = append(changes, "tags")
		}
		if in.Tools != nil {
			existing.Tools = in.Tools
			changes = append(changes, "tools")
		}
		if in.Model != nil {
			existing.Model = *in.Model
			changes = append(changes, "model")
		}
		if in.References != nil {
			existing.References = in.References
			changes = append(changes, "references")
		}
		if len(changes) == 0 {
			return "no changes specified", nil
		}
		if err := t.writer.UpdateSkill(ctx, in.Name, existing); err != nil {
			return "", err
		}
		return fmt.Sprintf("updated skill %q: %s", existing.Name, strings.Join(changes, ", ")), nil

	case "delete":
		if err := t.writer.DeleteSkill(ctx, in.Name); err != nil {
			return "", err
		}
		return fmt.Sprintf("deleted skill %q", in.Name), nil

	default:
		return "", fmt.Errorf("unknown action %q (expected create, update, or delete)", in.Action)
	}
}

// Compile-time interface checks.
var (
	_ core.Tool[skillsListIn, string]  = (*skillsListTool)(nil)
	_ core.Tool[skillViewIn, string]   = (*skillViewTool)(nil)
	_ core.Tool[skillManageIn, string] = (*skillManageTool)(nil)
)
