package openaicompat

// Option configures an OpenAI-compatible chat request.
type Option func(*ChatRequest)

// WithTemperature sets the sampling temperature (0.0–2.0).
func WithTemperature(t float64) Option {
	return func(r *ChatRequest) { r.Temperature = &t }
}

// WithTopP sets nucleus sampling top-p (0.0–1.0).
func WithTopP(p float64) Option {
	return func(r *ChatRequest) { r.TopP = &p }
}

// WithMaxTokens sets the maximum number of output tokens.
func WithMaxTokens(n int) Option {
	return func(r *ChatRequest) { r.MaxTokens = n }
}

// WithFrequencyPenalty sets the frequency penalty (-2.0–2.0).
func WithFrequencyPenalty(p float64) Option {
	return func(r *ChatRequest) { r.FrequencyPenalty = &p }
}

// WithPresencePenalty sets the presence penalty (-2.0–2.0).
func WithPresencePenalty(p float64) Option {
	return func(r *ChatRequest) { r.PresencePenalty = &p }
}

// WithStop sets one or more stop sequences.
func WithStop(s ...string) Option {
	return func(r *ChatRequest) { r.Stop = s }
}

// WithSeed sets a deterministic seed for reproducible outputs.
func WithSeed(s int) Option {
	return func(r *ChatRequest) { r.Seed = &s }
}

// WithToolChoice controls how the model selects tools.
// Accepts "none", "auto", "required", or a specific tool object
// like map[string]any{"type": "function", "function": map[string]any{"name": "my_func"}}.
func WithToolChoice(choice any) Option {
	return func(r *ChatRequest) { r.ToolChoice = choice }
}
