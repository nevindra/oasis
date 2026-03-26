package ix

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"

	"github.com/nevindra/oasis/sandbox"
)

// ManagerConfig configures an IXManager.
type ManagerConfig struct {
	Image         string            // default: "oasis-ix:latest"
	Runtime       string            // "kata", "runsc", or "" for default Docker
	MaxConcurrent int               // 0 = auto-detect from host resources
	DefaultTTL    time.Duration     // default: 1 hour
	PerSandbox    sandbox.ResourceSpec
	MaxRestarts   int               // default: 3
	Logger        *slog.Logger
}

// applyDefaults fills zero-valued fields with sensible defaults.
func (c *ManagerConfig) applyDefaults() {
	if c.Image == "" {
		c.Image = "oasis-ix:latest"
	}
	if c.DefaultTTL == 0 {
		c.DefaultTTL = time.Hour
	}
	if c.PerSandbox.CPU == 0 {
		c.PerSandbox.CPU = 1
	}
	if c.PerSandbox.Memory == 0 {
		c.PerSandbox.Memory = 2 << 30 // 2 GB
	}
	if c.PerSandbox.Disk == 0 {
		c.PerSandbox.Disk = 10 << 30 // 10 GB
	}
	if c.MaxRestarts == 0 {
		c.MaxRestarts = 3
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// IXManager manages sandbox container lifecycle using Docker.
type IXManager struct {
	docker    client.APIClient
	cfg       ManagerConfig
	sandboxes map[string]*IXSandbox // keyed by sessionID
	mu        sync.RWMutex
	semaphore chan struct{} // concurrency limiter
	accepting atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
	logger    *slog.Logger
}

// NewManager connects to Docker, auto-detects limits, and starts background
// goroutines (monitor + reaper stubs). Call Shutdown or Close to release resources.
func NewManager(ctx context.Context, cfg ManagerConfig) (*IXManager, error) {
	cfg.applyDefaults()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}

	// Verify Docker connectivity.
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close()
		return nil, fmt.Errorf("docker ping: %w", err)
	}

	maxConc := cfg.MaxConcurrent
	if maxConc <= 0 {
		maxConc = autoDetectMax(cfg.PerSandbox)
	}
	if maxConc < 1 {
		maxConc = 1
	}

	mCtx, cancel := context.WithCancel(ctx)

	m := &IXManager{
		docker:    cli,
		cfg:       cfg,
		sandboxes: make(map[string]*IXSandbox),
		semaphore: make(chan struct{}, maxConc),
		ctx:       mCtx,
		cancel:    cancel,
		logger:    cfg.Logger,
	}
	m.accepting.Store(true)

	// Recover existing containers from a previous run.
	if err := m.recover(mCtx); err != nil {
		m.logger.Warn("recover failed", "error", err)
	}

	go m.monitor(mCtx)
	go m.reaper(mCtx)

	return m, nil
}

// Create provisions a new sandbox container.
func (m *IXManager) Create(ctx context.Context, opts sandbox.CreateOpts) (sandbox.Sandbox, error) {
	if !m.accepting.Load() {
		return nil, sandbox.ErrShuttingDown
	}

	resolved := m.resolveOpts(opts)

	// Acquire concurrency slot.
	if err := acquireSlot(ctx, m.semaphore, func() bool { return m.evictIdle(ctx) }, 30*time.Second); err != nil {
		return nil, err
	}

	sandboxID := uuid.NewString()[:12]
	networkName := "sandbox-" + sandboxID

	// Create per-sandbox network.
	netResp, err := m.docker.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			"oasis.sandbox": "true",
			"oasis.session": resolved.SessionID,
		},
	})
	if err != nil {
		m.releaseSlot()
		return nil, fmt.Errorf("create network: %w", err)
	}

	// Build container config.
	ttl := resolved.TTL
	now := time.Now()

	exposedPorts, portBindings, _ := nat.ParsePortSpecs([]string{"127.0.0.1::8080/tcp"})

	var pidsLimit int64 = 256
	hostCfg := &container.HostConfig{
		Runtime: m.cfg.Runtime,
		Resources: container.Resources{
			Memory:    resolved.Resources.Memory,
			CPUQuota:  int64(resolved.Resources.CPU) * 100000,
			CPUPeriod: 100000,
			PidsLimit: &pidsLimit,
		},
		NetworkMode:  container.NetworkMode(networkName),
		PortBindings: portBindings,
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyDisabled,
		},
		SecurityOpt: []string{"no-new-privileges:true"},
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{"CHOWN", "SETUID", "SETGID", "KILL", "NET_BIND_SERVICE"},
	}

	// Build env vars.
	var envSlice []string
	for k, v := range resolved.Env {
		envSlice = append(envSlice, k+"="+v)
	}

	containerCfg := &container.Config{
		Image:        resolved.Image,
		ExposedPorts: exposedPorts,
		Env:          envSlice,
		Labels: map[string]string{
			"oasis.sandbox": "true",
			"oasis.session": resolved.SessionID,
			"oasis.created": now.Format(time.RFC3339),
			"oasis.expires": now.Add(ttl).Format(time.RFC3339),
		},
	}

	resp, err := m.docker.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "sandbox-"+sandboxID)
	if err != nil {
		// Attempt cleanup of network on failure.
		_ = m.docker.NetworkRemove(ctx, netResp.ID)
		m.releaseSlot()
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := m.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		_ = m.docker.NetworkRemove(ctx, netResp.ID)
		m.releaseSlot()
		return nil, fmt.Errorf("start container: %w", err)
	}

	baseURL, err := m.resolveBaseURL(ctx, resp.ID)
	if err != nil {
		_ = m.destroyContainer(ctx, resp.ID, netResp.ID)
		m.releaseSlot()
		return nil, fmt.Errorf("resolve port: %w", err)
	}

	if err := m.waitReady(ctx, baseURL); err != nil {
		_ = m.destroyContainer(ctx, resp.ID, netResp.ID)
		m.releaseSlot()
		return nil, fmt.Errorf("wait ready: %w", err)
	}

	sb := &IXSandbox{
		id:          resolved.SessionID,
		containerID: resp.ID,
		baseURL:     baseURL,
		client:      newClient(baseURL, &http.Client{Timeout: 30 * time.Second}),
		networkID:   netResp.ID,
		createdAt:   now,
		expiresAt:   now.Add(ttl),
	}

	m.mu.Lock()
	m.sandboxes[resolved.SessionID] = sb
	m.mu.Unlock()

	m.logger.Info("sandbox created",
		"session", resolved.SessionID,
		"container", resp.ID[:12],
		"baseURL", baseURL,
		"ttl", ttl,
	)

	return sb, nil
}

// Get retrieves an existing sandbox by session ID.
func (m *IXManager) Get(sessionID string) (sandbox.Sandbox, error) {
	m.mu.RLock()
	sb, ok := m.sandboxes[sessionID]
	m.mu.RUnlock()
	if !ok {
		return nil, sandbox.ErrNotFound
	}
	return sb, nil
}

// Shutdown stops accepting new sandboxes, waits for in-flight work to drain,
// and keeps containers alive for recovery on next boot.
func (m *IXManager) Shutdown(ctx context.Context) error {
	m.accepting.Store(false)

	// Wait for context or drain (all slots released).
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.mu.RLock()
			active := len(m.sandboxes)
			m.mu.RUnlock()
			if active == 0 {
				m.cancel()
				return nil
			}
		}
	}
}

// Close force-destroys all managed sandboxes and their networks.
func (m *IXManager) Close() error {
	m.accepting.Store(false)
	m.cancel()

	m.mu.Lock()
	sessions := make([]string, 0, len(m.sandboxes))
	for sid := range m.sandboxes {
		sessions = append(sessions, sid)
	}
	m.mu.Unlock()

	var firstErr error
	for _, sid := range sessions {
		if err := m.destroy(context.Background(), sid); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if err := m.docker.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// --- Helpers ---

// resolveOpts merges user-provided CreateOpts with manager defaults.
func (m *IXManager) resolveOpts(opts sandbox.CreateOpts) sandbox.CreateOpts {
	if opts.Image == "" {
		opts.Image = m.cfg.Image
	}
	if opts.TTL == 0 {
		opts.TTL = m.cfg.DefaultTTL
	}
	if opts.Resources.CPU == 0 {
		opts.Resources.CPU = m.cfg.PerSandbox.CPU
	}
	if opts.Resources.Memory == 0 {
		opts.Resources.Memory = m.cfg.PerSandbox.Memory
	}
	if opts.Resources.Disk == 0 {
		opts.Resources.Disk = m.cfg.PerSandbox.Disk
	}
	return opts
}

// Destroy stops and removes a sandbox by session ID.
func (m *IXManager) Destroy(ctx context.Context, sessionID string) error {
	return m.destroy(ctx, sessionID)
}

// destroy removes a sandbox from the map, destroys its container and network,
// and releases the concurrency slot.
func (m *IXManager) destroy(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	sb, ok := m.sandboxes[sessionID]
	if ok {
		delete(m.sandboxes, sessionID)
	}
	m.mu.Unlock()
	if !ok {
		return sandbox.ErrNotFound
	}

	sb.Close()

	err := m.destroyContainer(ctx, sb.containerID, sb.networkID)
	m.releaseSlot()
	return err
}

// destroyContainer stops and removes a container and its network.
func (m *IXManager) destroyContainer(ctx context.Context, containerID, networkID string) error {
	timeout := 10
	_ = m.docker.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	if err := m.docker.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		m.logger.Warn("container remove failed", "container", containerID, "error", err)
	}
	if networkID != "" {
		if err := m.docker.NetworkRemove(ctx, networkID); err != nil {
			m.logger.Warn("network remove failed", "network", networkID, "error", err)
			return fmt.Errorf("network remove: %w", err)
		}
	}
	return nil
}

// releaseSlot returns a concurrency slot to the semaphore.
func (m *IXManager) releaseSlot() {
	select {
	case <-m.semaphore:
	default:
	}
}

// evictIdle destroys the oldest idle sandbox to free a slot. Returns true if
// a sandbox was evicted.
func (m *IXManager) evictIdle(ctx context.Context) bool {
	m.mu.RLock()
	var oldest *IXSandbox
	var oldestSID string
	now := time.Now()
	for sid, sb := range m.sandboxes {
		if now.After(sb.expiresAt) {
			if oldest == nil || sb.createdAt.Before(oldest.createdAt) {
				oldest = sb
				oldestSID = sid
			}
		}
	}
	m.mu.RUnlock()

	if oldest == nil {
		return false
	}

	if err := m.destroy(ctx, oldestSID); err != nil {
		m.logger.Warn("evict idle failed", "session", oldestSID, "error", err)
		return false
	}
	m.logger.Info("evicted idle sandbox", "session", oldestSID)
	return true
}

// resolveBaseURL inspects the running container to find the host-assigned port
// for 8080/tcp and returns the base URL.
func (m *IXManager) resolveBaseURL(ctx context.Context, containerID string) (string, error) {
	info, err := m.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("inspect container: %w", err)
	}
	if info.NetworkSettings == nil {
		return "", fmt.Errorf("no network settings for container %s", containerID)
	}

	port := nat.Port("8080/tcp")
	bindings, ok := info.NetworkSettings.Ports[port]
	if !ok || len(bindings) == 0 {
		return "", fmt.Errorf("no port binding for %s on container %s", port, containerID)
	}

	hostPort := bindings[0].HostPort
	if hostPort == "" {
		return "", fmt.Errorf("empty host port for %s on container %s", port, containerID)
	}

	return fmt.Sprintf("http://127.0.0.1:%s", hostPort), nil
}

// waitReady polls the ix daemon health endpoint until it responds with HTTP 200
// or the context/timeout expires.
func (m *IXManager) waitReady(ctx context.Context, baseURL string) error {
	deadline := time.After(60 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	httpClient := &http.Client{Timeout: 3 * time.Second}
	endpoint := baseURL + "/health"

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("sandbox not ready after 60s at %s", baseURL)
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				continue
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
	}
}

// acquireSlot attempts to acquire a concurrency slot. It tries the fast path
// first, then attempts eviction, then queues with a timeout.
func acquireSlot(ctx context.Context, sem chan struct{}, tryEvict func() bool, timeout time.Duration) error {
	// Fast path: slot available.
	select {
	case sem <- struct{}{}:
		return nil
	default:
	}

	// Try eviction.
	if tryEvict() {
		select {
		case sem <- struct{}{}:
			return nil
		default:
		}
	}

	// Queue with timeout.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case sem <- struct{}{}:
		return nil
	case <-timer.C:
		return sandbox.ErrCapacityFull
	case <-ctx.Done():
		return ctx.Err()
	}
}

// autoDetectMax calculates maximum concurrent sandboxes from host resources.
func autoDetectMax(perSandbox sandbox.ResourceSpec) int {
	cpus := runtime.NumCPU()
	cpuMax := cpus / max(perSandbox.CPU, 1)

	memMax := hostMemoryBytes() / max(perSandbox.Memory, 1)

	result := min(cpuMax, int(memMax))
	if result < 1 {
		result = 1
	}
	return result
}

// Compile-time check that IXManager implements sandbox.Manager.
var _ sandbox.Manager = (*IXManager)(nil)

// EnsureImage pulls the configured image if it is not already present locally.
func (m *IXManager) EnsureImage(ctx context.Context) error {
	_, _, err := m.docker.ImageInspectWithRaw(ctx, m.cfg.Image)
	if err == nil {
		return nil // already present
	}
	m.logger.Info("pulling image", "image", m.cfg.Image)
	rc, err := m.docker.ImagePull(ctx, m.cfg.Image, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("image pull %s: %w", m.cfg.Image, err)
	}
	defer rc.Close()
	// Drain the pull output to completion.
	buf := make([]byte, 32*1024)
	for {
		if _, err := rc.Read(buf); err != nil {
			break
		}
	}
	return nil
}
