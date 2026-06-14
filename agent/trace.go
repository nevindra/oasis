package agent

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/nevindra/oasis/core"
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
func handleProcessorErrorWithSteps(err error, usage core.Usage, steps []StepTrace) (AgentResult, error) {
	var halt *core.ErrHalt
	if errors.As(err, &halt) {
		return AgentResult{Output: halt.Response, Usage: usage, Steps: steps}, nil
	}
	return AgentResult{Usage: usage, Steps: steps}, err
}

// buildStepTrace creates a StepTrace from a tool call and its execution result.
// Agent delegations (tool calls prefixed with "agent_") get Type StepTypeAgent
// and the prefix stripped from Name. All other calls get StepTypeTool.
func buildStepTrace(tc core.ToolCall, res toolExecResult) StepTrace {
	name := tc.Name
	traceType := core.StepTypeTool
	input := string(tc.Args)

	if after, ok := strings.CutPrefix(name, core.ToolPrefixAgent); ok {
		name = after
		traceType = core.StepTypeAgent
		// Extract the task field from agent call args for a cleaner trace.
		var params struct {
			Task string `json:"task"`
		}
		if json.Unmarshal(tc.Args, &params) == nil && params.Task != "" {
			input = params.Task
		}
	}

	return StepTrace{
		Name:    name,
		Type:    traceType,
		Input:   TruncateStr(input, 200),
		Output:  TruncateStr(res.content, 500),
		RawArgs: json.RawMessage(tc.Args),
		// Why: res.content is an immutable string the tool already owns;
		// assigning it directly is zero-copy. Typing RawOutput as []byte-backed
		// json.RawMessage here used to copy the full payload per step — the
		// dominant allocation for large tool results.
		RawOutput: res.content,
		Usage:     res.usage,
		Duration:  res.duration,
	}
}
