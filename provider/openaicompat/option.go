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

// WithModalities sets the requested output modalities (e.g. ["text","image"]).
// When "image" is requested, string message content is promoted to a single
// text content block, because image-capable endpoints require `content` to be
// a list. Text-only requests are left untouched.
func WithModalities(m []string) Option {
	return func(r *ChatRequest) {
		r.Modalities = m
		wantImage := false
		for _, mod := range m {
			if mod == "image" {
				wantImage = true
				break
			}
		}
		if !wantImage {
			return
		}
		for i := range r.Messages {
			if s, ok := r.Messages[i].Content.(string); ok && s != "" {
				r.Messages[i].Content = []ContentBlock{{Type: "text", Text: s}}
			}
		}
	}
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
//
// Composable with core.ChatMessage.CacheCheckpoint: if a message already has
// cache_control set (via CacheCheckpoint), applying WithCacheControl for the
// same index is idempotent — the same ephemeral marker is written once.
func WithCacheControl(messageIndices ...int) Option {
	return func(r *ChatRequest) {
		set := make(map[int]struct{}, len(messageIndices))
		for _, idx := range messageIndices {
			set[idx] = struct{}{}
		}
		for i := range r.Messages {
			if _, ok := set[i]; !ok {
				continue
			}
			markCacheControl(&r.Messages[i])
		}
	}
}
