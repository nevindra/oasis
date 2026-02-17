package oasis

import "testing"

func TestUserMessage(t *testing.T) {
	msg := UserMessage("hello")
	if msg.Role != "user" {
		t.Errorf("Role = %q, want %q", msg.Role, "user")
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want %q", msg.Content, "hello")
	}
	if msg.ToolCallID != "" {
		t.Errorf("ToolCallID = %q, want empty", msg.ToolCallID)
	}
	if len(msg.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want empty", msg.ToolCalls)
	}
	if len(msg.Images) != 0 {
		t.Errorf("Images = %v, want empty", msg.Images)
	}
	if msg.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", msg.Metadata)
	}
}

func TestSystemMessage(t *testing.T) {
	msg := SystemMessage("you are helpful")
	if msg.Role != "system" {
		t.Errorf("Role = %q, want %q", msg.Role, "system")
	}
	if msg.Content != "you are helpful" {
		t.Errorf("Content = %q, want %q", msg.Content, "you are helpful")
	}
}

func TestAssistantMessage(t *testing.T) {
	msg := AssistantMessage("sure thing")
	if msg.Role != "assistant" {
		t.Errorf("Role = %q, want %q", msg.Role, "assistant")
	}
	if msg.Content != "sure thing" {
		t.Errorf("Content = %q, want %q", msg.Content, "sure thing")
	}
}

func TestToolResultMessage(t *testing.T) {
	msg := ToolResultMessage("call-123", "result data")
	if msg.Role != "tool" {
		t.Errorf("Role = %q, want %q", msg.Role, "tool")
	}
	if msg.Content != "result data" {
		t.Errorf("Content = %q, want %q", msg.Content, "result data")
	}
	if msg.ToolCallID != "call-123" {
		t.Errorf("ToolCallID = %q, want %q", msg.ToolCallID, "call-123")
	}
}

func TestToolResultMessageFields(t *testing.T) {
	callID := "call-abc"
	content := "tool output"
	msg := ToolResultMessage(callID, content)

	// callID must go to ToolCallID, not Content
	if msg.ToolCallID != callID {
		t.Errorf("ToolCallID = %q, want %q (callID)", msg.ToolCallID, callID)
	}
	if msg.Content == callID {
		t.Error("Content contains callID; callID should only be in ToolCallID")
	}

	// content must go to Content, not ToolCallID
	if msg.Content != content {
		t.Errorf("Content = %q, want %q (content)", msg.Content, content)
	}
	if msg.ToolCallID == content {
		t.Error("ToolCallID contains content; content should only be in Content")
	}
}

func TestMessageConstructorsEmpty(t *testing.T) {
	tests := []struct {
		name string
		msg  ChatMessage
		role string
	}{
		{"UserMessage", UserMessage(""), "user"},
		{"SystemMessage", SystemMessage(""), "system"},
		{"AssistantMessage", AssistantMessage(""), "assistant"},
		{"ToolResultMessage", ToolResultMessage("", ""), "tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.msg.Role != tt.role {
				t.Errorf("%s(\"\").Role = %q, want %q", tt.name, tt.msg.Role, tt.role)
			}
		})
	}
}
