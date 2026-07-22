// agent/tasktool.go
//
// The unified "task" delegation tool (deepagents' task(description,
// subagent_type) shape): ONE tool covers every hand-off — named subagents on
// a network roster and "self" (an ephemeral copy of the calling agent). The
// routing surface the model sees is the subagent enum plus the roster in the
// tool description, instead of one agent_<name> tool per child.
package agent

import (
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

// TaskToolArgs is the parsed arguments of one task tool call.
type TaskToolArgs struct {
	Subagent string `json:"subagent"`
	Task     string `json:"task"`
}

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
	params := fmt.Sprintf(`{"type":"object","properties":{"subagent":{"type":"string","enum":%s,"description":"Which subagent runs the task."},"task":{"type":"string","description":"The complete, self-contained assignment. The subagent CANNOT see this conversation: include every fact, constraint, and piece of prior context it needs, plus what its final report must contain. Preserve the user's language and exact figures."}},"required":["subagent","task"]}`, enumJSON)

	return core.ToolDefinition{
		Name: core.ToolTask,
		Description: "Delegate a self-contained task to a subagent. The call blocks until the subagent finishes and returns its final report as the tool result" +
			` (a result starting with "error: " means it failed — retry once or report the failure; never silently redo its work yourself).` +
			" The subagent cannot see this conversation, so the task must be fully self-contained." +
			" To run independent tasks in parallel, issue ALL of those task calls together in a single response." +
			" Never re-delegate a task that already returned a result.\n\nAvailable subagents:\n" + roster.String(),
		Parameters: json.RawMessage(params),
	}
}
