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

// WithCacheControl marks the last content block of each specified message index
// with cache_control: {"type": "ephemeral"}. The provider caches all content
// up to and including each marked block, reducing cost and latency on subsequent
// requests that share the same prefix.
//
// Supported by Anthropic, Qwen, and other providers that implement the
// cache_control extension. Providers without cache support silently ignore it.
//
// Typical usage: mark the system message and/or long context messages:
//
//	openaicompat.WithCacheControl(0, 1) // cache system prompt (index 0) and context (index 1)
func WithCacheControl(messageIndices ...int) Option {
	return func(r *ChatRequest) {
		set := make(map[int]struct{}, len(messageIndices))
		for _, idx := range messageIndices {
			set[idx] = struct{}{}
		}
		cc := &CacheControl{Type: "ephemeral"}

		for i := range r.Messages {
			if _, ok := set[i]; !ok {
				continue
			}
			msg := &r.Messages[i]
			switch content := msg.Content.(type) {
			case string:
				// Convert plain string to content blocks so we can attach cache_control.
				msg.Content = []ContentBlock{
					{Type: "text", Text: content, CacheControl: cc},
				}
			case []ContentBlock:
				// Mark the last block.
				if len(content) > 0 {
					content[len(content)-1].CacheControl = cc
					msg.Content = content
				}
			}
		}
	}
}
