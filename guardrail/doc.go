// Package guardrail provides input and output safety processors for the
// oasis agent loop. Each guard implements oasis.PreProcessor (input-side),
// oasis.PostProcessor (output-side), or both. Guards return *oasis.ErrHalt
// when policy is violated, causing the agent loop to stop with the
// configured response.
//
// Available guards:
//
//   - InjectionGuard:    multi-layer prompt-injection detection (phrases,
//                        role overrides, delimiter abuse, base64 decode,
//                        custom regex).
//   - ContentGuard:      input/output length limits (rune count).
//   - KeywordGuard:      keyword and regex blocklist for user messages.
//   - MaxToolCallsGuard: silently trims excess tool calls per LLM turn
//                        (graceful degradation, no halt).
//
// Basic usage:
//
//	injection := guardrail.NewInjectionGuard()
//	length    := guardrail.NewContentGuard(
//	    guardrail.MaxInputLength(5000),
//	    guardrail.MaxOutputLength(10_000),
//	)
//
//	agent := oasis.NewLLMAgent("agent", "...", provider,
//	    oasis.WithPreProcessors(injection, length),
//	)
//
// All guards are safe for concurrent use. See each guard's documentation
// for option details.
package guardrail
