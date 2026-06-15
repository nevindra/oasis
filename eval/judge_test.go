package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/nevindra/oasis/core"
)

// stubProvider is a core.Provider for tests: it records the request and returns
// a canned response. Shared by judge/judges/run tests in this package.
type stubProvider struct {
	resp core.ChatResponse
	err  error
	got  *core.ChatRequest
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) ChatStream(_ context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	s.got = &req
	if ch != nil { // core.Chat passes nil; guard anyway
		close(ch)
	}
	return s.resp, s.err
}

func TestLLMJudgeParsesScoreAndSetsSchema(t *testing.T) {
	p := &stubProvider{resp: core.ChatResponse{Content: `{"score":0.8,"reason":"grounded"}`}}
	j := newJudge("faithfulness", "judge faithfulness", p)

	got, err := j.Score(context.Background(), core.ScorerRun{Input: "q", Output: "a"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got.ScorerID != "faithfulness" || got.Value != 0.8 || got.Reason != "grounded" {
		t.Fatalf("unexpected score: %+v", got)
	}
	if !j.PrefersAsync() {
		t.Fatal("LLM judge must prefer async")
	}
	if p.got == nil || p.got.ResponseSchema == nil {
		t.Fatal("ResponseSchema must be set on the request")
	}
	if len(p.got.Messages) != 2 || p.got.Messages[0].Role != core.RoleSystem || p.got.Messages[1].Role != core.RoleUser {
		t.Fatalf("expected system+user messages, got %+v", p.got.Messages)
	}
}

func TestLLMJudgeClampsAndErrors(t *testing.T) {
	// Out-of-range score clamps to [0,1].
	hi := &stubProvider{resp: core.ChatResponse{Content: `{"score":1.5,"reason":"x"}`}}
	got, _ := newJudge("x", "", hi).Score(context.Background(), core.ScorerRun{})
	if got.Value != 1.0 {
		t.Fatalf("clamp high: got %v", got.Value)
	}
	// Malformed JSON → infrastructure error.
	bad := &stubProvider{resp: core.ChatResponse{Content: "not json"}}
	if _, err := newJudge("x", "", bad).Score(context.Background(), core.ScorerRun{}); err == nil {
		t.Fatal("expected parse error")
	}
	// Provider error → propagated.
	boom := &stubProvider{err: errors.New("boom")}
	if _, err := newJudge("x", "", boom).Score(context.Background(), core.ScorerRun{}); err == nil {
		t.Fatal("expected provider error")
	}
}

func TestBuildJudgeInputIncludesSections(t *testing.T) {
	run := core.ScorerRun{
		Input:       "the question",
		Output:      "the answer",
		GroundTruth: "the reference",
		Context:     []string{"chunk one"},
		Steps:       []core.StepTrace{{Name: "web_search", Type: core.StepTypeTool, Input: "q"}},
		Expected:    &core.ExpectedTrajectory{Steps: []core.ExpectedStep{{Name: "web_search"}}},
	}
	s := buildJudgeInput(run)
	for _, want := range []string{"the question", "the answer", "the reference", "chunk one", "web_search"} {
		if !contains(s, want) {
			t.Errorf("buildJudgeInput missing %q in:\n%s", want, s)
		}
	}
}

// contains is a tiny test helper (strings.Contains without importing strings here).
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
