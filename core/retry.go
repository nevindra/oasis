package core

import (
	"context"
	"errors"
	"net"
	"time"
)

// ToolPolicy describes the per-tool timeout and retry policy applied by the
// agent's dispatch wrapper. Zero value = no timeout, no retries (current
// behavior). Streaming tools (those implementing StreamingAnyTool) bypass
// this policy entirely — retrying a partially-streamed call would duplicate
// events at the consumer.
type ToolPolicy struct {
	// Timeout is the per-attempt context deadline. Zero means no timeout
	// (the parent context still applies).
	Timeout time.Duration
	// Retries is the number of additional attempts after the first. Zero
	// means a single attempt, identical to current behavior.
	Retries int
	// RetryDelay is the base backoff between attempts. The actual delay
	// before attempt N+1 is RetryDelay << N, capped by MaxRetryDelay.
	RetryDelay time.Duration
	// MaxRetryDelay caps the exponential backoff. Zero means no cap.
	MaxRetryDelay time.Duration
	// RetryOn decides whether a given error is retryable. nil → DefaultRetryOn.
	RetryOn func(error) bool
}

// Retryable is the opt-in convention tool authors use to mark an error as
// retryable. DefaultRetryOn honors this mark via errors.As, and any
// user-supplied RetryOn predicate may do the same.
type Retryable interface {
	Retryable() bool
}

// retryableErr wraps an underlying error and reports Retryable() == true.
type retryableErr struct{ err error }

func (r *retryableErr) Error() string   { return r.err.Error() }
func (r *retryableErr) Unwrap() error   { return r.err }
func (r *retryableErr) Retryable() bool { return true }

// RetryableError marks err as retryable. It is the recommended way for tool
// implementations to signal that a transient failure (HTTP 429, 5xx, etc.)
// is worth a retry attempt. Returns nil when err is nil.
//
//	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
//	    return zero, core.RetryableError(fmt.Errorf("upstream: HTTP %d", resp.StatusCode))
//	}
//	return zero, fmt.Errorf("upstream: HTTP %d", resp.StatusCode) // not retryable
func RetryableError(err error) error {
	if err == nil {
		return nil
	}
	return &retryableErr{err: err}
}

// DefaultRetryOn is the predicate used when ToolPolicy.RetryOn is nil. It
// returns true iff:
//
//  1. errors.Is(err, context.DeadlineExceeded) — our own timeout fired.
//  2. err is a net.Error with Timeout() == true — TCP-layer timeout.
//  3. err matches the Retryable interface via errors.As — opt-in mark.
//
// DefaultRetryOn is exported so user-supplied predicates can compose:
//
//	RetryOn: func(err error) bool {
//	    return core.DefaultRetryOn(err) || errors.Is(err, myExtraSentinel)
//	}
func DefaultRetryOn(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	var r Retryable
	if errors.As(err, &r) && r.Retryable() {
		return true
	}
	return false
}

// BackoffDelay computes the backoff for retry attempt N (0-indexed) given
// the base RetryDelay and optional MaxRetryDelay cap. delay = base << attempt,
// then capped at max if max > 0. Exported for test parity and so user code
// can reproduce the framework's backoff schedule.
func BackoffDelay(base, max time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	shift := attempt
	if shift > 30 {
		shift = 30
	}
	d := base << shift
	if max > 0 && d > max {
		return max
	}
	return d
}
