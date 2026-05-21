package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevindra/oasis/core"
)

// --- runWithPolicy tests ---

func TestRunWithPolicy_SuccessFirstAttempt(t *testing.T) {
	calls := 0
	res, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 3}, func(_ context.Context) (ToolResult, error) {
		calls++
		return ToolResult{Content: []byte(`"ok"`)}, nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if string(res.Content) != `"ok"` {
		t.Errorf("Content = %q, want \"ok\"", res.Content)
	}
}

func TestRunWithPolicy_RetriesUntilSuccess(t *testing.T) {
	var calls int32
	res, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 3, RetryDelay: 1 * time.Millisecond},
		func(_ context.Context) (ToolResult, error) {
			n := atomic.AddInt32(&calls, 1)
			if n < 3 {
				return ToolResult{}, core.RetryableError(errors.New("transient"))
			}
			return ToolResult{Content: []byte(`"finally"`)}, nil
		})
	if err != nil {
		t.Fatalf("err = %v, want nil after retries", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	if string(res.Content) != `"finally"` {
		t.Errorf("Content = %q", res.Content)
	}
}

func TestRunWithPolicy_NonRetryableErrorReturnsImmediately(t *testing.T) {
	var calls int32
	plain := errors.New("not retryable")
	_, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 5, RetryDelay: 1 * time.Millisecond},
		func(_ context.Context) (ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return ToolResult{}, plain
		})
	if !errors.Is(err, plain) {
		t.Errorf("err = %v, want plain error", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retries on non-retryable)", calls)
	}
}

func TestRunWithPolicy_ExhaustsRetries(t *testing.T) {
	var calls int32
	_, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 2, RetryDelay: 1 * time.Millisecond},
		func(_ context.Context) (ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return ToolResult{}, core.RetryableError(errors.New("always fails"))
		})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRunWithPolicy_TimeoutFires(t *testing.T) {
	var calls int32
	_, err := runWithPolicy(context.Background(), core.ToolPolicy{Timeout: 20 * time.Millisecond, Retries: 1, RetryDelay: 1 * time.Millisecond},
		func(ctx context.Context) (ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			select {
			case <-time.After(200 * time.Millisecond):
				return ToolResult{}, nil
			case <-ctx.Done():
				return ToolResult{}, ctx.Err()
			}
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestRunWithPolicy_ParentCancelAbortsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := runWithPolicy(ctx, core.ToolPolicy{Retries: 5, RetryDelay: 500 * time.Millisecond},
		func(_ context.Context) (ToolResult, error) {
			return ToolResult{}, core.RetryableError(errors.New("retry me"))
		})
	dur := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want Canceled", err)
	}
	if dur > 100*time.Millisecond {
		t.Errorf("loop did not exit promptly on cancel; took %v", dur)
	}
}

func TestRunWithPolicy_ZeroPolicyIsPassthrough(t *testing.T) {
	var calls int32
	plain := errors.New("plain")
	_, err := runWithPolicy(context.Background(), core.ToolPolicy{},
		func(_ context.Context) (ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return ToolResult{}, plain
		})
	if !errors.Is(err, plain) {
		t.Errorf("err = %v, want plain", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// --- WithToolPolicy / resolveToolPolicy tests ---

func TestWithToolPolicy_ExactName(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolPolicy("foo", core.ToolPolicy{Timeout: 5 * time.Second}),
	})
	p, ok := cfg.resolveToolPolicy("foo")
	if !ok || p.Timeout != 5*time.Second {
		t.Errorf("resolveToolPolicy(foo) = (%v, %v), want (5s, true)", p, ok)
	}
}

func TestWithToolPolicy_ExactOverwrites(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolPolicy("foo", core.ToolPolicy{Timeout: 1 * time.Second}),
		WithToolPolicy("foo", core.ToolPolicy{Timeout: 9 * time.Second}),
	})
	p, _ := cfg.resolveToolPolicy("foo")
	if p.Timeout != 9*time.Second {
		t.Errorf("Timeout = %v, want 9s (last-wins)", p.Timeout)
	}
}

func TestWithToolPolicyMatch_Ordering(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolPolicyMatch(func(n string) bool { return strings.HasPrefix(n, "mcp__") }, core.ToolPolicy{Timeout: 1 * time.Second}),
		WithToolPolicyMatch(func(n string) bool { return strings.HasPrefix(n, "mcp__github") }, core.ToolPolicy{Timeout: 2 * time.Second}),
	})
	p, _ := cfg.resolveToolPolicy("mcp__github__issues")
	if p.Timeout != 1*time.Second {
		t.Errorf("Timeout = %v, want 1s (first-match-wins)", p.Timeout)
	}
}

func TestResolvePolicy_ExactBeatsMatcher(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolPolicyMatch(func(n string) bool { return true }, core.ToolPolicy{Timeout: 1 * time.Second}),
		WithToolPolicy("special", core.ToolPolicy{Timeout: 7 * time.Second}),
	})
	p, _ := cfg.resolveToolPolicy("special")
	if p.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v, want 7s (exact beats matcher)", p.Timeout)
	}
}

func TestResolvePolicy_Unknown(t *testing.T) {
	cfg := BuildConfig(nil)
	if _, ok := cfg.resolveToolPolicy("nope"); ok {
		t.Error("resolveToolPolicy(nope) = ok=true, want false")
	}
}
