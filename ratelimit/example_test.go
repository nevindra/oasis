package ratelimit_test

import (
	"context"
	"fmt"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/provider"
	"github.com/nevindra/oasis/ratelimit"
)

// passThroughProvider is a minimal Provider for the example.
type passThroughProvider struct{}

func (passThroughProvider) Name() string { return "example" }
func (passThroughProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	if ch != nil {
		defer close(ch)
	}
	return core.ChatResponse{Content: "ok"}, nil
}

// ExampleRateLimitMiddleware shows the typical wiring for a rate-limited Provider.
func ExampleRateLimitMiddleware() {
	base := passThroughProvider{}
	limited := provider.Chain(ratelimit.RateLimitMiddleware(
		ratelimit.RPM(60),      // 60 requests per minute
		ratelimit.TPM(100_000), // 100k tokens per minute
	))(base)

	resp, _ := core.Chat(context.Background(), limited, core.ChatRequest{})
	fmt.Println(resp.Content)
	// Output: ok
}
