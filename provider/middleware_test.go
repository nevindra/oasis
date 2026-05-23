package provider_test

import (
	"context"
	"slices"
	"testing"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/provider"
)

// stubProvider is a minimal core.Provider that records calls and returns zero values.
type stubProvider struct{}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) ChatStream(_ context.Context, _ core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	close(ch)
	return core.ChatResponse{}, nil
}

// recordingProvider wraps an inner provider and calls fn before/after ChatStream.
// fn(false) is called before the inner call, fn(true) after.
type recordingProvider struct {
	inner  core.Provider
	before func()
	after  func()
}

func (r *recordingProvider) Name() string { return r.inner.Name() }
func (r *recordingProvider) ChatStream(ctx context.Context, req core.ChatRequest, ch chan<- core.StreamEvent) (core.ChatResponse, error) {
	r.before()
	resp, err := r.inner.ChatStream(ctx, req, ch)
	r.after()
	return resp, err
}

// makeOrderMiddleware returns a Middleware that appends label+"-pre" before and
// label+"-post" after delegating to the next provider.
func makeOrderMiddleware(label string, order *[]string) provider.Middleware {
	return func(p core.Provider) core.Provider {
		return &recordingProvider{
			inner:  p,
			before: func() { *order = append(*order, label+"-pre") },
			after:  func() { *order = append(*order, label+"-post") },
		}
	}
}

func TestChain_AppliesInOrder(t *testing.T) {
	var got []string

	a := makeOrderMiddleware("a", &got)
	b := makeOrderMiddleware("b", &got)

	base := &stubProvider{}
	wrapped := provider.Chain(a, b)(base)

	ch := make(chan core.StreamEvent, 8)
	_, err := wrapped.ChatStream(context.Background(), core.ChatRequest{}, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Drain any remaining events.
	for range ch {
	}

	want := []string{"a-pre", "b-pre", "b-post", "a-post"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestChain_NilMiddlewareSkipped(t *testing.T) {
	var got []string
	a := makeOrderMiddleware("a", &got)

	base := &stubProvider{}
	// nil in the middle should be skipped gracefully.
	wrapped := provider.Chain(a, nil)(base)

	ch := make(chan core.StreamEvent, 8)
	_, err := wrapped.ChatStream(context.Background(), core.ChatRequest{}, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range ch {
	}

	want := []string{"a-pre", "a-post"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestChain_Empty(t *testing.T) {
	base := &stubProvider{}
	// Chain with no middlewares must return base unchanged.
	wrapped := provider.Chain()(base)
	if wrapped != base {
		t.Errorf("Chain() changed provider; want identity")
	}
}
