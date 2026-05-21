package agent

import (
	"testing"
)

// Verifies that when a Network forwards subagent events to the parent channel,
// the subagent's EventRunStart and EventRunFinish are suppressed (the Network
// uses its own EventAgentStart / EventAgentFinish envelope).
func TestSubagentEnvelopeSuppressed(t *testing.T) {
	// Construct a Network with one subagent that produces a single-text-delta run.
	// Assert no EventRunStart or EventRunFinish from the subagent leaks through.
	t.Skip("implementation depends on network test harness; flesh out during execution")
}
