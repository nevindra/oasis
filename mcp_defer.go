package oasis

// deferConfig holds configuration for deferred MCP schema behavior.
//
// In v1, only the "opt-in" flag (set by WithDeferredSchemas being called at
// all) is honored. Threshold-based auto-defer is accepted in the API but not
// implemented yet — it will land in a v1.x minor release once telemetry
// informs a sensible default. DeferAlwaysOn behaves identically to plain
// opt-in in v1.
type deferConfig struct {
	enabled          bool            // true if WithDeferredSchemas was called
	alwaysOn         bool            // reserved for v1.x behavior
	thresholdPercent int             // 0-100; reserved for v1.x
	exclude          map[string]bool // server names that should NOT be deferred
}

// DeferOption configures WithDeferredSchemas.
type DeferOption func(*deferConfig)

// DeferThreshold sets the percentage of context window above which deferred
// loading activates. Reserved for v1.x; accepted in v1 but ignored.
// Values are clamped to [0, 100].
func DeferThreshold(percent int) DeferOption {
	return func(c *deferConfig) {
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		c.thresholdPercent = percent
	}
}

// DeferAlwaysOn forces all MCP tool schemas to be deferred regardless of
// threshold. Equivalent to plain WithDeferredSchemas() in v1.
func DeferAlwaysOn() DeferOption {
	return func(c *deferConfig) { c.alwaysOn = true }
}

// DeferExclude keeps the named MCP servers' schemas eager (never deferred),
// even when deferred mode is active. Useful for servers whose tools are
// frequently called and worth their schema cost up-front.
func DeferExclude(serverNames ...string) DeferOption {
	return func(c *deferConfig) {
		if c.exclude == nil {
			c.exclude = make(map[string]bool)
		}
		for _, n := range serverNames {
			c.exclude[n] = true
		}
	}
}
