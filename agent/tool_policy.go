package agent

import (
	"context"
	"time"

	"github.com/nevindra/oasis/core"
)

// runWithPolicy executes fn under the given ToolPolicy. It applies the
// per-attempt Timeout (if non-zero), the Retries budget, and the exponential
// backoff. The backoff sleep aborts immediately when the parent context is
// cancelled. The retry predicate defaults to core.DefaultRetryOn.
//
// Return contract:
//   - Success: (result, nil).
//   - All retries exhaust on retryable error: (result, lastErr).
//   - Non-retryable error: (result, err) on first attempt; no retries.
//   - Parent ctx cancelled mid-backoff: (zeroResult, ctx.Err()).
func runWithPolicy(parent context.Context, policy core.ToolPolicy, fn func(context.Context) (ToolResult, error)) (ToolResult, error) {
	retryOn := policy.RetryOn
	if retryOn == nil {
		retryOn = core.DefaultRetryOn
	}

	var (
		result  ToolResult
		lastErr error
	)
	for attempt := 0; attempt <= policy.Retries; attempt++ {
		if err := parent.Err(); err != nil {
			return ToolResult{}, err
		}

		attemptCtx := parent
		var cancel context.CancelFunc
		if policy.Timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(parent, policy.Timeout)
		}
		result, lastErr = fn(attemptCtx)
		if cancel != nil {
			cancel()
		}

		if lastErr == nil {
			return result, nil
		}
		if attempt == policy.Retries || !retryOn(lastErr) {
			return result, lastErr
		}

		delay := core.BackoffDelay(policy.RetryDelay, policy.MaxRetryDelay, attempt)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-parent.Done():
				timer.Stop()
				return ToolResult{}, parent.Err()
			}
		}
	}
	return result, lastErr
}

// resolveToolPolicy implements ServeMux-style policy lookup: exact-name
// entry first, then matchers in registration order. Returns the policy
// and true if any rule matched.
func (c *Config) resolveToolPolicy(name string) (core.ToolPolicy, bool) {
	if c == nil {
		return core.ToolPolicy{}, false
	}
	if p, ok := c.toolPolicies[name]; ok {
		return p, true
	}
	for _, m := range c.toolPolicyMatchers {
		if m.match(name) {
			return m.policy, true
		}
	}
	return core.ToolPolicy{}, false
}
