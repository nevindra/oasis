// agent/tasktool.go
//
// The unified "task" delegation tool (deepagents' task(description,
// subagent_type) shape): ONE tool covers every hand-off — named subagents on
// a network roster and "self" (an ephemeral copy of the calling agent). The
// routing surface the model sees is the subagent enum plus the roster in the
// tool description, instead of one agent_<name> tool per child.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/internal/runtime"
)

// TaskTarget is one deliverable-to entry advertised in the task tool.
// Aliased to the runtime type so it can ride on Config (Config.TaskRoster)
// without an agent→runtime cycle.
type TaskTarget = runtime.TaskRosterEntry

// TaskSelf is the reserved subagent value for spawning an ephemeral copy of
// the calling agent.
const TaskSelf = "self"

// Task role values: RoleAuto (default) keeps the subagent's own delegation
// surface; RoleLeaf strips it for that one dispatch, so the child must do the
// work itself.
const (
	RoleAuto = "auto"
	RoleLeaf = "leaf"
)

// TaskToolArgs is the parsed arguments of one task tool call.
type TaskToolArgs struct {
	Subagent string `json:"subagent"`
	// Goal is what the subagent must accomplish; Context is optional
	// background (file paths, constraints, prior findings) composed after it.
	Goal    string `json:"goal"`
	Context string `json:"context"`
	// Role controls whether the child keeps its delegation surface: "" /
	// "auto" preserves it, "leaf" strips it for this dispatch.
	Role string `json:"role"`
	// Task is the legacy single-field shape, still accepted so tool calls
	// replayed from pre-goal/context history keep parsing.
	Task string `json:"task"`
}

// EffectiveTask composes the child's input from goal + context, falling back
// to the legacy task field. Empty means the call carried no assignment.
func (a TaskToolArgs) EffectiveTask() string {
	text := strings.TrimSpace(a.Goal)
	if text == "" {
		text = strings.TrimSpace(a.Task)
	}
	if c := strings.TrimSpace(a.Context); c != "" && text != "" {
		text += "\n\nContext:\n" + c
	}
	return text
}

// ValidRole reports whether the role value is one the task tool accepts.
func (a TaskToolArgs) ValidRole() bool {
	return a.Role == "" || a.Role == RoleAuto || a.Role == RoleLeaf
}

// IsLeaf reports whether this call restricts the child to leaf execution.
func (a TaskToolArgs) IsLeaf() bool { return a.Role == RoleLeaf }

// BuildTaskToolDef assembles the single task tool definition for a roster of
// targets (possibly empty) plus optional self-cloning. maxClones is only used
// for the self entry's wording.
func BuildTaskToolDef(targets []TaskTarget, selfEnabled bool, maxClones int) core.ToolDefinition {
	names := make([]string, 0, len(targets)+1)
	var roster strings.Builder
	for _, t := range targets {
		names = append(names, t.Name)
		fmt.Fprintf(&roster, "- %q: %s\n", t.Name, t.Description)
	}
	if selfEnabled {
		names = append(names, TaskSelf)
		fmt.Fprintf(&roster, "- %q: an ephemeral copy of yourself — same instructions and tools, fresh context. Use for parallelizable subtasks that need no specialist (at most %d copies per run).\n", TaskSelf, maxClones)
	}

	enumJSON, _ := json.Marshal(names)
	params := fmt.Sprintf(`{"type":"object","properties":{"subagent":{"type":"string","enum":%s,"description":"Which subagent runs the task."},"goal":{"type":"string","description":"What the subagent must accomplish, plus what its final report must contain. The subagent CANNOT see this conversation. Preserve the user's language and exact figures."},"context":{"type":"string","description":"Background the subagent needs to succeed: file paths, error messages, constraints, decisions already made, relevant prior findings. The more specific, the better the result — the subagent knows nothing beyond goal and context."},"role":{"type":"string","enum":["auto","leaf"],"description":"auto (default) keeps the subagent's own ability to delegate further; leaf forbids it — the subagent must do the work itself. Use leaf for focused single-agent work to avoid unnecessary fan-out."}},"required":["subagent","goal"]}`, enumJSON)

	return core.ToolDefinition{
		Name: core.ToolTask,
		Description: "Delegate a self-contained task to a subagent. The call blocks until the subagent finishes and returns its final report as the tool result" +
			` (a result starting with "error: " means it failed — retry once or report the failure; never silently redo its work yourself).` +
			" The subagent cannot see this conversation, so goal + context together must be fully self-contained." +
			" To run independent tasks in parallel, issue ALL of those task calls together in a single response." +
			" Never re-delegate a task that already returned a result.\n\nAvailable subagents:\n" + roster.String(),
		Parameters: json.RawMessage(params),
	}
}

// leafRoleKey marks a context as leaf-scoped: the agent executing under it
// was dispatched with role "leaf" and must not delegate further. The flag
// rides the context so it reaches every implementation (LLMAgent, network
// router, self-clone) without threading new parameters through dispatch.
type leafRoleKey struct{}

// WithLeafRole returns a context whose subtree executes under the leaf
// restriction: task-tool definitions are not advertised and delegation
// dispatch is refused.
func WithLeafRole(ctx context.Context) context.Context {
	return context.WithValue(ctx, leafRoleKey{}, true)
}

// IsLeafRole reports whether ctx carries the leaf restriction.
func IsLeafRole(ctx context.Context) bool {
	v, _ := ctx.Value(leafRoleKey{}).(bool)
	return v
}
