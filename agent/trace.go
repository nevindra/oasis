package agent

import (
	"encoding/json"
	"errors"
	"strings"
)

// appendStepBounded appends trace to steps, enforcing the max cap. When max <= 0
// the slice grows without bound. When full, the oldest entry is dropped and the
// newest takes its place (ring-buffer semantics via in-place copy).
func appendStepBounded(steps []StepTrace, trace StepTrace, max int) []StepTrace {
	if max <= 0 || len(steps) < max {
		return append(steps, trace)
	}
	copy(steps, steps[1:])
	steps[len(steps)-1] = trace
	return steps
}

// handleProcessorErrorWithSteps converts a processor error into an AgentResult.
// ErrHalt produces a graceful result; other errors propagate as failures.
// Any step traces collected before the error are preserved in the result.
func handleProcessorErrorWithSteps(err error, usage Usage, steps []StepTrace) (AgentResult, error) {
	var halt *ErrHalt
	if errors.As(err, &halt) {
		return AgentResult{Output: halt.Response, Usage: usage, Steps: steps}, nil
	}
	return AgentResult{Usage: usage, Steps: steps}, err
}

// buildStepTrace creates a StepTrace from a tool call and its execution result.
// Agent delegations (tool calls prefixed with "agent_") get Type "agent" and
// the prefix stripped from Name. All other calls get Type "tool".
func buildStepTrace(tc ToolCall, res toolExecResult) StepTrace {
	name := tc.Name
	traceType := "tool"
	input := string(tc.Args)

	if after, ok := strings.CutPrefix(name, "agent_"); ok {
		name = after
		traceType = "agent"
		// Extract the task field from agent call args for a cleaner trace.
		var params struct {
			Task string `json:"task"`
		}
		if json.Unmarshal(tc.Args, &params) == nil && params.Task != "" {
			input = params.Task
		}
	} else if tc.Name == "spawn_agent" {
		traceType = "agent"
		var params spawnAgentArgs
		if json.Unmarshal(tc.Args, &params) == nil {
			input = params.Task
			name = spawnAgentName(params)
		}
	}

	return StepTrace{
		Name:     name,
		Type:     traceType,
		Input:    TruncateStr(input, 200),
		Output:   TruncateStr(res.content, 500),
		Usage:    res.usage,
		Duration: res.duration,
	}
}
