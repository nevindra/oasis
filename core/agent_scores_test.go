package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentResultScoresOmitempty(t *testing.T) {
	// Empty Scores must be omitted from JSON.
	b, err := json.Marshal(AgentResult{Output: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "scores") {
		t.Fatalf("empty Scores should be omitted, got %s", b)
	}

	// Populated Scores must round-trip.
	in := AgentResult{Output: "hi", Scores: []Score{{ScorerID: "kw", Value: 0.5}}}
	b, _ = json.Marshal(in)
	var out AgentResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Scores) != 1 || out.Scores[0].ScorerID != "kw" {
		t.Fatalf("round-trip lost scores: %+v", out.Scores)
	}
}
