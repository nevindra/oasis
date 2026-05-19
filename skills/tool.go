package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis/core"
)

// NewSkillTools returns the set of skill-management tools backed by the given
// SkillProvider. skill_discover and skill_activate are always returned;
// skill_create and skill_update are included only when the provider also
// implements SkillWriter.
func NewSkillTools(provider SkillProvider) []core.AnyTool {
	tools := []core.AnyTool{
		core.Erase[skillDiscoverIn, string](&skillDiscoverTool{provider: provider}),
		&skillActivateTool{provider: provider}, // not yet migrated
	}
	if w, ok := provider.(SkillWriter); ok {
		tools = append(tools,
			&skillCreateTool{writer: w},
			&skillUpdateTool{provider: provider, writer: w},
		)
	}
	return tools
}

// --- skill_discover ---

// skillDiscoverIn has no fields; an empty In schema reflects to
// {"type":"object","properties":{}}.
type skillDiscoverIn struct{}

type skillDiscoverTool struct {
	provider SkillProvider
}

func (t *skillDiscoverTool) Definition() core.ToolMeta {
	return core.ToolMeta{
		Name:        "skill_discover",
		Description: "List all available skills with their names, descriptions, and tags. Use this to browse what skills exist before activating one.",
	}
}

func (t *skillDiscoverTool) Execute(ctx context.Context, _ skillDiscoverIn) (string, error) {
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

// --- skill_activate ---

type skillActivateTool struct {
	provider SkillProvider
}

func (t *skillActivateTool) Name() string { return "skill_activate" }

func (t *skillActivateTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        "skill_activate",
		Description: "Load the full instructions for a skill by name. Returns complete metadata and instructions that can be applied to the current task.",
		Parameters: json.RawMessage(`{"type":"object","properties":{
			"name":{"type":"string","description":"The name of the skill to activate"}
		},"required":["name"]}`),
	}
}

func (t *skillActivateTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return core.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if p.Name == "" {
		return core.ToolResult{Error: "name is required"}, nil
	}

	sk, err := t.provider.Activate(ctx, p.Name)
	if err != nil {
		return core.ToolResult{Error: err.Error()}, nil
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
	fmt.Fprintf(&out, "\nInstructions:\n%s\n", sk.Instructions)
	return core.ToolResult{Content: out.String()}, nil
}

// --- skill_create ---

type skillCreateTool struct {
	writer SkillWriter
}

func (t *skillCreateTool) Name() string { return "skill_create" }

func (t *skillCreateTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        "skill_create",
		Description: "Create a new skill from experience. A skill is a stored instruction package that can specialize agent behavior for specific tasks.",
		Parameters: json.RawMessage(`{"type":"object","properties":{
			"name":{"type":"string","description":"Short identifier for the skill (e.g. code-reviewer, data-analyst)"},
			"description":{"type":"string","description":"What this skill does, used for discovery matching"},
			"instructions":{"type":"string","description":"Detailed instructions injected into the agent system prompt when this skill is active"},
			"tags":{"type":"array","items":{"type":"string"},"description":"Optional categorization labels"},
			"tools":{"type":"array","items":{"type":"string"},"description":"Optional list of tool names this skill should use (empty = all)"},
			"model":{"type":"string","description":"Optional model override"},
			"references":{"type":"array","items":{"type":"string"},"description":"Optional skill names this skill builds on"}
		},"required":["name","description","instructions"]}`),
	}
}

func (t *skillCreateTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var p struct {
		Name         string   `json:"name"`
		Description  string   `json:"description"`
		Instructions string   `json:"instructions"`
		Tags         []string `json:"tags"`
		Tools        []string `json:"tools"`
		Model        string   `json:"model"`
		References   []string `json:"references"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return core.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if p.Name == "" || p.Description == "" || p.Instructions == "" {
		return core.ToolResult{Error: "name, description, and instructions are required"}, nil
	}

	sk := Skill{
		Name:         p.Name,
		Description:  p.Description,
		Instructions: p.Instructions,
		Tags:         p.Tags,
		Tools:        p.Tools,
		Model:        p.Model,
		References:   p.References,
	}
	if err := t.writer.CreateSkill(ctx, sk); err != nil {
		return core.ToolResult{Error: err.Error()}, nil
	}
	return core.ToolResult{Content: fmt.Sprintf("created skill %q", sk.Name)}, nil
}

// --- skill_update ---

type skillUpdateTool struct {
	provider SkillProvider
	writer   SkillWriter
}

func (t *skillUpdateTool) Name() string { return "skill_update" }

func (t *skillUpdateTool) Definition() core.ToolDefinition {
	return core.ToolDefinition{
		Name:        "skill_update",
		Description: "Update an existing skill. Only provided fields are changed; omitted fields keep their current values.",
		Parameters: json.RawMessage(`{"type":"object","properties":{
			"name":{"type":"string","description":"Name of the skill to update"},
			"description":{"type":"string","description":"New description"},
			"instructions":{"type":"string","description":"New instructions"},
			"tags":{"type":"array","items":{"type":"string"},"description":"New tags (replaces existing)"},
			"tools":{"type":"array","items":{"type":"string"},"description":"New tool list (replaces existing)"},
			"model":{"type":"string","description":"New model override"},
			"references":{"type":"array","items":{"type":"string"},"description":"New skill references (replaces existing)"}
		},"required":["name"]}`),
	}
}

func (t *skillUpdateTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var p struct {
		Name         string   `json:"name"`
		Description  *string  `json:"description"`
		Instructions *string  `json:"instructions"`
		Tags         []string `json:"tags"`
		Tools        []string `json:"tools"`
		Model        *string  `json:"model"`
		References   []string `json:"references"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return core.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if p.Name == "" {
		return core.ToolResult{Error: "name is required"}, nil
	}

	// Load existing skill to merge with.
	existing, err := t.provider.Activate(ctx, p.Name)
	if err != nil {
		return core.ToolResult{Error: "cannot update: " + err.Error()}, nil
	}

	var changes []string
	if p.Description != nil {
		existing.Description = *p.Description
		changes = append(changes, "description")
	}
	if p.Instructions != nil {
		existing.Instructions = *p.Instructions
		changes = append(changes, "instructions")
	}
	if p.Tags != nil {
		existing.Tags = p.Tags
		changes = append(changes, "tags")
	}
	if p.Tools != nil {
		existing.Tools = p.Tools
		changes = append(changes, "tools")
	}
	if p.Model != nil {
		existing.Model = *p.Model
		changes = append(changes, "model")
	}
	if p.References != nil {
		existing.References = p.References
		changes = append(changes, "references")
	}
	if len(changes) == 0 {
		return core.ToolResult{Content: "no changes specified"}, nil
	}
	if err := t.writer.UpdateSkill(ctx, p.Name, existing); err != nil {
		return core.ToolResult{Error: err.Error()}, nil
	}
	return core.ToolResult{Content: fmt.Sprintf("updated skill %q: %s", existing.Name, strings.Join(changes, ", "))}, nil
}

// Compile-time interface checks.
var (
	_ core.Tool[skillDiscoverIn, string] = (*skillDiscoverTool)(nil)
	// remaining AnyTool checks stay until their migration tasks
	_ core.AnyTool = (*skillActivateTool)(nil)
	_ core.AnyTool = (*skillCreateTool)(nil)
	_ core.AnyTool = (*skillUpdateTool)(nil)
)
