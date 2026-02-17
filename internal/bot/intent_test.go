package bot

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

func TestParseIntentUnknown(t *testing.T) {
	if ParseIntent(`{"intent":"unknown"}`) != oasis.IntentAction {
		t.Error("expected Action on unknown intent")
	}
}

