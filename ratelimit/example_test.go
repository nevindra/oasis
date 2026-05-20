package ratelimit_test

import (
	"context"
	"fmt"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/ratelimit"
)

// passThroughProvider is a minimal Provider for the example.
type passThroughProvider struct{}

func (passThroughProvider) Name() string { return "example" }
func (passThroughProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	return core.ChatResponse{Content: "ok"}, nil
}

// ExampleWithRateLimit shows the typical 4-line wiring for a rate-limited Provider.
func ExampleWithRateLimit() {
	provider := passThroughProvider{}
	limited := ratelimit.WithRateLimit(provider,
		ratelimit.RPM(60),      // 60 requests per minute
		ratelimit.TPM(100_000), // 100k tokens per minute
	)

	resp, _ := core.Chat(context.Background(), limited, core.ChatRequest{})
	fmt.Println(resp.Content)
	// Output: ok
}
