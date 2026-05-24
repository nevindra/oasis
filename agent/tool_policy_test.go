package agent

import (
	"context"
	"encoding/json"
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
	res, err := runWithPolicy(context.Background(), core.ToolPolicy{Retries: 3}, func(_ context.Context) (core.ToolResult, error) {
		calls++
		return core.ToolResult{Content: []byte(`"ok"`)}, nil
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
		func(_ context.Context) (core.ToolResult, error) {
			n := atomic.AddInt32(&calls, 1)
			if n < 3 {
				return core.ToolResult{}, core.RetryableError(errors.New("transient"))
			}
			return core.ToolResult{Content: []byte(`"finally"`)}, nil
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
		func(_ context.Context) (core.ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return core.ToolResult{}, plain
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
		func(_ context.Context) (core.ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return core.ToolResult{}, core.RetryableError(errors.New("always fails"))
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
		func(ctx context.Context) (core.ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			select {
			case <-time.After(200 * time.Millisecond):
				return core.ToolResult{}, nil
			case <-ctx.Done():
				return core.ToolResult{}, ctx.Err()
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
		func(_ context.Context) (core.ToolResult, error) {
			return core.ToolResult{}, core.RetryableError(errors.New("retry me"))
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
		func(_ context.Context) (core.ToolResult, error) {
			atomic.AddInt32(&calls, 1)
			return core.ToolResult{}, plain
		})
	if !errors.Is(err, plain) {
		t.Errorf("err = %v, want plain", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// --- WithToolPolicy / ResolveToolPolicy tests ---

func TestWithToolPolicy_ExactName(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolConfig(ToolConfig{Policies: map[string]core.ToolPolicy{"foo": {Timeout: 5 * time.Second}}}),
	})
	p, ok := cfg.ResolveToolPolicy("foo")
	if !ok || p.Timeout != 5*time.Second {
		t.Errorf("ResolveToolPolicy(foo) = (%v, %v), want (5s, true)", p, ok)
	}
}

func TestWithToolPolicy_ExactOverwrites(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolConfig(ToolConfig{Policies: map[string]core.ToolPolicy{"foo": {Timeout: 1 * time.Second}}}),
		WithToolConfig(ToolConfig{Policies: map[string]core.ToolPolicy{"foo": {Timeout: 9 * time.Second}}}),
	})
	p, _ := cfg.ResolveToolPolicy("foo")
	if p.Timeout != 9*time.Second {
		t.Errorf("Timeout = %v, want 9s (last-wins)", p.Timeout)
	}
}

func TestWithToolPolicyMatch_Ordering(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolConfig(ToolConfig{PolicyMatchers: []ToolPolicyMatcher{
			{Match: func(n string) bool { return strings.HasPrefix(n, "mcp__") }, Policy: core.ToolPolicy{Timeout: 1 * time.Second}},
			{Match: func(n string) bool { return strings.HasPrefix(n, "mcp__github") }, Policy: core.ToolPolicy{Timeout: 2 * time.Second}},
		}}),
	})
	p, _ := cfg.ResolveToolPolicy("mcp__github__issues")
	if p.Timeout != 1*time.Second {
		t.Errorf("Timeout = %v, want 1s (first-match-wins)", p.Timeout)
	}
}

func TestResolvePolicy_ExactBeatsMatcher(t *testing.T) {
	cfg := BuildConfig([]AgentOption{
		WithToolConfig(ToolConfig{
			PolicyMatchers: []ToolPolicyMatcher{
				{Match: func(n string) bool { return true }, Policy: core.ToolPolicy{Timeout: 1 * time.Second}},
			},
			Policies: map[string]core.ToolPolicy{"special": {Timeout: 7 * time.Second}},
		}),
	})
	p, _ := cfg.ResolveToolPolicy("special")
	if p.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v, want 7s (exact beats matcher)", p.Timeout)
	}
}

func TestResolvePolicy_Unknown(t *testing.T) {
	cfg := BuildConfig(nil)
	if _, ok := cfg.ResolveToolPolicy("nope"); ok {
		t.Error("resolveToolPolicy(nope) = ok=true, want false")
	}
}

// --- NewStandardDispatch policy integration tests ---

// policyTestExec is a configurable fake tool executor for dispatch tests.
type policyTestExec struct {
	calls  int32
	errFn  func(int32) error
	result core.ToolResult
}

func (p *policyTestExec) exec(_ context.Context, _ string, _ json.RawMessage) (core.ToolResult, error) {
	n := atomic.AddInt32(&p.calls, 1)
	if p.errFn != nil {
		if err := p.errFn(n); err != nil {
			return core.ToolResult{}, err
		}
	}
	return p.result, nil
}

func (p *policyTestExec) execStream(_ context.Context, _ string, _ json.RawMessage, _ chan<- core.StreamEvent) (core.ToolResult, error) {
	atomic.AddInt32(&p.calls, 1)
	return p.result, nil
}

func TestNewStandardDispatch_PolicyRetries(t *testing.T) {
	p := &policyTestExec{
		result: core.ToolResult{Content: []byte(`"done"`)},
		errFn: func(n int32) error {
			if n < 3 {
				return core.RetryableError(errors.New("transient"))
			}
			return nil
		},
	}
	cfg := StandardDispatchConfig{
		ExecuteTool:     p.exec,
		IsStreamingTool: func(string) bool { return false },
		ResolvePolicy: func(name string) (core.ToolPolicy, bool) {
			return core.ToolPolicy{Retries: 5, RetryDelay: 1 * time.Millisecond}, true
		},
	}
	d := NewStandardDispatch(cfg)
	dr := d(context.Background(), core.ToolCall{Name: "myTool", Args: json.RawMessage(`{}`)})
	if dr.IsError {
		t.Fatalf("expected success after retries, got IsError; Content=%q", dr.Content)
	}
	if p.calls != 3 {
		t.Errorf("calls = %d, want 3", p.calls)
	}
}

func TestNewStandardDispatch_StreamingBypassesPolicy(t *testing.T) {
	p := &policyTestExec{result: core.ToolResult{Content: []byte(`"streamed"`)}}
	cfg := StandardDispatchConfig{
		ExecuteTool:       p.exec,
		ExecuteToolStream: p.execStream,
		StreamCh:          make(chan core.StreamEvent, 1),
		IsStreamingTool:   func(string) bool { return true },
		ResolvePolicy: func(string) (core.ToolPolicy, bool) {
			return core.ToolPolicy{Retries: 99}, true
		},
	}
	d := NewStandardDispatch(cfg)
	dr := d(context.Background(), core.ToolCall{Name: "stream", Args: json.RawMessage(`{}`)})
	if dr.IsError {
		t.Fatalf("unexpected IsError: %q", dr.Content)
	}
	if p.calls != 1 {
		t.Errorf("calls = %d, want 1 (policy must NOT apply to streaming tools)", p.calls)
	}
}

func TestNewStandardDispatch_NoPolicyPassthrough(t *testing.T) {
	p := &policyTestExec{result: core.ToolResult{Content: []byte(`"plain"`)}}
	cfg := StandardDispatchConfig{
		ExecuteTool:     p.exec,
		IsStreamingTool: func(string) bool { return false },
		ResolvePolicy:   func(string) (core.ToolPolicy, bool) { return core.ToolPolicy{}, false },
	}
	d := NewStandardDispatch(cfg)
	dr := d(context.Background(), core.ToolCall{Name: "plain", Args: nil})
	if dr.IsError {
		t.Fatalf("unexpected IsError: %q", dr.Content)
	}
	if p.calls != 1 {
		t.Errorf("calls = %d, want 1", p.calls)
	}
}
