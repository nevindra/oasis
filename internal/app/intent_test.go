package app

import (
	"testing"

	oasis "github.com/nevindra/oasis"
)

func TestParseIntentChat(t *testing.T) {
	if ParseIntent(`{"intent":"chat"}`) != oasis.IntentChat {
		t.Error("expected Chat")
	}
}

func TestParseIntentAction(t *testing.T) {
	if ParseIntent(`{"intent":"action"}`) != oasis.IntentAction {
		t.Error("expected Action")
	}
}

func TestParseIntentInvalidJSON(t *testing.T) {
	if ParseIntent("not json") != oasis.IntentAction {
		t.Error("expected Action on invalid JSON")
	}
}

func TestParseIntentCodeFence(t *testing.T) {
	if ParseIntent("```json\n{\"intent\":\"chat\"}\n```") != oasis.IntentChat {
		t.Error("expected Chat from code fence")
	}
}

func TestParseIntentUnknown(t *testing.T) {
	if ParseIntent(`{"intent":"unknown"}`) != oasis.IntentAction {
		t.Error("expected Action on unknown intent")
	}
}

func TestParseIntentSurroundingText(t *testing.T) {
	if ParseIntent("Here is the result: {\"intent\":\"chat\"} done.") != oasis.IntentChat {
		t.Error("expected Chat from extracted JSON")
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{`{"intent":"chat"}`, `{"intent":"chat"}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"prefix {\"x\":2} suffix", `{"x":2}`},
		{"no json here", "no json here"},
	}
	for _, c := range cases {
		got := extractJSON(c.input)
		if got != c.want {
			t.Errorf("extractJSON(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
