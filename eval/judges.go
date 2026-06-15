package eval

import "github.com/nevindra/oasis/core"

// AnswerRelevancy scores how well the output addresses the input question.
func AnswerRelevancy(provider core.Provider) core.Scorer {
	return newJudge("answer_relevancy", answerRelevancyInstr, provider)
}

// Faithfulness scores whether the output's claims are grounded in the context.
func Faithfulness(provider core.Provider) core.Scorer {
	return newJudge("faithfulness", faithfulnessInstr, provider)
}

// Hallucination scores the output's freedom from contradictions/inventions (1.0 = none).
func Hallucination(provider core.Provider) core.Scorer {
	return newJudge("hallucination", hallucinationInstr, provider)
}

// AnswerSimilarity scores semantic closeness of the output to the reference answer.
func AnswerSimilarity(provider core.Provider) core.Scorer {
	return newJudge("answer_similarity", answerSimilarityInstr, provider)
}

// ContextPrecision scores how precisely retrieved context was used.
func ContextPrecision(provider core.Provider) core.Scorer {
	return newJudge("context_precision", contextPrecisionInstr, provider)
}

// ContextRelevance scores whether retrieved context was relevant to the question.
func ContextRelevance(provider core.Provider) core.Scorer {
	return newJudge("context_relevance", contextRelevanceInstr, provider)
}

// Bias scores the output's freedom from bias (1.0 = unbiased).
func Bias(provider core.Provider) core.Scorer {
	return newJudge("bias", biasInstr, provider)
}

// Toxicity scores the output's safety (1.0 = safe).
func Toxicity(provider core.Provider) core.Scorer {
	return newJudge("toxicity", toxicityInstr, provider)
}

// PromptAlignment scores whether the output follows the input's constraints.
func PromptAlignment(provider core.Provider) core.Scorer {
	return newJudge("prompt_alignment", promptAlignmentInstr, provider)
}

// ToolCallAccuracyLLM scores tool-call appropriateness via an LLM judge — the
// semantic variant of ToolCallAccuracy. Distinct ID avoids collision.
func ToolCallAccuracyLLM(provider core.Provider) core.Scorer {
	return newJudge("tool_call_accuracy_llm", toolCallAccuracyInstr, provider)
}

// TrajectoryLLM scores the agent's action sequence via an LLM judge — the
// semantic variant of Trajectory. Distinct ID avoids collision.
func TrajectoryLLM(provider core.Provider) core.Scorer {
	return newJudge("trajectory_llm", trajectoryInstr, provider)
}

// Rubric scores the output against caller-supplied free-form criteria.
func Rubric(provider core.Provider, criteria string) core.Scorer {
	return newJudge("rubric", rubricInstr(criteria), provider)
}
