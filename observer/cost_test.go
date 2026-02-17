package observer

import (
	"math"
	"testing"
)

func TestCostCalculator(t *testing.T) {
	calc := NewCostCalculator(nil)

	// Known model
	cost := calc.Calculate("gemini-2.5-flash", 1_000_000, 1_000_000)
	if math.Abs(cost-0.75) > 0.001 {
		t.Errorf("gemini-2.5-flash cost = %f, want 0.75", cost)
	}

	// Unknown model returns 0
	cost = calc.Calculate("unknown-model", 1000, 1000)
	if cost != 0.0 {
		t.Errorf("unknown model cost = %f, want 0.0", cost)
	}

	// Override pricing
	calc = NewCostCalculator(map[string]ModelPricing{
		"custom-model": {InputPerMillion: 5.0, OutputPerMillion: 10.0},
	})
	cost = calc.Calculate("custom-model", 500_000, 200_000)
	expected := 500_000.0/1_000_000*5.0 + 200_000.0/1_000_000*10.0 // 2.5 + 2.0 = 4.5
	if math.Abs(cost-expected) > 0.001 {
		t.Errorf("custom-model cost = %f, want %f", cost, expected)
	}

	// Override still has defaults
	cost = calc.Calculate("gemini-2.5-flash", 1_000_000, 1_000_000)
	if math.Abs(cost-0.75) > 0.001 {
		t.Errorf("after override, default cost = %f, want 0.75", cost)
	}
}

func TestCostCalculatorZeroTokens(t *testing.T) {
	calc := NewCostCalculator(nil)
	cost := calc.Calculate("gemini-2.5-flash", 0, 0)
	if cost != 0.0 {
		t.Errorf("zero tokens cost = %f, want 0.0", cost)
	}
}
