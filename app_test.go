package oasis

import "testing"

func TestNewApp(t *testing.T) {
	a := New(
		WithSystemPrompt("You are a test bot."),
		WithMaxToolIterations(5),
	)
	if a.systemPrompt != "You are a test bot." {
		t.Error("system prompt not set")
	}
	if a.maxIter != 5 {
		t.Error("max iterations not set")
	}
	if a.tools == nil {
		t.Error("tool registry should be initialized")
	}
}

func TestAppRunRequiresComponents(t *testing.T) {
	a := New()
	err := a.Run(t.Context())
	if err == nil {
		t.Error("expected error without required components")
	}
}
