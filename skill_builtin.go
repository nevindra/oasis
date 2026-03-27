package oasis

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed skills/*/SKILL.md
var builtinSkills embed.FS

// BuiltinSkillProvider serves skills embedded in the oasis module binary.
// These are read-only — Discover and Activate work, but write operations
// are not supported. Chain with a FileSkillProvider for user skills.
type BuiltinSkillProvider struct{}

// NewBuiltinSkillProvider returns a provider that reads the framework's
// embedded skills (oasis-pdf, oasis-docx, oasis-xlsx, oasis-pptx, etc.).
func NewBuiltinSkillProvider() *BuiltinSkillProvider {
	return &BuiltinSkillProvider{}
}

func (p *BuiltinSkillProvider) Discover(ctx context.Context) ([]SkillSummary, error) {
	entries, err := fs.ReadDir(builtinSkills, "skills")
	if err != nil {
		return nil, nil // no embedded skills
	}

	var summaries []SkillSummary
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		data, err := fs.ReadFile(builtinSkills, "skills/"+name+"/SKILL.md")
		if err != nil {
			continue
		}
		fm, _, err := parseFrontmatter(strings.NewReader(string(data)))
		if err != nil {
			continue
		}
		summary := SkillSummary{
			Name:        name,
			Description: fm["description"],
		}
		if tags := fm["tags"]; tags != "" {
			summary.Tags = splitCSV(tags)
		}
		summaries = append(summaries, summary)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Name < summaries[j].Name
	})
	return summaries, nil
}

func (p *BuiltinSkillProvider) Activate(ctx context.Context, name string) (Skill, error) {
	data, err := fs.ReadFile(builtinSkills, "skills/"+name+"/SKILL.md")
	if err != nil {
		return Skill{}, fmt.Errorf("builtin skill %q not found", name)
	}
	fm, body, err := parseFrontmatter(strings.NewReader(string(data)))
	if err != nil {
		return Skill{}, fmt.Errorf("builtin skill %q: %w", name, err)
	}

	skillName := fm["name"]
	if skillName == "" {
		skillName = name
	}

	skill := Skill{
		Name:         skillName,
		Description:  fm["description"],
		Instructions: strings.TrimSpace(body),
		Model:        fm["model"],
	}
	if tools := fm["tools"]; tools != "" {
		skill.Tools = splitCSV(tools)
	}
	if tags := fm["tags"]; tags != "" {
		skill.Tags = splitCSV(tags)
	}
	if refs := fm["references"]; refs != "" {
		skill.References = splitCSV(refs)
	}
	return skill, nil
}
