// network/taskrole_test.go
//
// Tests for the task tool's goal/context split and the per-dispatch "leaf"
// role: composition of the child's input, legacy task-field fallback, leaf
// propagation through dispatch, and the leaf restriction on routers.
package network

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

// TestTaskGoalContextComposition: a task call carrying goal + context hands
// the child a composed input (goal, then a Context: block); the legacy task
// field still dispatches; the advertised schema requires goal and offers
// context + role.
func TestTaskGoalContextComposition(t *testing.T) {
	var inputs []string
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			inputs = append(inputs, task.Input)
			return agent.AgentResult{Output: "done"}, nil
		},
	}

	var taskParams string
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			if countAssistantToolTurns(req) == 0 {
				for _, tl := range req.Tools {
					if tl.Name == core.ToolTask {
						taskParams = string(tl.Parameters)
					}
				}
				a1, _ := json.Marshal(map[string]string{
					"subagent": "worker",
					"goal":     "summarize the report",
					"context":  "file lives at /tmp/report.md; keep figures exact",
				})
				a2, _ := json.Marshal(map[string]string{"subagent": "worker", "task": "legacy shape"})
				return core.ChatResponse{ToolCalls: []core.ToolCall{
					{ID: "1", Name: core.ToolTask, Args: a1},
					{ID: "2", Name: core.ToolTask, Args: a2},
				}}
			}
			return core.ChatResponse{Content: "merged"}
		},
	}

	net := New("team", "goal/context", router, WithChildren(sub))
	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}); err != nil {
		t.Fatal(err)
	}

	if len(inputs) != 2 {
		t.Fatalf("child executed %d times, want 2", len(inputs))
	}
	want := "summarize the report\n\nContext:\nfile lives at /tmp/report.md; keep figures exact"
	joined := strings.Join(inputs, "\x00")
	if !strings.Contains(joined, want) {
		t.Errorf("composed input missing; got %q", inputs)
	}
	if !strings.Contains(joined, "legacy shape") {
		t.Errorf("legacy task field no longer dispatches; got %q", inputs)
	}

	if !strings.Contains(taskParams, `"required":["subagent","goal"]`) {
		t.Errorf("schema does not require subagent+goal: %s", taskParams)
	}
	for _, prop := range []string{`"context"`, `"role"`, `"leaf"`} {
		if !strings.Contains(taskParams, prop) {
			t.Errorf("schema missing %s: %s", prop, taskParams)
		}
	}
}

// TestTaskLeafRolePropagation: role "leaf" rides the context into the child's
// Execute; omitting role leaves the child unrestricted.
func TestTaskLeafRolePropagation(t *testing.T) {
	leafSeen := map[string]bool{}
	sub := &ctxStubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(ctx context.Context, task agent.AgentTask) (agent.AgentResult, error) {
			leafSeen[task.Input] = agent.IsLeafRole(ctx)
			return agent.AgentResult{Output: "done"}, nil
		},
	}

	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			if countAssistantToolTurns(req) == 0 {
				a1, _ := json.Marshal(map[string]string{"subagent": "worker", "goal": "restricted", "role": "leaf"})
				a2, _ := json.Marshal(map[string]string{"subagent": "worker", "goal": "unrestricted"})
				return core.ChatResponse{ToolCalls: []core.ToolCall{
					{ID: "1", Name: core.ToolTask, Args: a1},
					{ID: "2", Name: core.ToolTask, Args: a2},
				}}
			}
			return core.ChatResponse{Content: "merged"}
		},
	}

	net := New("team", "leaf propagation", router, WithChildren(sub))
	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}); err != nil {
		t.Fatal(err)
	}

	if got, ok := leafSeen["restricted"]; !ok || !got {
		t.Errorf("leaf-dispatched child did not see the leaf restriction (seen=%v)", leafSeen)
	}
	if got, ok := leafSeen["unrestricted"]; !ok || got {
		t.Errorf("default-dispatched child must not be leaf-restricted (seen=%v)", leafSeen)
	}
}

// TestTaskInvalidRole: an unknown role errors back to the model with the
// valid values instead of silently degrading.
func TestTaskInvalidRole(t *testing.T) {
	var childExecs atomic.Int32
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			childExecs.Add(1)
			return agent.AgentResult{Output: "done"}, nil
		},
	}

	var results []string
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			for _, m := range req.Messages {
				if m.Role == "tool" {
					results = append(results, m.Content)
				}
			}
			if countAssistantToolTurns(req) == 0 {
				args, _ := json.Marshal(map[string]string{"subagent": "worker", "goal": "work", "role": "orchestrator"})
				return core.ChatResponse{ToolCalls: []core.ToolCall{{ID: "1", Name: core.ToolTask, Args: args}}}
			}
			return core.ChatResponse{Content: "merged"}
		},
	}

	net := New("team", "invalid role", router, WithChildren(sub))
	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}); err != nil {
		t.Fatal(err)
	}

	if childExecs.Load() != 0 {
		t.Errorf("child executed %d times, want 0 (invalid role must not dispatch)", childExecs.Load())
	}
	joined := strings.Join(results, "\n")
	if !strings.Contains(joined, `invalid role "orchestrator"`) {
		t.Errorf("tool result missing invalid-role error, got:\n%s", joined)
	}
}

// TestLeafRouterCannotDelegate: a network router executed under the leaf
// restriction loses its delegation surface — the task tool is not advertised
// and a forced task call is refused — while direct answering still works.
func TestLeafRouterCannotDelegate(t *testing.T) {
	var childExecs atomic.Int32
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			childExecs.Add(1)
			return agent.AgentResult{Output: "done"}, nil
		},
	}

	var advertised []string
	var results []string
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			for _, m := range req.Messages {
				if m.Role == "tool" {
					results = append(results, m.Content)
				}
			}
			if countAssistantToolTurns(req) == 0 {
				advertised = nil
				for _, tl := range req.Tools {
					advertised = append(advertised, tl.Name)
				}
				// Force a task call despite the stripped defs — must be refused.
				args, _ := json.Marshal(map[string]string{"subagent": "worker", "goal": "work"})
				return core.ChatResponse{ToolCalls: []core.ToolCall{{ID: "1", Name: core.ToolTask, Args: args}}}
			}
			return core.ChatResponse{Content: "leaf answer"}
		},
	}

	net := New("team", "leaf router", router,
		WithChildren(sub),
		WithAgentOptions(agent.WithSelfClone(2, time.Minute)),
	)

	result, err := net.Execute(agent.WithLeafRole(context.Background()), agent.AgentTask{Input: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "leaf answer" {
		t.Errorf("output = %q, want leaf answer", result.Output)
	}
	for _, name := range advertised {
		if name == core.ToolTask || name == core.ToolSpawnAgent {
			t.Errorf("leaf-restricted router still advertises %q", name)
		}
	}
	if childExecs.Load() != 0 {
		t.Errorf("child executed %d times, want 0 (leaf router must not delegate)", childExecs.Load())
	}
	joined := strings.Join(results, "\n")
	if !strings.Contains(joined, "delegation is disabled for this task (role: leaf)") {
		t.Errorf("forced task call not refused with leaf error, got:\n%s", joined)
	}
}

// TestLeafSelfCloneLosesRoster: dispatching "self" with role "leaf" spawns a
// clone that is NOT offered the task tool, even though the network installs
// its roster on every clone.
func TestLeafSelfCloneLosesRoster(t *testing.T) {
	sub := &stubAgent{
		name: "worker",
		desc: "Does work",
		fn: func(task agent.AgentTask) (agent.AgentResult, error) {
			return agent.AgentResult{Output: "done"}, nil
		},
	}

	cloneOfferedTask := false
	cloneRan := false
	router := &routerCallbackProvider{
		name: "router",
		onChat: func(req core.ChatRequest) core.ChatResponse {
			last := req.Messages[len(req.Messages)-1]
			if last.Role == "user" && last.Content == "CLONEWORK" {
				cloneRan = true
				for _, tl := range req.Tools {
					if tl.Name == core.ToolTask {
						cloneOfferedTask = true
					}
				}
				return core.ChatResponse{Content: "clone done"}
			}
			if countAssistantToolTurns(req) == 0 {
				args, _ := json.Marshal(map[string]string{"subagent": "self", "goal": "CLONEWORK", "role": "leaf"})
				return core.ChatResponse{ToolCalls: []core.ToolCall{{ID: "1", Name: core.ToolTask, Args: args}}}
			}
			return core.ChatResponse{Content: "merged"}
		},
	}

	net := New("team", "leaf clone", router,
		WithChildren(sub),
		WithAgentOptions(agent.WithSelfClone(2, time.Minute)),
	)

	if _, err := net.Execute(context.Background(), agent.AgentTask{Input: "go"}); err != nil {
		t.Fatal(err)
	}
	if !cloneRan {
		t.Fatal("self-clone never ran")
	}
	if cloneOfferedTask {
		t.Error("leaf-dispatched self-clone was still offered the task tool")
	}
}
