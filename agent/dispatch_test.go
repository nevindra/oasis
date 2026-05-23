package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

func TestStandardDispatchOrder(t *testing.T) {
	result := func(s string) agent.DispatchResult { return agent.DispatchResult{Content: s} }

	cfg := agent.StandardDispatchConfig{
		Builtins: func(_ context.Context, tc core.ToolCall, _ agent.DispatchFunc) (agent.DispatchResult, bool) {
			if tc.Name == "builtin_tool" {
				return result("builtin"), true
			}
			return agent.DispatchResult{}, false
		},
		AgentRouter: func(_ context.Context, tc core.ToolCall) (agent.DispatchResult, bool) {
			if tc.Name == "agent_x" {
				return result("router"), true
			}
			return agent.DispatchResult{}, false
		},
		ExecuteTool: func(_ context.Context, _ string, _ json.RawMessage) (core.ToolResult, error) {
			return core.TextResult("tool"), nil
		},
	}

	dispatch := agent.NewStandardDispatch(cfg)
	ctx := context.Background()

	tests := []struct {
		name string
		want string
	}{
		{"builtin_tool", "builtin"},
		{"agent_x", "router"},
		{"anything_else", "tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dispatch(ctx, core.ToolCall{Name: tt.name})
			if got.Content != tt.want {
				t.Errorf("got %q, want %q", got.Content, tt.want)
			}
		})
	}
}
