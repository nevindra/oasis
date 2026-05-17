package ratelimit_test

import (
	"context"
	"fmt"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/ratelimit"
)

// passThroughProvider is a minimal Provider for the example.
type passThroughProvider struct{}

func (passThroughProvider) Name() string { return "example" }
func (passThroughProvider) Chat(ctx context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{Content: "ok"}, nil
}
func (passThroughProvider) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	return oasis.ChatResponse{Content: "ok"}, nil
}

// ExampleWithRateLimit shows the typical 4-line wiring for a rate-limited Provider.
func ExampleWithRateLimit() {
	provider := passThroughProvider{}
	limited := ratelimit.WithRateLimit(provider,
		ratelimit.RPM(60),      // 60 requests per minute
		ratelimit.TPM(100_000), // 100k tokens per minute
	)

	resp, _ := limited.Chat(context.Background(), oasis.ChatRequest{})
	fmt.Println(resp.Content)
	// Output: ok
}
