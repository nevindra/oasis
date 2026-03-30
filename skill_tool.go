package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// skillTool exposes skill management to agents through the Tool interface.
// Registered automatically when WithSkills is used. This is the internal
// equivalent of tools/skill.Tool — defined here to avoid an import cycle
// (tools/skill already imports oasis).
type skillTool struct {
	provider SkillProvider
}

// Compile-time interface check.
var _ Tool = (*skillTool)(nil)

// newSkillTool creates a skillTool backed by the given SkillProvider.
// If the provider also implements SkillWriter, create and update actions are enabled.
func newSkillTool(provider SkillProvider) *skillTool {
	return &skillTool{provider: provider}
}

func (t *skillTool) Definitions() []ToolDefinition {
	defs := []ToolDefinition{
		{
			Name:        "skill_discover",
			Description: "List all available skills with their names, descriptions, and tags. Use this to browse what skills exist before activating one.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "skill_activate",
			Description: "Load the full instructions for a skill by name. Returns complete metadata and instructions that can be applied to the current task.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"name":{"type":"string","description":"The name of the skill to activate"}
			},"required":["name"]}`),
		},
	}

	if _, ok := t.provider.(SkillWriter); ok {
		defs = append(defs,
			ToolDefinition{
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
			},
			ToolDefinition{
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
			},
		)
	}

	return defs
}

func (t *skillTool) Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	var result string
	var err error

	switch name {
	case "skill_discover":
		result, err = t.handleDiscover(ctx)
	case "skill_activate":
		result, err = t.handleActivate(ctx, args)
	case "skill_create":
		result, err = t.handleCreate(ctx, args)
	case "skill_update":
		result, err = t.handleUpdate(ctx, args)
	default:
		return ToolResult{Error: "unknown skill tool: " + name}, nil
	}

	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	return ToolResult{Content: result}, nil
}

func (t *skillTool) handleDiscover(ctx context.Context) (string, error) {
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

func (t *skillTool) handleActivate(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	sk, err := t.provider.Activate(ctx, p.Name)
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
	fmt.Fprintf(&out, "\nInstructions:\n%s\n", sk.Instructions)
	return out.String(), nil
}

func (t *skillTool) handleCreate(ctx context.Context, args json.RawMessage) (string, error) {
	w, ok := t.provider.(SkillWriter)
	if !ok {
		return "", fmt.Errorf("skill creation is not supported by this provider")
	}

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
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Name == "" || p.Description == "" || p.Instructions == "" {
		return "", fmt.Errorf("name, description, and instructions are required")
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

	if err := w.CreateSkill(ctx, sk); err != nil {
		return "", err
	}

	return fmt.Sprintf("created skill %q", sk.Name), nil
}

func (t *skillTool) handleUpdate(ctx context.Context, args json.RawMessage) (string, error) {
	w, ok := t.provider.(SkillWriter)
	if !ok {
		return "", fmt.Errorf("skill updates are not supported by this provider")
	}

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
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Name == "" {
		return "", fmt.Errorf("name is required")
	}

	// Load existing skill to merge with.
	existing, err := t.provider.Activate(ctx, p.Name)
	if err != nil {
		return "", fmt.Errorf("cannot update: %w", err)
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
		return "no changes specified", nil
	}

	if err := w.UpdateSkill(ctx, p.Name, existing); err != nil {
		return "", err
	}

	return fmt.Sprintf("updated skill %q: %s", existing.Name, strings.Join(changes, ", ")), nil
}
