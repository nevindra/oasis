package sandbox

import (
	"context"
	"time"
)

// Manager manages sandbox container lifecycle. It is owned by the platform
// layer (e.g., Orchestrator), not by agents. Agents receive a Sandbox
// instance via dependency injection.
type Manager interface {
	// Create provisions a new sandbox container. Blocks until the sandbox is
	// ready for use (health check passes). Returns ErrCapacityFull if the
	// concurrency limit is reached and no idle sandbox can be evicted.
	Create(ctx context.Context, opts CreateOpts) (Sandbox, error)

	// Get retrieves an existing sandbox by session ID. Returns ErrNotFound
	// if the sandbox does not exist or has been destroyed.
	Get(sessionID string) (Sandbox, error)

	// Destroy stops and removes a sandbox by session ID. Returns ErrNotFound
	// if the sandbox does not exist. Use this for explicit cleanup (e.g.,
	// when a conversation is deleted).
	Destroy(ctx context.Context, sessionID string) error

	// Shutdown stops accepting new sandboxes and waits for in-flight work
	// to drain. Containers are kept alive for recovery on next boot.
	Shutdown(ctx context.Context) error

	// Close force-destroys all managed sandboxes and their networks.
	Close() error
}

// CreateOpts configures a new sandbox.
type CreateOpts struct {
	SessionID string            // conversation/session identifier (required)
	Image     string            // container image; empty uses manager default
	TTL       time.Duration     // sandbox lifetime; 0 uses manager default
	Resources ResourceSpec      // per-sandbox resource limits; zero values use defaults
	Env       map[string]string // additional env vars injected into the container
}

// ResourceSpec defines per-sandbox resource limits.
type ResourceSpec struct {
	CPU    int   // number of CPU cores; 0 uses default (1)
	Memory int64 // bytes; 0 uses default (2GB)
	Disk   int64 // bytes; 0 uses default (10GB)
}
