package core

import "context"

// Chat is a non-streaming convenience wrapper around Provider.ChatStream.
// It discards stream events and returns the final assembled response.
// For UI-facing streaming, call ChatStream directly.
func Chat(ctx context.Context, p Provider, req ChatRequest) (ChatResponse, error) {
	ch := make(chan StreamEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range ch { // discard
		}
	}()
	resp, err := p.ChatStream(ctx, req, ch)
	<-done
	return resp, err
}
