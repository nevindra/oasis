// Package code provides CodeRunner implementations for LLM code execution.
package code

import "time"

// Option configures an HTTPRunner.
type Option func(*runnerConfig)

type runnerConfig struct {
	// Shared options.
	timeout   time.Duration
	maxOutput int

	// HTTPRunner options.
	sandboxURL      string
	callbackAddr    string // auto-start listener address (default "127.0.0.1:0")
	callbackExtAddr string // user-provided external address (skip auto-start)
	maxFileSize     int64  // max bytes per returned file
	maxRetries      int    // total attempts (1 = no retry)
	retryDelay      time.Duration
}

func defaultConfig() runnerConfig {
	return runnerConfig{
		timeout:      30 * time.Second,
		maxOutput:    64 * 1024,        // 64KB
		callbackAddr: "127.0.0.1:0",   // OS-assigned port
		maxFileSize:  10 << 20,         // 10MB
		maxRetries:   2,                // 1 retry
		retryDelay:   500 * time.Millisecond,
	}
}

// WithTimeout sets the maximum execution duration for code.
// Default: 30s.
func WithTimeout(d time.Duration) Option {
	return func(c *runnerConfig) { c.timeout = d }
}

// WithMaxOutput sets the maximum output size in bytes.
// Output beyond this limit is truncated. Default: 64KB.
func WithMaxOutput(bytes int) Option {
	return func(c *runnerConfig) { c.maxOutput = bytes }
}

// WithCallbackAddr sets the address for the auto-started callback HTTP server.
// The callback server handles tool dispatch requests from the sandbox.
// Default: "127.0.0.1:0" (OS-assigned port).
func WithCallbackAddr(addr string) Option {
	return func(c *runnerConfig) { c.callbackAddr = addr }
}

// WithCallbackExternal tells HTTPRunner that the callback handler is mounted
// on an external HTTP server at the given address. HTTPRunner will not start
// its own listener. Use runner.Handler() to get the http.Handler to mount.
//
// publicAddr should be the base URL reachable from the sandbox, e.g.
// "http://app:8080". The dispatch path /_oasis/dispatch is appended automatically.
func WithCallbackExternal(publicAddr string) Option {
	return func(c *runnerConfig) { c.callbackExtAddr = publicAddr }
}

// WithMaxFileSize sets the maximum size in bytes for a single returned file.
// Files exceeding this limit are included without data (metadata only).
// Default: 10MB.
func WithMaxFileSize(bytes int64) Option {
	return func(c *runnerConfig) { c.maxFileSize = bytes }
}

// WithMaxRetries sets the total number of attempts for the sandbox HTTP request.
// 1 means no retry; 2 means one retry on transient failure. Default: 2.
func WithMaxRetries(n int) Option {
	return func(c *runnerConfig) {
		if n < 1 {
			n = 1
		}
		c.maxRetries = n
	}
}

// WithRetryDelay sets the initial backoff delay between retries.
// The delay doubles on each subsequent retry. Default: 500ms.
func WithRetryDelay(d time.Duration) Option {
	return func(c *runnerConfig) { c.retryDelay = d }
}
