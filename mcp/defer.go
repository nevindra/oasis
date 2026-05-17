package mcp

// deferConfig holds configuration for deferred MCP schema behavior.
type deferConfig struct {
	enabled          bool
	alwaysOn         bool
	thresholdPercent int
	exclude          map[string]bool
}

// DeferOption configures WithDeferredSchemas.
type DeferOption func(*deferConfig)

// DeferThreshold sets the percentage of context window above which deferred
// loading activates. Reserved for v1.x; accepted in v1 but ignored.
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

// DeferExclude keeps the named MCP servers' schemas eager (never deferred).
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
