// Package oasis is the public umbrella for the Oasis agent framework.
//
// This file curates the re-export surface: users import a single package
// (github.com/nevindra/oasis) and get the common API via aliases.
//
// Niche or power-user APIs are deliberately NOT re-exported — those callers
// import the relevant subpackage directly (e.g. "github.com/nevindra/oasis/compaction").
//
// Adding a re-export here is a deliberate decision: it signals "this is part
// of the curated public surface." Do not auto-mirror every new export in a
// subpackage.
package oasis

import (
	"github.com/nevindra/oasis/compaction"
	"github.com/nevindra/oasis/guardrail"
	"github.com/nevindra/oasis/ratelimit"
)

// --- Compaction ---

// NewStructuredCompactor creates the default Compactor implementation that
// turns long conversation histories into structured 9-section summaries via
// a single LLM call. See github.com/nevindra/oasis/compaction for the full API.
var NewStructuredCompactor = compaction.NewStructuredCompactor

// --- Guardrail ---

// NewInjectionGuard creates a PreProcessor that detects and blocks prompt
// injection attempts. See github.com/nevindra/oasis/guardrail for options.
var NewInjectionGuard = guardrail.NewInjectionGuard

// NewContentGuard creates a guard that enforces character length limits on
// input and output content. See github.com/nevindra/oasis/guardrail for options.
var NewContentGuard = guardrail.NewContentGuard

// NewKeywordGuard creates a guard that blocks messages containing specified
// keywords or regex patterns. See github.com/nevindra/oasis/guardrail for options.
var NewKeywordGuard = guardrail.NewKeywordGuard

// --- Rate limiting ---

// WithRateLimit wraps a Provider with proactive rate limiting.
// Compose with RPM and TPM options:
//
//	limited := oasis.WithRateLimit(provider, oasis.RPM(60), oasis.TPM(100_000))
//
// See github.com/nevindra/oasis/ratelimit for the full API.
var WithRateLimit = ratelimit.WithRateLimit

// RPM sets the maximum requests per minute for a rate-limited Provider.
var RPM = ratelimit.RPM

// TPM sets the maximum tokens per minute for a rate-limited Provider.
var TPM = ratelimit.TPM
