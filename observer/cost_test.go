package observer

import (
	"math"
	"testing"

	oasis "github.com/nevindra/oasis"
)

// --- Updated existing tests (4th arg: cachedTokens) ---

func TestCostCalculator(t *testing.T) {
	calc := NewCostCalculator(nil)

	cost := calc.Calculate("gemini-2.5-flash", 1_000_000, 1_000_000, 0)
	if math.Abs(cost-0.75) > 0.001 {
		t.Errorf("gemini-2.5-flash cost = %f, want 0.75", cost)
	}

	cost = calc.Calculate("unknown-model", 1000, 1000, 0)
	if cost != 0.0 {
		t.Errorf("unknown model cost = %f, want 0.0", cost)
	}

	calc = NewCostCalculator(map[string]oasis.ModelPricing{
		"custom-model": {InputPerMillion: 5.0, OutputPerMillion: 10.0},
	})
	cost = calc.Calculate("custom-model", 500_000, 200_000, 0)
	expected := 500_000.0/1_000_000*5.0 + 200_000.0/1_000_000*10.0
	if math.Abs(cost-expected) > 0.001 {
		t.Errorf("custom-model cost = %f, want %f", cost, expected)
	}

	cost = calc.Calculate("gemini-2.5-flash", 1_000_000, 1_000_000, 0)
	if math.Abs(cost-0.75) > 0.001 {
		t.Errorf("after override, default cost = %f, want 0.75", cost)
	}
}

func TestCostCalculatorZeroTokens(t *testing.T) {
	calc := NewCostCalculator(nil)
	cost := calc.Calculate("gemini-2.5-flash", 0, 0, 0)
	if cost != 0.0 {
		t.Errorf("zero tokens cost = %f, want 0.0", cost)
	}
}

// --- New cache-aware tests ---

func TestCalculateBasicCost(t *testing.T) {
	pricing := map[string]oasis.ModelPricing{
		"gpt-4o": {InputPerMillion: 2.50, OutputPerMillion: 10.00},
	}
	calc := NewCostCalculator(pricing)
	cost := calc.Calculate("gpt-4o", 1000, 500, 0)
	if cost < 0.0074 || cost > 0.0076 {
		t.Errorf("cost = %f, want ~0.0075", cost)
	}
}

func TestCalculateWithCachedTokens(t *testing.T) {
	pricing := map[string]oasis.ModelPricing{
		"gpt-4o": {InputPerMillion: 2.50, OutputPerMillion: 10.00, CacheReadPerMillion: 1.25},
	}
	calc := NewCostCalculator(pricing)
	cost := calc.Calculate("gpt-4o", 1000, 500, 300)
	// Non-cached: (700/1M)*2.50 = 0.00175, Cached: (300/1M)*1.25 = 0.000375, Output: (500/1M)*10 = 0.005
	if cost < 0.0071 || cost > 0.0072 {
		t.Errorf("cost = %f, want ~0.007125", cost)
	}
}

func TestCalculateNoCachePrice(t *testing.T) {
	pricing := map[string]oasis.ModelPricing{
		"model-x": {InputPerMillion: 1.00, OutputPerMillion: 2.00},
	}
	calc := NewCostCalculator(pricing)
	cost := calc.Calculate("model-x", 1000, 500, 300)
	expected := 0.002
	if cost < expected-0.0001 || cost > expected+0.0001 {
		t.Errorf("cost = %f, want ~%f", cost, expected)
	}
}

func TestCalculateUnknownModel(t *testing.T) {
	calc := NewCostCalculator(nil)
	cost := calc.Calculate("unknown", 1000, 500, 0)
	if cost != 0.0 {
		t.Errorf("unknown model cost = %f, want 0.0", cost)
	}
}

func TestNewCostCalculatorFromModels(t *testing.T) {
	models := []oasis.ModelInfo{
		{ID: "gpt-4o", Provider: "openai", Pricing: &oasis.ModelPricing{InputPerMillion: 2.50, OutputPerMillion: 10.00, CacheReadPerMillion: 1.25}},
		{ID: "gemini-2.5-flash", Provider: "gemini", Pricing: &oasis.ModelPricing{InputPerMillion: 0.15, OutputPerMillion: 0.60}},
		{ID: "no-pricing-model", Pricing: nil},
	}
	calc := NewCostCalculatorFromModels(models, nil)

	cost := calc.Calculate("gpt-4o", 1_000_000, 0, 0)
	if cost != 2.50 {
		t.Errorf("gpt-4o cost = %f, want 2.50", cost)
	}
	cost = calc.Calculate("gemini-2.5-flash", 1_000_000, 0, 0)
	if cost != 0.15 {
		t.Errorf("gemini cost = %f, want 0.15", cost)
	}
	cost = calc.Calculate("no-pricing-model", 1_000_000, 0, 0)
	if cost != 0.0 {
		t.Errorf("no-pricing cost = %f, want 0.0", cost)
	}
}

func TestCostCalculatorOverrides(t *testing.T) {
	models := []oasis.ModelInfo{
		{ID: "gpt-4o", Pricing: &oasis.ModelPricing{InputPerMillion: 2.50, OutputPerMillion: 10.00}},
	}
	overrides := map[string]oasis.ModelPricing{
		"gpt-4o": {InputPerMillion: 1.00, OutputPerMillion: 5.00},
	}
	calc := NewCostCalculatorFromModels(models, overrides)
	cost := calc.Calculate("gpt-4o", 1_000_000, 0, 0)
	if cost != 1.00 {
		t.Errorf("expected override price 1.00, got %f", cost)
	}
}
