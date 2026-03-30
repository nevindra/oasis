package oasis

import (
	"os"
	"path/filepath"
)

// DefaultSkillDirs returns the standard AgentSkills-compatible scan paths:
//   - <cwd>/.agents/skills/ (project-level)
//   - ~/.agents/skills/ (user-level)
//
// Directories that do not exist are included — FileSkillProvider handles
// missing directories gracefully.
func DefaultSkillDirs() []string {
	var dirs []string
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(cwd, ".agents", "skills"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".agents", "skills"))
	}
	return dirs
}
