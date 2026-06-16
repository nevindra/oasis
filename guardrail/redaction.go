package guardrail

import (
	"context"
	"log/slog"
	"regexp"

	"github.com/nevindra/oasis/core"
)

// Strategy selects how the RedactionGuard reacts to a match.
type Strategy int

const (
	StrategyRedact Strategy = iota // replace each match with a placeholder (default)
	StrategyBlock                  // halt the run via *core.ErrHalt
	StrategyWarn                   // log only, pass through unchanged
)

// Phase selects which sides of the LLM call the guard inspects.
type Phase int

const (
	PhaseBoth   Phase = iota // input and output (default)
	PhaseInput               // request messages only
	PhaseOutput              // response content only
)

type redactRule struct {
	kind string
	re   *regexp.Regexp
}

// presetRules holds the built-in deterministic patterns. RE2 (no backrefs).
var presetRules = map[string][]redactRule{
	"pii": {
		{"email", regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)},
		{"ssn", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
		{"phone", regexp.MustCompile(`\b(?:\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}\b`)},
		{"credit_card", regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`)},
	},
	"secrets": {
		{"aws_access_key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
		{"bearer_token", regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/]+=*`)},
		{"api_key", regexp.MustCompile(`(?i)(?:api[_-]?key|secret|token)\s*[:=]\s*['"]?[A-Za-z0-9\-_]{16,}['"]?`)},
	},
	"urls": {
		{"url", regexp.MustCompile(`https?://[^\s]+`)},
	},
}

// RedactionGuard performs deterministic, zero-cost regex redaction on request
// and/or response text. It implements core.PreProcessor and core.PostProcessor.
// Stateless; safe for concurrent use.
type RedactionGuard struct {
	rules       []redactRule
	strategy    Strategy
	phases      Phase
	placeholder func(kind string) string
	response    string
	logger      *slog.Logger
}

// RedactionOption configures a RedactionGuard.
type RedactionOption func(*RedactionGuard)

// NewRedactionGuard builds a guard. With no presets or rules it is a no-op.
func NewRedactionGuard(opts ...RedactionOption) *RedactionGuard {
	g := &RedactionGuard{
		strategy:    StrategyRedact,
		phases:      PhaseBoth,
		placeholder: func(kind string) string { return "[REDACTED:" + kind + "]" },
		response:    "Request blocked: sensitive content detected.",
	}
	for _, opt := range opts {
		opt(g)
	}
	if g.logger == nil {
		g.logger = nopLogger
	}
	return g
}

// RedactPresets adds built-in rule sets: "pii", "secrets", "urls".
func RedactPresets(names ...string) RedactionOption {
	return func(g *RedactionGuard) {
		for _, n := range names {
			g.rules = append(g.rules, presetRules[n]...)
		}
	}
}

// RedactRule adds a single custom rule with a label.
func RedactRule(kind string, re *regexp.Regexp) RedactionOption {
	return func(g *RedactionGuard) { g.rules = append(g.rules, redactRule{kind, re}) }
}

// RedactStrategy sets the reaction to a match (default StrategyRedact).
func RedactStrategy(s Strategy) RedactionOption {
	return func(g *RedactionGuard) { g.strategy = s }
}

// RedactPhases selects input/output coverage (default PhaseBoth).
func RedactPhases(p Phase) RedactionOption {
	return func(g *RedactionGuard) { g.phases = p }
}

// RedactPlaceholder overrides the redaction placeholder.
func RedactPlaceholder(fn func(kind string) string) RedactionOption {
	return func(g *RedactionGuard) { g.placeholder = fn }
}

// RedactLogger sets the guard's logger.
func RedactLogger(l *slog.Logger) RedactionOption {
	return func(g *RedactionGuard) { g.logger = l }
}

// PreLLM applies input-phase redaction.
func (g *RedactionGuard) PreLLM(_ context.Context, req *core.ChatRequest) error {
	if g.phases == PhaseOutput {
		return nil
	}
	for i := range req.Messages {
		out, matched := g.apply(req.Messages[i].Content)
		if matched && g.strategy == StrategyBlock {
			g.logger.Warn("redaction guard blocked input")
			return &core.ErrHalt{Response: g.response}
		}
		req.Messages[i].Content = out
	}
	return nil
}

// PostLLM applies output-phase redaction to content and thinking.
func (g *RedactionGuard) PostLLM(_ context.Context, resp *core.ChatResponse) error {
	if g.phases == PhaseInput {
		return nil
	}
	out, matched := g.apply(resp.Content)
	if matched && g.strategy == StrategyBlock {
		g.logger.Warn("redaction guard blocked output")
		return &core.ErrHalt{Response: g.response}
	}
	resp.Content = out
	if resp.Thinking != "" {
		t, matched := g.apply(resp.Thinking)
		if matched && g.strategy == StrategyBlock {
			g.logger.Warn("redaction guard blocked output (thinking)")
			return &core.ErrHalt{Response: g.response}
		}
		resp.Thinking = t
	}
	return nil
}

// apply runs every rule over text. Returns the (possibly redacted) text and
// whether any rule matched. For StrategyWarn it logs and returns text
// unchanged; for StrategyRedact it replaces matches.
func (g *RedactionGuard) apply(text string) (string, bool) {
	if text == "" {
		return text, false
	}
	matched := false
	for _, r := range g.rules {
		if !r.re.MatchString(text) {
			continue
		}
		matched = true
		switch g.strategy {
		case StrategyWarn:
			g.logger.Warn("redaction guard matched", "kind", r.kind)
		case StrategyRedact:
			text = r.re.ReplaceAllString(text, g.placeholder(r.kind))
		case StrategyBlock:
			return text, true // caller halts
		}
	}
	return text, matched
}

// PostChunk redacts a single streamed delta (v1: per-chunk, no cross-chunk
// buffering). Honors the configured phases (output side) and strategy.
func (g *RedactionGuard) PostChunk(_ context.Context, ev *core.StreamEvent) (*core.StreamEvent, error) {
	if g.phases == PhaseInput || ev.Content == "" {
		return ev, nil
	}
	out, matched := g.apply(ev.Content)
	if matched && g.strategy == StrategyBlock {
		g.logger.Warn("redaction guard blocked stream chunk")
		return nil, &core.ErrHalt{Response: g.response}
	}
	ev.Content = out
	return ev, nil
}

// compile-time interface checks
var (
	_ core.PreProcessor    = (*RedactionGuard)(nil)
	_ core.PostProcessor   = (*RedactionGuard)(nil)
	_ core.StreamProcessor = (*RedactionGuard)(nil)
)
