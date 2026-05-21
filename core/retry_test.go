package core

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestRetryableError_NilPassThrough(t *testing.T) {
	if got := RetryableError(nil); got != nil {
		t.Fatalf("RetryableError(nil) = %v, want nil", got)
	}
}

func TestRetryableError_WrapsAndUnwraps(t *testing.T) {
	wrapped := RetryableError(io.EOF)
	if !errors.Is(wrapped, io.EOF) {
		t.Errorf("errors.Is(wrapped, io.EOF) = false, want true")
	}
	var r Retryable
	if !errors.As(wrapped, &r) {
		t.Fatalf("errors.As(wrapped, &Retryable) = false, want true")
	}
	if !r.Retryable() {
		t.Errorf("r.Retryable() = false, want true")
	}
}

func TestDefaultRetryOn_DeadlineExceeded(t *testing.T) {
	if !DefaultRetryOn(context.DeadlineExceeded) {
		t.Errorf("DefaultRetryOn(DeadlineExceeded) = false, want true")
	}
}

func TestDefaultRetryOn_NetTimeout(t *testing.T) {
	e := &net.DNSError{IsTimeout: true}
	if !DefaultRetryOn(e) {
		t.Errorf("DefaultRetryOn(net.DNSError{IsTimeout:true}) = false, want true")
	}
}

func TestDefaultRetryOn_PlainErrorRejected(t *testing.T) {
	if DefaultRetryOn(errors.New("plain")) {
		t.Errorf("DefaultRetryOn(plain) = true, want false")
	}
}

func TestDefaultRetryOn_RetryableErrorAccepted(t *testing.T) {
	if !DefaultRetryOn(RetryableError(errors.New("upstream 503"))) {
		t.Errorf("DefaultRetryOn(RetryableError(...)) = false, want true")
	}
}

func TestDefaultRetryOn_Nil(t *testing.T) {
	if DefaultRetryOn(nil) {
		t.Errorf("DefaultRetryOn(nil) = true, want false")
	}
}

func TestToolPolicy_BackoffFormula(t *testing.T) {
	cases := []struct {
		attempt int
		base    time.Duration
		max     time.Duration
		want    time.Duration
	}{
		{0, 100 * time.Millisecond, 0, 100 * time.Millisecond},
		{1, 100 * time.Millisecond, 0, 200 * time.Millisecond},
		{2, 100 * time.Millisecond, 0, 400 * time.Millisecond},
		{3, 100 * time.Millisecond, 300 * time.Millisecond, 300 * time.Millisecond},
		{10, 100 * time.Millisecond, 1 * time.Second, 1 * time.Second},
		{0, 0, 0, 0},
	}
	for _, c := range cases {
		got := BackoffDelay(c.base, c.max, c.attempt)
		if got != c.want {
			t.Errorf("BackoffDelay(%v, %v, %d) = %v, want %v", c.base, c.max, c.attempt, got, c.want)
		}
	}
}
