package oasis

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"
)

// retryProvider wraps a Provider and automatically retries transient HTTP errors
// (status 429 Too Many Requests and 503 Service Unavailable) with exponential backoff.
type retryProvider struct {
	inner       Provider
	maxAttempts int
	baseDelay   time.Duration
	timeout     time.Duration    // overall timeout across all attempts; 0 = no limit
	logger      *slog.Logger     // nil = nopLogger
}

// RetryOption configures a retryProvider.
type RetryOption func(*retryProvider)

// RetryMaxAttempts sets the maximum number of attempts (default: 3).
func RetryMaxAttempts(n int) RetryOption {
	return func(r *retryProvider) { r.maxAttempts = n }
}

// RetryBaseDelay sets the initial backoff delay before the second attempt (default: 1s).
// Each subsequent delay doubles: baseDelay, 2×baseDelay, 4×baseDelay, …
func RetryBaseDelay(d time.Duration) RetryOption {
	return func(r *retryProvider) { r.baseDelay = d }
}

// RetryTimeout sets the overall timeout for the entire retry sequence. If the
// total time across all attempts exceeds this duration, the retry loop gives up
// and returns the last error. The zero value (default) disables the timeout.
func RetryTimeout(d time.Duration) RetryOption {
	return func(r *retryProvider) { r.timeout = d }
}

// RetryLogger sets the structured logger for retry events. When set, retries
// log at WARN level and final failures after exhausting attempts log at ERROR.
// If not set, a no-op logger is used (no output).
func RetryLogger(l *slog.Logger) RetryOption {
	return func(r *retryProvider) { r.logger = l }
}

// WithRetry wraps p with automatic retry on transient HTTP errors (429, 503).
// Retries use exponential backoff with jitter. When the error includes a
// Retry-After duration (parsed from the HTTP header), the retry delay is at
// least that long. Compose with any Provider:
//
//	chatLLM = oasis.WithRetry(gemini.New(apiKey, model))
//	chatLLM = oasis.WithRetry(gemini.New(apiKey, model), oasis.RetryMaxAttempts(5))
//	chatLLM = oasis.WithRetry(gemini.New(apiKey, model), oasis.RetryTimeout(30*time.Second))
func WithRetry(p Provider, opts ...RetryOption) Provider {
	r := &retryProvider{
		inner:       p,
		maxAttempts: 3,
		baseDelay:   time.Second,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.logger == nil {
		r.logger = nopLogger
	}
	return r
}

// Name delegates to the inner provider.
func (r *retryProvider) Name() string { return r.inner.Name() }

// Chat implements Provider with retry.
func (r *retryProvider) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	return retryCall(ctx, r.maxAttempts, r.baseDelay, r.inner.Name(), r.logger, func() (ChatResponse, error) {
		return r.inner.Chat(ctx, req)
	})
}

// ChatStream implements Provider with retry. Retries are only performed if no
// tokens have been written to ch yet — once streaming has started, errors pass
// through immediately to avoid sending duplicate content.
// ch is always closed before returning.
func (r *retryProvider) ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error) {
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	var lastErr error
	for i := 0; i < r.maxAttempts; i++ {
		mid := make(chan StreamEvent, 64)
		var (
			resp      ChatResponse
			streamErr error
		)
		done := make(chan struct{})
		go func() {
			defer close(done)
			resp, streamErr = r.inner.ChatStream(ctx, req, mid)
		}()

		var tokensSent bool
		for ev := range mid {
			tokensSent = true
			ch <- ev
		}
		<-done

		if streamErr == nil || !isTransient(streamErr) || tokensSent {
			close(ch)
			return resp, streamErr
		}

		lastErr = streamErr
		r.logger.Warn("retrying transient error",
			"provider", r.inner.Name(),
			"status", statusOf(streamErr),
			"attempt", i+1,
			"max_attempts", r.maxAttempts)
		if i < r.maxAttempts-1 {
			delay := retryDelay(r.baseDelay, i, streamErr)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				close(ch)
				return ChatResponse{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	r.logger.Error("all retry attempts exhausted (stream)",
		"provider", r.inner.Name(),
		"attempts", r.maxAttempts,
		"error", lastErr)
	close(ch)
	return ChatResponse{}, lastErr
}

// withTimeout returns a child context with a deadline if r.timeout is set.
// If timeout is zero or ctx already has an earlier deadline, returns ctx unchanged.
// The caller must call the returned CancelFunc when done.
func (r *retryProvider) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.timeout <= 0 {
		return ctx, func() {}
	}
	deadline := time.Now().Add(r.timeout)
	if existing, ok := ctx.Deadline(); ok && existing.Before(deadline) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, deadline)
}

// isTransient reports whether err is a retryable HTTP error (429 or 503).
func isTransient(err error) bool {
	var e *ErrHTTP
	return errors.As(err, &e) && (e.Status == 429 || e.Status == 503)
}

// statusOf extracts the HTTP status code from an ErrHTTP, or 0.
func statusOf(err error) int {
	var e *ErrHTTP
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// retryAfterOf extracts the Retry-After duration from an ErrHTTP, or 0.
func retryAfterOf(err error) time.Duration {
	var e *ErrHTTP
	if errors.As(err, &e) {
		return e.RetryAfter
	}
	return 0
}

// retryDelay computes the delay before retry attempt i, using exponential
// backoff as a floor and the server's Retry-After value (if present) as a
// minimum. The effective delay is max(backoff, retryAfter).
func retryDelay(base time.Duration, i int, err error) time.Duration {
	backoff := retryBackoff(base, i)
	if ra := retryAfterOf(err); ra > backoff {
		return ra
	}
	return backoff
}

// retryCall calls fn up to maxAttempts times, sleeping between transient failures.
func retryCall[T any](ctx context.Context, maxAttempts int, base time.Duration, name string, logger *slog.Logger, fn func() (T, error)) (T, error) {
	var zero T
	var last error
	for i := 0; i < maxAttempts; i++ {
		result, err := fn()
		if err == nil || !isTransient(err) {
			return result, err
		}
		last = err
		logger.Warn("retrying transient error",
			"provider", name,
			"status", statusOf(err),
			"attempt", i+1,
			"max_attempts", maxAttempts)
		if i < maxAttempts-1 {
			delay := retryDelay(base, i, err)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, ctx.Err()
			case <-timer.C:
			}
		}
	}
	logger.Error("all retry attempts exhausted",
		"provider", name,
		"attempts", maxAttempts,
		"error", last)
	return zero, last
}

// retryBackoff returns the delay for retry i (0-indexed).
// Exponential: base * 2^i, plus up to 50% random jitter.
func retryBackoff(base time.Duration, i int) time.Duration {
	exp := base * (1 << i)
	jitter := time.Duration(rand.Int63n(int64(exp)/2 + 1))
	return exp + jitter
}

// retryEmbeddingProvider wraps an EmbeddingProvider and automatically retries
// transient HTTP errors (429, 503) with exponential backoff.
type retryEmbeddingProvider struct {
	inner       EmbeddingProvider
	maxAttempts int
	baseDelay   time.Duration
	timeout     time.Duration
	logger      *slog.Logger
}

// WithEmbeddingRetry wraps p with automatic retry on transient HTTP errors (429, 503).
// Accepts the same RetryOption functions as WithRetry. Compose with any EmbeddingProvider:
//
//	emb = oasis.WithEmbeddingRetry(gemini.NewEmbedding(apiKey, model))
//	emb = oasis.WithEmbeddingRetry(gemini.NewEmbedding(apiKey, model), oasis.RetryMaxAttempts(5))
func WithEmbeddingRetry(p EmbeddingProvider, opts ...RetryOption) EmbeddingProvider {
	// Apply options to a temporary retryProvider to extract config values.
	cfg := &retryProvider{maxAttempts: 3, baseDelay: time.Second}
	for _, opt := range opts {
		opt(cfg)
	}
	logger := cfg.logger
	if logger == nil {
		logger = nopLogger
	}
	return &retryEmbeddingProvider{
		inner:       p,
		maxAttempts: cfg.maxAttempts,
		baseDelay:   cfg.baseDelay,
		timeout:     cfg.timeout,
		logger:      logger,
	}
}

func (r *retryEmbeddingProvider) Name() string       { return r.inner.Name() }
func (r *retryEmbeddingProvider) Dimensions() int     { return r.inner.Dimensions() }

func (r *retryEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if r.timeout > 0 {
		deadline := time.Now().Add(r.timeout)
		if existing, ok := ctx.Deadline(); !ok || deadline.Before(existing) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
	}
	return retryCall(ctx, r.maxAttempts, r.baseDelay, r.inner.Name(), r.logger, func() ([][]float32, error) {
		return r.inner.Embed(ctx, texts)
	})
}

// compile-time checks
var (
	_ Provider          = (*retryProvider)(nil)
	_ EmbeddingProvider = (*retryEmbeddingProvider)(nil)
)
