package oasis

// --- Model Catalog vocabulary types ---
//
// These types are used by the ModelCatalog (provider/catalog package) and can
// be referenced by other packages (e.g., observer for cost calculation).

// Protocol determines how to communicate with a provider's model listing API.
type Protocol int

const (
	// ProtocolOpenAICompat is the default — covers ~90% of providers.
	// Any provider with an OpenAI-compatible /v1/models endpoint.
	ProtocolOpenAICompat Protocol = iota
	// ProtocolGemini is for Google's unique Generative Language API.
	ProtocolGemini
)

// Platform represents a known LLM provider platform.
// Built-in platforms are shipped with Oasis; custom platforms can be registered
// at runtime via ModelCatalog.RegisterPlatform.
type Platform struct {
	Name     string   // "OpenAI", "Gemini", "Qwen", "Groq", etc.
	Protocol Protocol // determines how to list models and create providers
	BaseURL  string   // default API endpoint
}

// ModelInfo is the normalized metadata for a single model, aggregated from
// static registry data and live provider APIs.
type ModelInfo struct {
	ID          string // "qwen-turbo", "gemini-2.5-flash"
	Provider    string // identifier used in catalog (e.g., "openai", "qwen")
	DisplayName string // human-friendly name (empty if unavailable)

	InputContext  int // max input tokens (0 = unknown)
	OutputContext int // max output tokens (0 = unknown)

	Capabilities ModelCapabilities

	Status         ModelStatus // live availability
	Deprecated     bool        // is this model deprecated?
	DeprecationMsg string      // e.g., "use gemini-2.5-flash instead"

	Pricing *ModelPricing // nil if provider doesn't expose it
}

// ModelCapabilities describes what a model can do.
type ModelCapabilities struct {
	Chat      bool
	Vision    bool
	ToolUse   bool
	Embedding bool
}

// ModelPricing holds per-token cost information in USD.
// This type is shared with the observer package for cost calculation.
type ModelPricing struct {
	InputPerMillion  float64 // USD per 1M input tokens
	OutputPerMillion float64 // USD per 1M output tokens
}

// ModelStatus reflects the live availability of a model.
type ModelStatus int

const (
	// ModelStatusUnknown means no live data is available (no API key, or offline).
	ModelStatusUnknown ModelStatus = iota
	// ModelStatusAvailable means the model was confirmed by a live API call.
	ModelStatusAvailable
	// ModelStatusUnavailable means the model exists in static data but was
	// not returned by the live API (possibly deprecated or removed).
	ModelStatusUnavailable
)

// ParseModelID splits a "provider/model" string into provider and model parts.
// Returns empty strings if the format is invalid.
func ParseModelID(id string) (provider, model string) {
	for i := range id {
		if id[i] == '/' {
			return id[:i], id[i+1:]
		}
	}
	return "", ""
}
