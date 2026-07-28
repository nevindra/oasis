package agent

import (
	"context"
	"strings"
	"testing"
)

func TestTaskToolArgsEffectiveTask(t *testing.T) {
	cases := []struct {
		name string
		args TaskToolArgs
		want string
	}{
		{"goal only", TaskToolArgs{Goal: "do X"}, "do X"},
		{"goal and context", TaskToolArgs{Goal: "do X", Context: "in /tmp"}, "do X\n\nContext:\nin /tmp"},
		{"legacy task", TaskToolArgs{Task: "old shape"}, "old shape"},
		{"goal wins over legacy", TaskToolArgs{Goal: "new", Task: "old"}, "new"},
		{"context without goal or task is empty", TaskToolArgs{Context: "orphan"}, ""},
		{"whitespace goal falls back", TaskToolArgs{Goal: "  ", Task: "old"}, "old"},
	}
	for _, c := range cases {
		if got := c.args.EffectiveTask(); got != c.want {
			t.Errorf("%s: EffectiveTask() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTaskToolArgsRole(t *testing.T) {
	for _, role := range []string{"", RoleAuto, RoleLeaf} {
		if !(TaskToolArgs{Role: role}).ValidRole() {
			t.Errorf("role %q should be valid", role)
		}
	}
	if (TaskToolArgs{Role: "orchestrator"}).ValidRole() {
		t.Error(`role "orchestrator" should be invalid`)
	}
	if !(TaskToolArgs{Role: RoleLeaf}).IsLeaf() || (TaskToolArgs{Role: RoleAuto}).IsLeaf() {
		t.Error("IsLeaf must be true only for leaf")
	}
}

func TestLeafRoleContext(t *testing.T) {
	ctx := context.Background()
	if IsLeafRole(ctx) {
		t.Error("fresh context must not be leaf-scoped")
	}
	if !IsLeafRole(WithLeafRole(ctx)) {
		t.Error("WithLeafRole context must report leaf")
	}
}

func TestBuildTaskToolDefSchema(t *testing.T) {
	def := BuildTaskToolDef([]TaskTarget{{Name: "worker", Description: "does work"}}, true, 3)
	params := string(def.Parameters)
	if !strings.Contains(params, `"required":["subagent","goal"]`) {
		t.Errorf("schema must require subagent+goal: %s", params)
	}
	for _, want := range []string{`"context"`, `"role"`, `"auto"`, `"leaf"`, `"worker"`, `"self"`} {
		if !strings.Contains(params, want) {
			t.Errorf("schema missing %s: %s", want, params)
		}
	}
	if !strings.Contains(def.Description, "worker") {
		t.Errorf("description missing roster entry: %s", def.Description)
	}
}
