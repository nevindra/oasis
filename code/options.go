// Package code provides CodeRunner implementations for LLM code execution.
package code

import "time"

// Option configures a SubprocessRunner.
type Option func(*runnerConfig)

type runnerConfig struct {
	timeout        time.Duration
	maxOutput      int
	workspace      string
	envVars        map[string]string
	envPassthrough bool
}

func defaultConfig() runnerConfig {
	return runnerConfig{
		timeout:   30 * time.Second,
		maxOutput: 64 * 1024, // 64KB
	}
}

// WithTimeout sets the maximum execution duration for code.
// Default: 30s. The subprocess is killed (SIGKILL) on timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *runnerConfig) { c.timeout = d }
}

// WithMaxOutput sets the maximum output size in bytes.
// Output beyond this limit is truncated. Default: 64KB.
func WithMaxOutput(bytes int) Option {
	return func(c *runnerConfig) { c.maxOutput = bytes }
}

// WithWorkspace sets the working directory for code execution.
// Filesystem operations in the code are restricted to this directory.
// Default: os.TempDir().
func WithWorkspace(path string) Option {
	return func(c *runnerConfig) { c.workspace = path }
}

// WithEnv sets a specific environment variable for the subprocess.
// Multiple calls accumulate. These are added to the subprocess environment
// alongside any passthrough variables.
func WithEnv(key, value string) Option {
	return func(c *runnerConfig) {
		if c.envVars == nil {
			c.envVars = make(map[string]string)
		}
		c.envVars[key] = value
	}
}

// WithEnvPassthrough passes all host environment variables to the subprocess.
// By default, the subprocess inherits a minimal environment.
func WithEnvPassthrough() Option {
	return func(c *runnerConfig) { c.envPassthrough = true }
}
