package mcp

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestRegistry_RouteNotification_Emits(t *testing.T) {
	r := NewRegistry(WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	events := r.Subscribe()

	cases := []struct {
		method string
		params string
		want   EventType
		check  func(Event) bool
	}{
		{"notifications/message", `{"level":"warning","data":"disk full"}`, EventLog,
			func(e Event) bool { return e.Level == LogLevelWarning && e.Message == "disk full" }},
		{"notifications/progress", `{"progressToken":"fetch#3","progress":0.5,"total":1}`, EventProgress,
			func(e Event) bool { return e.Tool == "fetch" && e.Progress == 0.5 && e.Total == 1 }},
		{"notifications/resources/updated", `{"uri":"file:///a"}`, EventResourceUpdated,
			func(e Event) bool { return e.URI == "file:///a" }},
		{"notifications/resources/list_changed", `{}`, EventResourceListChanged,
			func(e Event) bool { return true }},
		{"notifications/prompts/list_changed", `{}`, EventPromptListChanged,
			func(e Event) bool { return true }},
	}

	for _, tc := range cases {
		r.routeNotification("srv", tc.method, json.RawMessage(tc.params))
		select {
		case e := <-events:
			if e.Type != tc.want {
				t.Errorf("%s: type = %v want %v", tc.method, e.Type, tc.want)
			}
			if e.Server != "srv" {
				t.Errorf("%s: server = %q", tc.method, e.Server)
			}
			if !tc.check(e) {
				t.Errorf("%s: field check failed: %+v", tc.method, e)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s: no event emitted", tc.method)
		}
	}
}

// TestRegistry_RouteNotification_MalformedJSON asserts that malformed
// notification params do not produce a phantom zero-value event: routeNotification
// must check the unmarshal error and return without emitting.
func TestRegistry_RouteNotification_MalformedJSON(t *testing.T) {
	r := NewRegistry(WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	events := r.Subscribe()

	// Methods that decode a typed params struct — each must drop malformed JSON.
	malformed := []string{
		"notifications/message",
		"notifications/progress",
		"notifications/resources/updated",
	}
	for _, method := range malformed {
		// Bad JSON for the params field (not an object).
		r.routeNotification("srv", method, json.RawMessage(`{not valid json`))
	}

	select {
	case e := <-events:
		t.Fatalf("expected no event for malformed params, got %+v", e)
	case <-time.After(100 * time.Millisecond):
		// No event emitted — correct.
	}
}
