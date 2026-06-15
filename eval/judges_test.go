package eval

import (
	"context"
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestBuiltinJudges(t *testing.T) {
	p := &stubProvider{resp: core.ChatResponse{Content: `{"score":0.5,"reason":"ok"}`}}

	judges := map[string]core.Scorer{
		"answer_relevancy":       AnswerRelevancy(p),
		"faithfulness":           Faithfulness(p),
		"hallucination":          Hallucination(p),
		"answer_similarity":      AnswerSimilarity(p),
		"context_precision":      ContextPrecision(p),
		"context_relevance":      ContextRelevance(p),
		"bias":                   Bias(p),
		"toxicity":               Toxicity(p),
		"prompt_alignment":       PromptAlignment(p),
		"tool_call_accuracy_llm": ToolCallAccuracyLLM(p),
		"trajectory_llm":         TrajectoryLLM(p),
		"rubric":                 Rubric(p, "Be concise and correct."),
	}

	for wantID, sc := range judges {
		if sc.ID() != wantID {
			t.Errorf("ID = %q, want %q", sc.ID(), wantID)
		}
		if _, ok := sc.(core.AsyncScorer); !ok {
			t.Errorf("%s must implement core.AsyncScorer", wantID)
		}
		got, err := sc.Score(context.Background(), core.ScorerRun{Input: "q", Output: "a"})
		if err != nil {
			t.Errorf("%s.Score: %v", wantID, err)
		}
		if got.Value != 0.5 {
			t.Errorf("%s value = %v, want 0.5", wantID, got.Value)
		}
	}
}

func TestRubricEmbedsCriteria(t *testing.T) {
	p := &stubProvider{resp: core.ChatResponse{Content: `{"score":1,"reason":"x"}`}}
	_, _ = Rubric(p, "MUST_INCLUDE_THIS_CRITERION").Score(context.Background(), core.ScorerRun{})
	if p.got == nil || !contains(p.got.Messages[0].Content, "MUST_INCLUDE_THIS_CRITERION") {
		t.Fatal("rubric criteria not embedded in system prompt")
	}
}
