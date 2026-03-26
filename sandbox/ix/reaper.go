package ix

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
)

// reaper periodically destroys expired sandboxes and evicts the oldest
// sandbox when host disk space on /var/lib/docker drops below 5 GB.
func (m *IXManager) reaper(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reapExpired(ctx)
			m.reapDisk(ctx)
		}
	}
}

// reapExpired destroys sandboxes whose TTL has elapsed.
func (m *IXManager) reapExpired(ctx context.Context) {
	m.mu.RLock()
	snapshot := maps.Clone(m.sandboxes)
	m.mu.RUnlock()

	now := time.Now()
	for sessionID, sb := range snapshot {
		if now.After(sb.expiresAt) {
			m.logger.Info("reaper: TTL expired, destroying", "session", sessionID)
			if err := m.destroy(ctx, sessionID); err != nil {
				m.logger.Warn("reaper: destroy failed", "session", sessionID, "error", err)
			}
		}
	}
}

// reapDisk evicts the oldest sandbox when free disk space on /var/lib/docker
// falls below 5 GB.
func (m *IXManager) reapDisk(ctx context.Context) {
	if diskFreeGB("/var/lib/docker") >= 5 {
		return
	}

	m.mu.RLock()
	var oldestSID string
	var oldestTime time.Time
	for sid, sb := range m.sandboxes {
		if oldestSID == "" || sb.createdAt.Before(oldestTime) {
			oldestSID = sid
			oldestTime = sb.createdAt
		}
	}
	m.mu.RUnlock()

	if oldestSID == "" {
		return
	}

	m.logger.Warn("reaper: low disk space, evicting oldest sandbox",
		"session", oldestSID,
		"freeGB", diskFreeGB("/var/lib/docker"),
	)
	if err := m.destroy(ctx, oldestSID); err != nil {
		m.logger.Warn("reaper: evict failed", "session", oldestSID, "error", err)
	}
}

// recover reclaims running sandbox containers from a previous manager
// instance, destroys expired or stopped containers, and sweeps orphaned
// networks.
func (m *IXManager) recover(ctx context.Context) error {
	containers, err := m.docker.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "oasis.sandbox=true"),
		),
	})
	if err != nil {
		return err
	}

	for _, c := range containers {
		expiresStr := c.Labels["oasis.expires"]
		sessionID := c.Labels["oasis.session"]
		networkID := m.networkIDFromContainer(c)

		expires, parseErr := time.Parse(time.RFC3339, expiresStr)
		expired := parseErr != nil || time.Now().After(expires)
		running := c.State == container.StateRunning

		if expired || !running {
			m.logger.Info("recover: destroying stale container",
				"container", c.ID[:12],
				"session", sessionID,
				"expired", expired,
				"running", running,
			)
			_ = m.destroyContainer(ctx, c.ID, networkID)
			continue
		}

		// Reclaim: resolve port, create sandbox, register.
		baseURL, err := m.resolveBaseURL(ctx, c.ID)
		if err != nil {
			m.logger.Warn("recover: resolve port failed, destroying",
				"container", c.ID[:12],
				"error", err,
			)
			_ = m.destroyContainer(ctx, c.ID, networkID)
			continue
		}

		// Acquire semaphore slot.
		select {
		case m.semaphore <- struct{}{}:
		default:
			m.logger.Warn("recover: no capacity, destroying container",
				"container", c.ID[:12],
				"session", sessionID,
			)
			_ = m.destroyContainer(ctx, c.ID, networkID)
			continue
		}

		sb := &IXSandbox{
			id:          sessionID,
			containerID: c.ID,
			baseURL:     baseURL,
			client:      newClient(baseURL, &http.Client{Timeout: 30 * time.Second}),
			networkID:   networkID,
			createdAt:   time.Unix(c.Created, 0),
			expiresAt:   expires,
		}

		m.mu.Lock()
		m.sandboxes[sessionID] = sb
		m.mu.Unlock()

		m.logger.Info("recover: reclaimed sandbox",
			"session", sessionID,
			"container", c.ID[:12],
			"expiresIn", time.Until(expires).Round(time.Second),
		)
	}

	// Sweep orphaned networks with "sandbox-" prefix.
	m.sweepOrphanedNetworks(ctx)

	return nil
}

// sweepOrphanedNetworks removes networks with a "sandbox-" name prefix that
// have no attached containers.
func (m *IXManager) sweepOrphanedNetworks(ctx context.Context) {
	networks, err := m.docker.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		m.logger.Warn("recover: list networks failed", "error", err)
		return
	}

	for _, n := range networks {
		if !strings.HasPrefix(n.Name, "sandbox-") {
			continue
		}
		if len(n.Containers) == 0 {
			m.logger.Info("recover: removing orphaned network",
				"network", n.Name,
				"id", n.ID[:12],
			)
			if err := m.docker.NetworkRemove(ctx, n.ID); err != nil {
				m.logger.Warn("recover: network remove failed",
					"network", n.Name,
					"error", err,
				)
			}
		}
	}
}

// networkIDFromContainer extracts the network ID from a container's network
// mode (HostConfig) name. Falls back to empty string.
func (m *IXManager) networkIDFromContainer(c container.Summary) string {
	netMode := c.HostConfig.NetworkMode
	if netMode == "" || !strings.HasPrefix(netMode, "sandbox-") {
		return ""
	}
	// The network name is the network mode for user-defined bridge networks.
	// We need the network ID, so inspect it.
	info, err := m.docker.NetworkInspect(context.Background(), netMode, network.InspectOptions{})
	if err != nil {
		return ""
	}
	return info.ID
}

// diskFreeGB returns the free disk space in gigabytes at the given path.
// Returns 999 (assume plenty) if the check fails.
func diskFreeGB(path string) int {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 999 // assume plenty if can't check
	}
	return int((stat.Bavail * uint64(stat.Bsize)) / (1 << 30))
}
