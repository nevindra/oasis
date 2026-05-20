package agent_test

import (
	"context"

	"github.com/nevindra/oasis/core"
)

// fakeProvider is a minimal core.Provider that always returns a fixed response.
type fakeProvider struct {
	response core.ChatResponse
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	defer close(ch)
	return f.response, nil
}

// newFakeProviderReturning returns a core.Provider whose ChatStream method always
// returns a ChatResponse with the given text as Content.
func newFakeProviderReturning(text string) core.Provider {
	return &fakeProvider{response: core.ChatResponse{Content: text}}
}
