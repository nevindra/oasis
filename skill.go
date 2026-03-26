package oasis

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Compile-time interface checks.
var _ SkillProvider = (*FileSkillProvider)(nil)
var _ SkillWriter = (*FileSkillProvider)(nil)

// parseFrontmatter reads an io.Reader whose first line must be "---".
// It returns the parsed frontmatter key-value map, the body (everything after
// the closing "---"), and any error encountered.
//
// Supported value types in frontmatter:
//   - plain string:   key: value
//   - quoted string:  key: "value"  or  key: 'value'
//   - inline array:   key: [a, b, c]  → stored as "a, b, c"
//
// Comment lines (starting with #) and blank lines inside the frontmatter block
// are silently skipped.
func parseFrontmatter(r io.Reader) (map[string]string, string, error) {
	scanner := bufio.NewScanner(r)

	// First line must be "---".
	if !scanner.Scan() {
		return nil, "", fmt.Errorf("parseFrontmatter: empty input")
	}
	if strings.TrimRight(scanner.Text(), "\r") != "---" {
		return nil, "", fmt.Errorf("parseFrontmatter: first line must be ---")
	}

	fm := make(map[string]string)
	inFrontmatter := true
	var bodyLines []string

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")

		if inFrontmatter {
			if line == "---" {
				inFrontmatter = false
				continue
			}
			// Skip blank lines and comment lines.
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			// Parse key: value.
			idx := strings.Index(line, ":")
			if idx < 0 {
				continue
			}
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			val = parseFrontmatterValue(val)
			fm[key] = val
		} else {
			bodyLines = append(bodyLines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, "", fmt.Errorf("parseFrontmatter: %w", err)
	}

	body := strings.Join(bodyLines, "\n")
	return fm, body, nil
}

// parseFrontmatterValue normalises a raw YAML scalar value string:
//   - inline array [a, b, c] → "a, b, c"
//   - surrounding double or single quotes stripped
func parseFrontmatterValue(v string) string {
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		inner := v[1 : len(v)-1]
		parts := splitCSV(inner)
		for i, p := range parts {
			parts[i] = trimQuotes(p)
		}
		return strings.Join(parts, ", ")
	}
	return trimQuotes(v)
}

// trimQuotes strips matching surrounding quotes (single or double) from s.
func trimQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// splitCSV splits a comma-separated string into trimmed, non-empty parts.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// --- FileSkillProvider ---

// FileSkillProvider discovers and manages skills stored as directories on disk.
// Each skill lives in its own subdirectory containing a SKILL.md file.
// Multiple search directories are supported; the first directory wins on
// name collisions during discovery, and is the write target for CreateSkill.
type FileSkillProvider struct {
	dirs []string
}

// NewFileSkillProvider creates a FileSkillProvider that searches the given
// directories in order for skill subdirectories.
func NewFileSkillProvider(dirs ...string) *FileSkillProvider {
	return &FileSkillProvider{dirs: dirs}
}

// Discover scans all configured directories for skill subdirectories and
// returns lightweight summaries sorted by name. Non-existent directories and
// malformed SKILL.md files are silently skipped. If the same skill name
// appears in multiple directories, the first directory wins.
func (p *FileSkillProvider) Discover(ctx context.Context) ([]SkillSummary, error) {
	seen := make(map[string]bool)
	var summaries []SkillSummary

	for _, dir := range p.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Non-existent or unreadable directory: silently skip.
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if seen[name] {
				continue
			}

			skillPath := filepath.Join(dir, name, "SKILL.md")
			f, err := os.Open(skillPath)
			if err != nil {
				continue
			}
			fm, _, err := parseFrontmatter(f)
			f.Close()
			if err != nil {
				continue
			}

			seen[name] = true

			// Folder name is the canonical identifier — it's what
			// Activate uses for lookup, so Discover must match.
			summary := SkillSummary{
				Name:        name,
				Description: fm["description"],
			}
			if tags := fm["tags"]; tags != "" {
				summary.Tags = splitCSV(tags)
			}
			summaries = append(summaries, summary)
		}
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Name < summaries[j].Name
	})

	return summaries, nil
}

// Activate loads the full skill by folder name, searching configured
// directories in order. Body (trimmed) becomes Instructions.
// Returns an error if the skill is not found.
func (p *FileSkillProvider) Activate(ctx context.Context, name string) (Skill, error) {
	for _, dir := range p.dirs {
		skillPath := filepath.Join(dir, name, "SKILL.md")
		f, err := os.Open(skillPath)
		if err != nil {
			continue
		}
		fm, body, err := parseFrontmatter(f)
		f.Close()
		if err != nil {
			continue
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
			Dir:          filepath.Join(dir, name),
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

	return Skill{}, fmt.Errorf("skill %q not found", name)
}

// CreateSkill writes a new skill to the first configured directory.
// Returns an error if no directories are configured, name is empty, or the
// skill already exists. On write failure the folder is cleaned up.
func (p *FileSkillProvider) CreateSkill(ctx context.Context, skill Skill) error {
	if len(p.dirs) == 0 {
		return fmt.Errorf("CreateSkill: no directories configured")
	}
	if skill.Name == "" {
		return fmt.Errorf("CreateSkill: skill name must not be empty")
	}

	dir := p.dirs[0]
	skillDir := filepath.Join(dir, skill.Name)

	if _, err := os.Stat(skillDir); err == nil {
		return fmt.Errorf("CreateSkill: skill %q already exists", skill.Name)
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("CreateSkill: mkdir %s: %w", skillDir, err)
	}

	skillPath := filepath.Join(skillDir, "SKILL.md")
	content := renderSkillMD(skill)
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		// Clean up folder on write failure.
		_ = os.RemoveAll(skillDir)
		return fmt.Errorf("CreateSkill: write %s: %w", skillPath, err)
	}

	return nil
}

// UpdateSkill finds an existing skill by name across all configured
// directories and rewrites its SKILL.md. Returns an error if not found.
func (p *FileSkillProvider) UpdateSkill(ctx context.Context, name string, skill Skill) error {
	for _, dir := range p.dirs {
		skillDir := filepath.Join(dir, name)
		skillPath := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}

		content := renderSkillMD(skill)
		if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("UpdateSkill: write %s: %w", skillPath, err)
		}
		return nil
	}

	return fmt.Errorf("UpdateSkill: skill %q not found", name)
}

// DeleteSkill finds a skill by name across all configured directories and
// removes its entire folder. Returns an error if not found.
func (p *FileSkillProvider) DeleteSkill(ctx context.Context, name string) error {
	for _, dir := range p.dirs {
		skillDir := filepath.Join(dir, name)
		skillPath := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}

		if err := os.RemoveAll(skillDir); err != nil {
			return fmt.Errorf("DeleteSkill: remove %s: %w", skillDir, err)
		}
		return nil
	}

	return fmt.Errorf("DeleteSkill: skill %q not found", name)
}

// renderSkillMD serialises a Skill to SKILL.md format: YAML frontmatter
// between "---" delimiters, followed by the instructions body.
func renderSkillMD(skill Skill) string {
	var sb strings.Builder

	sb.WriteString("---\n")
	sb.WriteString("name: ")
	sb.WriteString(skill.Name)
	sb.WriteString("\n")

	sb.WriteString("description: ")
	sb.WriteString(skill.Description)
	sb.WriteString("\n")

	if len(skill.Tags) > 0 {
		sb.WriteString("tags: [")
		sb.WriteString(strings.Join(skill.Tags, ", "))
		sb.WriteString("]\n")
	}
	if len(skill.Tools) > 0 {
		sb.WriteString("tools: [")
		sb.WriteString(strings.Join(skill.Tools, ", "))
		sb.WriteString("]\n")
	}
	if skill.Model != "" {
		sb.WriteString("model: ")
		sb.WriteString(skill.Model)
		sb.WriteString("\n")
	}
	if len(skill.References) > 0 {
		sb.WriteString("references: [")
		sb.WriteString(strings.Join(skill.References, ", "))
		sb.WriteString("]\n")
	}

	sb.WriteString("---\n")

	if skill.Instructions != "" {
		sb.WriteString(skill.Instructions)
		sb.WriteString("\n")
	}

	return sb.String()
}
