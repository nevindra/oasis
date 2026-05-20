package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

func TestInlineCompactorFullScope(t *testing.T) {
	provider := newFakeProviderReturning("[FULL_SUMMARY]")
	c := agent.NewInlineCompactor(provider)
	result, err := c.Compact(context.Background(), core.CompactRequest{
		Messages: []core.ChatMessage{
			core.UserMessage("hello"),
			core.AssistantMessage("world"),
		},
		Scope: core.ScopeFull,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.SummaryText, "FULL_SUMMARY") {
		t.Errorf("expected FULL_SUMMARY in result, got %q", result.SummaryText)
	}
}

func TestInlineCompactorToolResultsOnly(t *testing.T) {
	provider := newFakeProviderReturning("[TOOL_RESULTS_SUMMARY]")
	c := agent.NewInlineCompactor(provider)

	// Mixed input: user + tool result + assistant + tool result
	msgs := []core.ChatMessage{
		core.UserMessage("question"),
		core.ToolResultMessage("call1", "huge tool output 1"),
		core.AssistantMessage("intermediate"),
		core.ToolResultMessage("call2", "huge tool output 2"),
	}
	result, err := c.Compact(context.Background(), core.CompactRequest{
		Messages: msgs,
		Scope:    core.ScopeToolResultsOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.SummaryText, "TOOL_RESULTS_SUMMARY") {
		t.Errorf("expected TOOL_RESULTS_SUMMARY in result, got %q", result.SummaryText)
	}
}
