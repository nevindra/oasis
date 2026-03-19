package observer

import oasis "github.com/nevindra/oasis"

// DefaultPricing contains sensible defaults for common models.
// Users can override or extend via [observer.pricing] in oasis.toml.
var DefaultPricing = map[string]oasis.ModelPricing{
	// Gemini
	"gemini-2.0-flash":      {InputPerMillion: 0.10, OutputPerMillion: 0.40},
	"gemini-2.0-flash-lite": {InputPerMillion: 0.0, OutputPerMillion: 0.0},
	"gemini-2.5-flash":      {InputPerMillion: 0.15, OutputPerMillion: 0.60},
	"gemini-2.5-flash-lite": {InputPerMillion: 0.0, OutputPerMillion: 0.0},
	"gemini-2.5-pro":        {InputPerMillion: 1.25, OutputPerMillion: 10.00},
	"gemini-embedding-001":  {InputPerMillion: 0.0, OutputPerMillion: 0.0},

	// OpenAI
	"gpt-4o":       {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	"gpt-4o-mini":  {InputPerMillion: 0.15, OutputPerMillion: 0.60},
	"gpt-4.1":      {InputPerMillion: 2.00, OutputPerMillion: 8.00},
	"gpt-4.1-mini": {InputPerMillion: 0.40, OutputPerMillion: 1.60},
	"gpt-4.1-nano": {InputPerMillion: 0.10, OutputPerMillion: 0.40},
	"o3-mini":      {InputPerMillion: 1.10, OutputPerMillion: 4.40},

	// Anthropic
	"claude-sonnet-4-5": {InputPerMillion: 3.00, OutputPerMillion: 15.00},
	"claude-haiku-3-5":  {InputPerMillion: 0.80, OutputPerMillion: 4.00},
	"claude-opus-4":     {InputPerMillion: 15.00, OutputPerMillion: 75.00},
}

// CostCalculator computes USD cost from token counts.
type CostCalculator struct {
	pricing map[string]oasis.ModelPricing
}

// NewCostCalculator creates a calculator with default pricing, optionally merged with overrides.
func NewCostCalculator(overrides map[string]oasis.ModelPricing) *CostCalculator {
	merged := make(map[string]oasis.ModelPricing, len(DefaultPricing)+len(overrides))
	for k, v := range DefaultPricing {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return &CostCalculator{pricing: merged}
}

// Calculate returns the cost in USD for the given model and token counts.
// When cachedTokens > 0 and the model has cache pricing, cached tokens
// are billed at the lower cache rate. Otherwise all input tokens use standard rate.
// Returns 0.0 for unknown models.
func (c *CostCalculator) Calculate(model string, inputTokens, outputTokens, cachedTokens int) float64 {
	p, ok := c.pricing[model]
	if !ok {
		return 0.0
	}

	var inputCost float64
	if cachedTokens > 0 && p.CacheReadPerMillion > 0 {
		nonCached := inputTokens - cachedTokens
		if nonCached < 0 {
			nonCached = 0
		}
		inputCost = float64(nonCached)/1_000_000*p.InputPerMillion +
			float64(cachedTokens)/1_000_000*p.CacheReadPerMillion
	} else {
		inputCost = float64(inputTokens) / 1_000_000 * p.InputPerMillion
	}

	return inputCost + float64(outputTokens)/1_000_000*p.OutputPerMillion
}

// NewCostCalculatorFromModels creates a calculator using pricing data from ModelInfo entries.
// Models are indexed by their bare ID (e.g., "gpt-4o", not "openai/gpt-4o").
// Optional overrides take precedence over catalog pricing.
func NewCostCalculatorFromModels(models []oasis.ModelInfo, overrides map[string]oasis.ModelPricing) *CostCalculator {
	pricing := make(map[string]oasis.ModelPricing, len(models))
	for _, m := range models {
		if m.Pricing != nil {
			pricing[m.ID] = *m.Pricing
		}
	}
	// DefaultPricing as fallback for models not in catalog.
	for k, v := range DefaultPricing {
		if _, exists := pricing[k]; !exists {
			pricing[k] = v
		}
	}
	for k, v := range overrides {
		pricing[k] = v
	}
	return &CostCalculator{pricing: pricing}
}
