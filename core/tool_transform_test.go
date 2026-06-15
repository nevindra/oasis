package core

import (
	"encoding/json"
	"testing"
)

func TestToolTransform_SinkFuncsInvocable(t *testing.T) {
	tt := ToolTransform{
		Display: &SinkTransform{
			Result: func(name string, r ToolResult) ToolResult {
				return ToolResult{Content: "redacted:" + name}
			},
			Args: func(name string, args json.RawMessage) json.RawMessage {
				return json.RawMessage(`{}`)
			},
		},
	}

	got := tt.Display.Result("greet", ToolResult{Content: "secret"})
	if got.Content != "redacted:greet" {
		t.Errorf("Result content = %q, want %q", got.Content, "redacted:greet")
	}
	if string(tt.Display.Args("greet", json.RawMessage(`{"k":"v"}`))) != `{}` {
		t.Errorf("Args not applied")
	}

	// Zero value: all sinks nil.
	var zero ToolTransform
	if zero.Model != nil || zero.Display != nil || zero.Transcript != nil {
		t.Error("zero ToolTransform should have nil sinks")
	}
}
