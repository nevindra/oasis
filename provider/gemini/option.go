package gemini

import "log/slog"

// Option configures a Gemini provider.
type Option func(*Gemini)

// WithTemperature sets the sampling temperature (default 0.1).
func WithTemperature(t float64) Option {
	return func(g *Gemini) { g.temperature = t }
}

// WithTopP sets nucleus sampling top-p (default 0.9).
func WithTopP(p float64) Option {
	return func(g *Gemini) { g.topP = p }
}

// WithMediaResolution sets the media resolution for multimodal inputs.
// Valid values: "MEDIA_RESOLUTION_LOW", "MEDIA_RESOLUTION_MEDIUM", "MEDIA_RESOLUTION_HIGH".
// Only sent when explicitly set; omitted by default.
func WithMediaResolution(r string) Option {
	return func(g *Gemini) { g.mediaResolution = r }
}

// WithResponseModalities sets the response modalities for the model.
// Required for image generation models — use WithResponseModalities("TEXT", "IMAGE").
// Only sent when explicitly set; omitted by default (text-only).
func WithResponseModalities(modalities ...string) Option {
	return func(g *Gemini) { g.responseModalities = modalities }
}

// WithThinking enables or disables thinking mode (default false).
// When enabled, sends thinkingConfig with budget -1 (dynamic).
// When disabled (default), thinkingConfig is omitted entirely.
func WithThinking(enabled bool) Option {
	return func(g *Gemini) { g.thinkingEnabled = enabled }
}

// WithStructuredOutput enables or disables structured JSON output (default true).
// When enabled, responses matching a provided schema use application/json MIME type.
func WithStructuredOutput(enabled bool) Option {
	return func(g *Gemini) { g.structuredOutput = enabled }
}

// WithCodeExecution enables or disables the code execution tool (default false).
func WithCodeExecution(enabled bool) Option {
	return func(g *Gemini) { g.codeExecution = enabled }
}

// WithFunctionCalling enables or disables implicit function calling (default false).
// When disabled, toolConfig mode is set to NONE unless tools are explicitly provided via ChatRequest.Tools.
func WithFunctionCalling(enabled bool) Option {
	return func(g *Gemini) { g.functionCalling = enabled }
}

// WithGoogleSearch enables or disables grounding with Google Search (default false).
func WithGoogleSearch(enabled bool) Option {
	return func(g *Gemini) { g.googleSearch = enabled }
}

// WithURLContext enables or disables URL context (default false).
func WithURLContext(enabled bool) Option {
	return func(g *Gemini) { g.urlContext = enabled }
}

// WithLogger sets a structured logger for the provider.
// When set, the provider emits warnings for unsupported GenerationParams fields.
// If not set, no warnings are emitted.
func WithLogger(l *slog.Logger) Option {
	return func(g *Gemini) { g.logger = l }
}
