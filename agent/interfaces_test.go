package agent_test

import (
	"testing"

	"github.com/nevindra/oasis/agent"
)

func TestLLMAgentSatisfiesAgentWithOptions(t *testing.T) {
	var _ agent.AgentWithOptions = (*agent.LLMAgent)(nil)
}

func TestLLMAgentSatisfiesStreamingAgentWithOptions(t *testing.T) {
	var _ agent.StreamingAgentWithOptions = (*agent.LLMAgent)(nil)
}
