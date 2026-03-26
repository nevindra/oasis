package ix

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
)

// monitor periodically health-checks every sandbox and restarts or marks
// failed those that exceed the consecutive failure threshold.
func (m *IXManager) monitor(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			snapshot := maps.Clone(m.sandboxes)
			m.mu.RUnlock()

			for sessionID, sb := range snapshot {
				if sb.healthCheck(ctx) {
					m.mu.Lock()
					if cur, ok := m.sandboxes[sessionID]; ok {
						cur.failCount = 0
					}
					m.mu.Unlock()
					continue
				}

				m.mu.Lock()
				cur, ok := m.sandboxes[sessionID]
				if !ok {
					m.mu.Unlock()
					continue
				}
				cur.failCount++
				failCount := cur.failCount
				restartCount := cur.restartCount
				m.mu.Unlock()

				if failCount >= 3 {
					if restartCount < m.cfg.MaxRestarts {
						m.restart(ctx, sessionID)
					} else {
						m.markFailed(ctx, sessionID)
					}
				}
			}
		}
	}
}

// restart destroys the old sandbox container and replaces it with a fresh one,
// preserving the session ID and incrementing the restart counter.
func (m *IXManager) restart(ctx context.Context, sessionID string) {
	m.mu.Lock()
	old, ok := m.sandboxes[sessionID]
	if !ok {
		m.mu.Unlock()
		return
	}
	oldContainerID := old.containerID
	oldNetworkID := old.networkID
	oldRestartCount := old.restartCount
	remainingTTL := time.Until(old.expiresAt)
	if remainingTTL < 0 {
		remainingTTL = 0
	}
	m.mu.Unlock()

	// Destroy old container + network.
	old.Close()
	_ = m.destroyContainer(ctx, oldContainerID, oldNetworkID)

	// Create replacement.
	sandboxID := uuid.NewString()[:12]
	networkName := "sandbox-" + sandboxID
	now := time.Now()

	netResp, err := m.docker.NetworkCreate(ctx, networkName, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			"oasis.sandbox": "true",
			"oasis.session": sessionID,
		},
	})
	if err != nil {
		m.logger.Error("restart: create network failed", "session", sessionID, "error", err)
		return
	}

	exposedPorts, portBindings, _ := nat.ParsePortSpecs([]string{"127.0.0.1::8080/tcp"})

	var pidsLimit int64 = 256
	hostCfg := &container.HostConfig{
		Runtime: m.cfg.Runtime,
		Resources: container.Resources{
			Memory:    m.cfg.PerSandbox.Memory,
			CPUQuota:  int64(m.cfg.PerSandbox.CPU) * 100000,
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

	containerCfg := &container.Config{
		Image:        m.cfg.Image,
		ExposedPorts: exposedPorts,
		Labels: map[string]string{
			"oasis.sandbox": "true",
			"oasis.session": sessionID,
			"oasis.created": now.Format(time.RFC3339),
			"oasis.expires": now.Add(remainingTTL).Format(time.RFC3339),
		},
	}

	resp, err := m.docker.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "sandbox-"+sandboxID)
	if err != nil {
		_ = m.docker.NetworkRemove(ctx, netResp.ID)
		m.logger.Error("restart: create container failed", "session", sessionID, "error", err)
		return
	}

	if err := m.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		_ = m.docker.NetworkRemove(ctx, netResp.ID)
		m.logger.Error("restart: start container failed", "session", sessionID, "error", err)
		return
	}

	baseURL, err := m.resolveBaseURL(ctx, resp.ID)
	if err != nil {
		_ = m.destroyContainer(ctx, resp.ID, netResp.ID)
		m.logger.Error("restart: resolve port failed", "session", sessionID, "error", err)
		return
	}

	if err := m.waitReady(ctx, baseURL); err != nil {
		_ = m.destroyContainer(ctx, resp.ID, netResp.ID)
		m.logger.Error("restart: wait ready failed", "session", sessionID, "error", err)
		return
	}

	newSb := &IXSandbox{
		id:           sessionID,
		containerID:  resp.ID,
		baseURL:      baseURL,
		client:       newClient(baseURL, &http.Client{Timeout: 30 * time.Second}),
		networkID:    netResp.ID,
		createdAt:    now,
		expiresAt:    now.Add(remainingTTL),
		restartCount: oldRestartCount + 1,
	}

	m.mu.Lock()
	m.sandboxes[sessionID] = newSb
	m.mu.Unlock()

	m.logger.Info("sandbox restarted",
		"session", sessionID,
		"container", resp.ID[:12],
		"restartCount", newSb.restartCount,
	)
}

// markFailed logs a circuit-breaker event and destroys the sandbox.
func (m *IXManager) markFailed(ctx context.Context, sessionID string) {
	m.logger.Error("circuit breaker: sandbox exceeded max restarts, destroying",
		"session", sessionID,
		"maxRestarts", m.cfg.MaxRestarts,
	)
	if err := m.destroy(ctx, sessionID); err != nil {
		m.logger.Warn("markFailed: destroy failed",
			"session", sessionID,
			"error", fmt.Errorf("destroy: %w", err),
		)
	}
}
