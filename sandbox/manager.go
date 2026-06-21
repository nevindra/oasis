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

	// Health returns a passive snapshot of runtime readiness and pool state.
	// It MUST NOT launch or mutate sandboxes, and MUST NOT return an error for
	// a known-bad condition (missing file, KVM absent) — those are reported via
	// the *OK fields. It returns an error only if the snapshot itself cannot be
	// taken (e.g. ctx cancelled).
	Health(ctx context.Context) (Health, error)
}

// CreateOpts configures a new sandbox.
type CreateOpts struct {
	SessionID string            // conversation/session identifier (required)
	Image     string            // container image; empty uses manager default
	TTL       time.Duration     // sandbox lifetime; 0 uses manager default
	Resources ResourceSpec      // per-sandbox resource limits; zero values use defaults
	Env       map[string]string // additional env vars injected into the container

	// Browser declares whether this sandbox needs browser capability.
	// nil  = manager default (typically browser via shared tier);
	// true = ensure browser; false = no browser ("light" sandbox).
	// Implementations that have no browser concept may ignore it.
	Browser *bool
}

// ResourceSpec defines per-sandbox resource limits.
type ResourceSpec struct {
	CPU    int   // number of CPU cores; 0 uses default (1)
	Memory int64 // bytes; 0 uses default (2GB)
	Disk   int64 // bytes; 0 uses default (10GB)
}

// Health is a passive snapshot of sandbox-runtime readiness and pool state.
type Health struct {
	Ready    bool         `json:"ready"`    // runtime usable end-to-end (all Runtime *OK fields true)
	Runtime  RuntimeInfo  `json:"runtime"`  // "installed correctly"
	Pool     PoolStats    `json:"pool"`     // "connected / warm"
	Egress   EgressInfo   `json:"egress"`   // default egress policy in effect
	Snapshot SnapshotInfo `json:"snapshot"` // golden-snapshot configuration & readiness
}

// RuntimeInfo reports whether the host pieces needed to launch a VM are present.
type RuntimeInfo struct {
	Backend       string `json:"backend"` // e.g. "firecracker"
	KernelPath    string `json:"kernel_path"`
	KernelOK      bool   `json:"kernel_ok"` // kernel file exists & is readable
	RootfsImage   string `json:"rootfs_image"`
	RootfsOK      bool   `json:"rootfs_ok"`      // rootfs file exists & is readable
	FCBinary      string `json:"fc_binary"`      // resolved firecracker binary path
	FCBinaryOK    bool   `json:"fc_binary_ok"`   // binary exists & is executable
	KVMAccessible bool   `json:"kvm_accessible"` // /dev/kvm present & openable
}

// PoolStats reports pre-warmed pool occupancy and lifetime fault counters.
type PoolStats struct {
	Configured int `json:"configured"` // target pool size
	Ready      int `json:"ready"`      // warm/idle VMs ready to claim
	Active     int `json:"active"`     // sandboxes currently in use
	Failed     int `json:"failed"`     // cumulative circuit-breaker trips
	Restarts   int `json:"restarts"`   // cumulative monitor-driven restarts
}

// EgressInfo reports the default egress policy applied to new sandboxes.
type EgressInfo struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"` // "allow" | "deny" | ""
	RuleCount int    `json:"rule_count"`
}

// SnapshotInfo reports golden-snapshot configuration and readiness.
type SnapshotInfo struct {
	Enabled bool `json:"enabled"` // UseSnapshot configured
	Ready   bool `json:"ready"`   // golden snapshot built/loaded
}
