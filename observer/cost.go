package observer

// ModelPricing holds per-million-token pricing for a model.
type ModelPricing struct {
	InputPerMillion  float64
	OutputPerMillion float64
}

// DefaultPricing contains sensible defaults for common models.
// Users can override or extend via [observer.pricing] in oasis.toml.
var DefaultPricing = map[string]ModelPricing{
	// Gemini
	"gemini-2.0-flash":      {0.10, 0.40},
	"gemini-2.0-flash-lite": {0.0, 0.0},
	"gemini-2.5-flash":      {0.15, 0.60},
	"gemini-2.5-flash-lite": {0.0, 0.0},
	"gemini-2.5-pro":        {1.25, 10.00},
	"gemini-embedding-001":  {0.0, 0.0},

	// OpenAI
	"gpt-4o":      {2.50, 10.00},
	"gpt-4o-mini": {0.15, 0.60},
	"gpt-4.1":     {2.00, 8.00},
	"gpt-4.1-mini": {0.40, 1.60},
	"gpt-4.1-nano": {0.10, 0.40},
	"o3-mini":     {1.10, 4.40},

	// Anthropic
	"claude-sonnet-4-5": {3.00, 15.00},
	"claude-haiku-3-5":  {0.80, 4.00},
	"claude-opus-4":     {15.00, 75.00},
}

// CostCalculator computes USD cost from token counts.
type CostCalculator struct {
	pricing map[string]ModelPricing
}

// NewCostCalculator creates a calculator with default pricing, optionally merged with overrides.
func NewCostCalculator(overrides map[string]ModelPricing) *CostCalculator {
	merged := make(map[string]ModelPricing, len(DefaultPricing)+len(overrides))
	for k, v := range DefaultPricing {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return &CostCalculator{pricing: merged}
}

// Calculate returns the cost in USD for the given model and token counts.
// Returns 0.0 for unknown models.
func (c *CostCalculator) Calculate(model string, inputTokens, outputTokens int) float64 {
	p, ok := c.pricing[model]
	if !ok {
		return 0.0
	}
	return float64(inputTokens)/1_000_000*p.InputPerMillion +
		float64(outputTokens)/1_000_000*p.OutputPerMillion
}
