package core

import "encoding/json"

// Source is a citation declared by a tool, retriever, or model. Sources
// aggregate onto AgentResult.Sources during a run and let consumers
// render "this answer was based on these documents" UX without inspecting
// individual tool results.
type Source struct {
	// URL is the canonical pointer to the source, when one exists.
	URL string `json:"url,omitempty"`
	// Title is a human-readable label (e.g. document title, page title).
	Title string `json:"title,omitempty"`
	// Quote is the specific passage the answer drew from. Optional.
	Quote string `json:"quote,omitempty"`
	// Origin marks where the source came from. Common values: "rag",
	// "tool:<name>", "model". Free-form; UIs may filter or group on it.
	Origin string `json:"origin,omitempty"`
	// Meta carries opaque metadata (relevance score, chunk ID, etc.).
	Meta json.RawMessage `json:"meta,omitempty"`
}

// Sourced is the opt-in capability for tools, retrievers, and providers
// that produce citations. The agent loop checks for this interface on
// every tool result and every provider response; implementations that
// don't satisfy it contribute nothing to AgentResult.Sources.
type Sourced interface {
	Sources() []Source
}

// Warner is the opt-in capability for providers and provider decorators
// that emit non-fatal warnings. Decorators like WithRetry or WithRateLimit
// implement this to surface "fallback model used" or "throttling applied"
// messages without writing to stderr.
type Warner interface {
	Warnings() []string
}
