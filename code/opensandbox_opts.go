package code

import "time"

// OpenSandboxOption configures an OpenSandboxRunner.
type OpenSandboxOption func(*openSandboxConfig)

type openSandboxConfig struct {
	serverURL  string
	apiKey     string
	execdToken string

	image        string
	resourceCPU  string
	resourceMem  string
	entrypoint   []string
	sandboxTTL   int               // seconds, 0 = no auto-terminate
	sandboxEnv   map[string]string

	execTimeout time.Duration
	maxFileSize int64

	callbackAddr    string
	callbackExtAddr string

	maxRetries int
	retryDelay time.Duration
}

func defaultOpenSandboxConfig() openSandboxConfig {
	return openSandboxConfig{
		image:        "python:3.11-slim",
		resourceCPU:  "500m",
		resourceMem:  "512Mi",
		entrypoint:   []string{"sleep", "infinity"},
		sandboxTTL:   600,
		execTimeout:  30 * time.Second,
		maxFileSize:  10 << 20,
		callbackAddr: "127.0.0.1:0",
		maxRetries:   2,
		retryDelay:   500 * time.Millisecond,
	}
}

// WithImage sets the container image for new sandboxes.
// Default: "python:3.11-slim".
func WithImage(image string) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.image = image }
}

// WithResources sets the CPU and memory resource limits for the sandbox.
// cpu uses Kubernetes notation (e.g. "500m", "1"), mem uses binary units
// (e.g. "512Mi", "1Gi"). Default: "500m" CPU, "512Mi" memory.
func WithResources(cpu, mem string) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.resourceCPU = cpu; c.resourceMem = mem }
}

// WithExecdToken sets the access token for the execd sidecar inside the sandbox.
// When set, the token is sent as X-EXECD-ACCESS-TOKEN on every execd request.
func WithExecdToken(token string) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.execdToken = token }
}

// WithEntrypoint overrides the container entrypoint command.
// Default: ["sleep", "infinity"].
func WithEntrypoint(args ...string) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.entrypoint = args }
}

// WithSandboxTTL sets the sandbox auto-termination timeout in seconds.
// 0 disables auto-termination. Default: 600 (10 minutes).
func WithSandboxTTL(seconds int) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.sandboxTTL = seconds }
}

// WithSandboxEnv sets additional environment variables passed to the sandbox
// container at creation time. These are distinct from per-execution env vars.
func WithSandboxEnv(env map[string]string) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.sandboxEnv = env }
}

// WithExecTimeout sets the maximum duration for a single code execution.
// Default: 30s.
func WithExecTimeout(d time.Duration) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.execTimeout = d }
}

// WithMaxFileDownload sets the maximum size in bytes for a single file
// downloaded from the sandbox. Files exceeding this limit are skipped.
// Default: 10MB.
func WithMaxFileDownload(bytes int64) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.maxFileSize = bytes }
}

// WithCallbackListenAddr sets the address for the auto-started callback HTTP
// server that receives tool dispatch requests from sandbox code.
// Default: "127.0.0.1:0" (OS-assigned port).
func WithCallbackListenAddr(addr string) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.callbackAddr = addr }
}

// WithExternalCallbackAddr tells the runner that the callback handler is
// mounted on an external HTTP server at the given address. The runner will
// not start its own listener. Use runner.Handler() to get the handler.
func WithExternalCallbackAddr(addr string) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.callbackExtAddr = addr }
}

// WithRetryCount sets the total number of attempts for transient API errors.
// Values below 1 are clamped to 1 (no retry). Default: 2 (one retry).
func WithRetryCount(n int) OpenSandboxOption {
	return func(c *openSandboxConfig) {
		if n < 1 {
			n = 1
		}
		c.maxRetries = n
	}
}

// WithRetryBackoff sets the initial backoff delay between retries.
// The delay doubles on each subsequent retry. Default: 500ms.
func WithRetryBackoff(d time.Duration) OpenSandboxOption {
	return func(c *openSandboxConfig) { c.retryDelay = d }
}
