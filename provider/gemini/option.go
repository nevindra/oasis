package gemini

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
// Default is "MEDIA_RESOLUTION_MEDIUM".
func WithMediaResolution(r string) Option {
	return func(g *Gemini) { g.mediaResolution = r }
}

// WithThinking enables or disables thinking mode (default false).
// When disabled, thinkingBudget is set to 0 to avoid consuming tokens.
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
// When disabled, toolConfig mode is set to NONE unless tools are explicitly provided via ChatWithTools.
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
