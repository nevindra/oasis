// Package skill exposes skill management to agents through the standard Tool interface.
// Agents can search for, create, and update skills at runtime — enabling self-improvement
// loops where agents encode learned patterns as reusable instruction packages.
package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// Tool manages skills — stored instruction packages that specialize agent behavior.
type Tool struct {
	store     oasis.Store
	embedding oasis.EmbeddingProvider
	topK      int
}

// Compile-time interface check.
var _ oasis.Tool = (*Tool)(nil)

// New creates a skill Tool.
func New(store oasis.Store, embedding oasis.EmbeddingProvider) *Tool {
	return &Tool{store: store, embedding: embedding, topK: 5}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{
		{
			Name:        "skill_search",
			Description: "Search for relevant skills by semantic similarity to a query. Returns the top matching skills with their descriptions and instructions.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"query":{"type":"string","description":"Natural language query to find relevant skills"}
			},"required":["query"]}`),
		},
		{
			Name:        "skill_create",
			Description: "Create a new skill from experience. A skill is a stored instruction package that can specialize agent behavior for specific tasks.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"name":{"type":"string","description":"Short identifier for the skill (e.g. code-reviewer, data-analyst)"},
				"description":{"type":"string","description":"What this skill does, used for semantic search matching"},
				"instructions":{"type":"string","description":"Detailed instructions injected into the agent system prompt when this skill is active"},
				"tags":{"type":"array","items":{"type":"string"},"description":"Optional categorization labels"},
				"tools":{"type":"array","items":{"type":"string"},"description":"Optional list of tool names this skill should use (empty = all)"},
				"model":{"type":"string","description":"Optional model override"},
				"references":{"type":"array","items":{"type":"string"},"description":"Optional skill IDs this skill builds on"}
			},"required":["name","description","instructions"]}`),
		},
		{
			Name:        "skill_update",
			Description: "Update an existing skill. Only provided fields are changed; omitted fields keep their current values.",
			Parameters: json.RawMessage(`{"type":"object","properties":{
				"id":{"type":"string","description":"ID of the skill to update"},
				"name":{"type":"string","description":"New name"},
				"description":{"type":"string","description":"New description (triggers re-embedding)"},
				"instructions":{"type":"string","description":"New instructions"},
				"tags":{"type":"array","items":{"type":"string"},"description":"New tags (replaces existing)"},
				"tools":{"type":"array","items":{"type":"string"},"description":"New tool list (replaces existing)"},
				"model":{"type":"string","description":"New model override"},
				"references":{"type":"array","items":{"type":"string"},"description":"New skill references (replaces existing)"}
			},"required":["id"]}`),
		},
	}
}

func (t *Tool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
	var result string
	var err error

	switch name {
	case "skill_search":
		result, err = t.handleSearch(ctx, args)
	case "skill_create":
		result, err = t.handleCreate(ctx, args)
	case "skill_update":
		result, err = t.handleUpdate(ctx, args)
	default:
		return oasis.ToolResult{Error: "unknown skill tool: " + name}, nil
	}

	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}
	return oasis.ToolResult{Content: result}, nil
}

func (t *Tool) handleSearch(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if p.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	vectors, err := t.embedding.Embed(ctx, []string{p.Query})
	if err != nil {
		return "", fmt.Errorf("embedding failed: %w", err)
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return "", fmt.Errorf("embedding returned empty result")
	}

	results, err := t.store.SearchSkills(ctx, vectors[0], t.topK)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "no skills found matching query", nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%d skill(s) found:\n\n", len(results))
	for i, r := range results {
		fmt.Fprintf(&out, "%d. %s (score: %.2f)\n   ID: %s\n   %s\n",
			i+1, r.Name, r.Score, r.ID, r.Description)
		if len(r.Tags) > 0 {
			fmt.Fprintf(&out, "   Tags: %s\n", strings.Join(r.Tags, ", "))
		}
		if r.CreatedBy != "" {
			fmt.Fprintf(&out, "   Created by: %s\n", r.CreatedBy)
		}
		fmt.Fprintf(&out, "   Instructions: %s\n\n", r.Instructions)
	}
	return out.String(), nil
}

func (t *Tool) handleCreate(ctx context.Context, args json.RawMessage) (string, error) {
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

	vectors, err := t.embedding.Embed(ctx, []string{p.Description})
	if err != nil {
		return "", fmt.Errorf("embedding failed: %w", err)
	}
	var emb []float32
	if len(vectors) > 0 {
		emb = vectors[0]
	}

	createdBy := "unknown"
	if task, ok := oasis.TaskFromContext(ctx); ok {
		if uid := task.TaskUserID(); uid != "" {
			createdBy = uid
		}
	}

	now := oasis.NowUnix()
	skill := oasis.Skill{
		ID:           oasis.NewID(),
		Name:         p.Name,
		Description:  p.Description,
		Instructions: p.Instructions,
		Tools:        p.Tools,
		Model:        p.Model,
		Tags:         p.Tags,
		CreatedBy:    createdBy,
		References:   p.References,
		Embedding:    emb,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := t.store.CreateSkill(ctx, skill); err != nil {
		return "", err
	}

	return fmt.Sprintf("created skill %q (id: %s)", skill.Name, skill.ID), nil
}

func (t *Tool) handleUpdate(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID           string   `json:"id"`
		Name         *string  `json:"name"`
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
	if p.ID == "" {
		return "", fmt.Errorf("skill id is required")
	}

	skill, err := t.store.GetSkill(ctx, p.ID)
	if err != nil {
		return "", fmt.Errorf("skill not found: %w", err)
	}

	var changes []string
	if p.Name != nil {
		skill.Name = *p.Name
		changes = append(changes, "name")
	}
	if p.Description != nil {
		skill.Description = *p.Description
		changes = append(changes, "description")
	}
	if p.Instructions != nil {
		skill.Instructions = *p.Instructions
		changes = append(changes, "instructions")
	}
	if p.Tags != nil {
		skill.Tags = p.Tags
		changes = append(changes, "tags")
	}
	if p.Tools != nil {
		skill.Tools = p.Tools
		changes = append(changes, "tools")
	}
	if p.Model != nil {
		skill.Model = *p.Model
		changes = append(changes, "model")
	}
	if p.References != nil {
		skill.References = p.References
		changes = append(changes, "references")
	}

	if len(changes) == 0 {
		return "no changes specified", nil
	}

	// Re-embed if description changed.
	if p.Description != nil {
		vectors, err := t.embedding.Embed(ctx, []string{skill.Description})
		if err != nil {
			return "", fmt.Errorf("embedding failed: %w", err)
		}
		if len(vectors) > 0 {
			skill.Embedding = vectors[0]
		}
	}

	skill.UpdatedAt = oasis.NowUnix()
	if err := t.store.UpdateSkill(ctx, skill); err != nil {
		return "", err
	}

	return fmt.Sprintf("updated skill %q: %s", skill.Name, strings.Join(changes, ", ")), nil
}
