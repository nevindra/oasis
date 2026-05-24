package agent_test

import (
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

func TestLLMAgentSatisfiesCoreAgent(t *testing.T) {
	var _ core.Agent = (*agent.LLMAgent)(nil)
}
