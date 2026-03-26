package ix

import (
	"context"
	"testing"
	"time"

	"github.com/nevindra/oasis/sandbox"
)

func TestDefaultManagerConfig(t *testing.T) {
	cfg := ManagerConfig{}
	cfg.applyDefaults()

	if cfg.Image != "oasis-ix:latest" {
		t.Errorf("Image = %q, want %q", cfg.Image, "oasis-ix:latest")
	}
	if cfg.DefaultTTL != time.Hour {
		t.Errorf("DefaultTTL = %v, want %v", cfg.DefaultTTL, time.Hour)
	}
	if cfg.PerSandbox.CPU != 1 {
		t.Errorf("PerSandbox.CPU = %d, want 1", cfg.PerSandbox.CPU)
	}
	if cfg.PerSandbox.Memory != 2<<30 {
		t.Errorf("PerSandbox.Memory = %d, want %d", cfg.PerSandbox.Memory, 2<<30)
	}
	if cfg.PerSandbox.Disk != 10<<30 {
		t.Errorf("PerSandbox.Disk = %d, want %d", cfg.PerSandbox.Disk, 10<<30)
	}
	if cfg.MaxRestarts != 3 {
		t.Errorf("MaxRestarts = %d, want 3", cfg.MaxRestarts)
	}
	if cfg.Logger == nil {
		t.Error("Logger should not be nil after applyDefaults")
	}
}

func TestDefaultManagerConfigPreservesExplicit(t *testing.T) {
	cfg := ManagerConfig{
		Image:      "custom-image:v1",
		DefaultTTL: 30 * time.Minute,
		PerSandbox: sandbox.ResourceSpec{
			CPU:    4,
			Memory: 8 << 30,
			Disk:   20 << 30,
		},
		MaxRestarts: 5,
	}
	cfg.applyDefaults()

	if cfg.Image != "custom-image:v1" {
		t.Errorf("Image = %q, want %q", cfg.Image, "custom-image:v1")
	}
	if cfg.DefaultTTL != 30*time.Minute {
		t.Errorf("DefaultTTL = %v, want %v", cfg.DefaultTTL, 30*time.Minute)
	}
	if cfg.PerSandbox.CPU != 4 {
		t.Errorf("PerSandbox.CPU = %d, want 4", cfg.PerSandbox.CPU)
	}
	if cfg.PerSandbox.Memory != 8<<30 {
		t.Errorf("PerSandbox.Memory = %d, want %d", cfg.PerSandbox.Memory, 8<<30)
	}
	if cfg.PerSandbox.Disk != 20<<30 {
		t.Errorf("PerSandbox.Disk = %d, want %d", cfg.PerSandbox.Disk, 20<<30)
	}
	if cfg.MaxRestarts != 5 {
		t.Errorf("MaxRestarts = %d, want 5", cfg.MaxRestarts)
	}
}

func TestResolveOpts(t *testing.T) {
	cfg := ManagerConfig{
		Image:      "default-image:latest",
		DefaultTTL: time.Hour,
		PerSandbox: sandbox.ResourceSpec{
			CPU:    2,
			Memory: 4 << 30,
			Disk:   10 << 30,
		},
	}
	m := &IXManager{cfg: cfg}

	t.Run("all defaults", func(t *testing.T) {
		resolved := m.resolveOpts(sandbox.CreateOpts{SessionID: "s1"})
		if resolved.Image != "default-image:latest" {
			t.Errorf("Image = %q, want %q", resolved.Image, "default-image:latest")
		}
		if resolved.TTL != time.Hour {
			t.Errorf("TTL = %v, want %v", resolved.TTL, time.Hour)
		}
		if resolved.Resources.CPU != 2 {
			t.Errorf("CPU = %d, want 2", resolved.Resources.CPU)
		}
		if resolved.Resources.Memory != 4<<30 {
			t.Errorf("Memory = %d, want %d", resolved.Resources.Memory, 4<<30)
		}
		if resolved.Resources.Disk != 10<<30 {
			t.Errorf("Disk = %d, want %d", resolved.Resources.Disk, 10<<30)
		}
	})

	t.Run("explicit overrides", func(t *testing.T) {
		resolved := m.resolveOpts(sandbox.CreateOpts{
			SessionID: "s2",
			Image:     "custom:v2",
			TTL:       30 * time.Minute,
			Resources: sandbox.ResourceSpec{
				CPU:    8,
				Memory: 16 << 30,
				Disk:   50 << 30,
			},
		})
		if resolved.Image != "custom:v2" {
			t.Errorf("Image = %q, want %q", resolved.Image, "custom:v2")
		}
		if resolved.TTL != 30*time.Minute {
			t.Errorf("TTL = %v, want %v", resolved.TTL, 30*time.Minute)
		}
		if resolved.Resources.CPU != 8 {
			t.Errorf("CPU = %d, want 8", resolved.Resources.CPU)
		}
		if resolved.Resources.Memory != 16<<30 {
			t.Errorf("Memory = %d, want %d", resolved.Resources.Memory, 16<<30)
		}
		if resolved.Resources.Disk != 50<<30 {
			t.Errorf("Disk = %d, want %d", resolved.Resources.Disk, 50<<30)
		}
	})

	t.Run("partial overrides", func(t *testing.T) {
		resolved := m.resolveOpts(sandbox.CreateOpts{
			SessionID: "s3",
			Image:     "partial:v1",
			// TTL and Resources left at zero - should use defaults.
		})
		if resolved.Image != "partial:v1" {
			t.Errorf("Image = %q, want %q", resolved.Image, "partial:v1")
		}
		if resolved.TTL != time.Hour {
			t.Errorf("TTL = %v, want %v", resolved.TTL, time.Hour)
		}
		if resolved.Resources.CPU != 2 {
			t.Errorf("CPU = %d, want 2", resolved.Resources.CPU)
		}
	})
}

func TestAcquireSlotAvailable(t *testing.T) {
	sem := make(chan struct{}, 2)
	ctx := context.Background()

	err := acquireSlot(ctx, sem, func() bool { return false }, time.Second)
	if err != nil {
		t.Fatalf("acquireSlot() returned error: %v", err)
	}

	// Slot should be occupied.
	if len(sem) != 1 {
		t.Errorf("semaphore length = %d, want 1", len(sem))
	}

	// Second slot should also be available.
	err = acquireSlot(ctx, sem, func() bool { return false }, time.Second)
	if err != nil {
		t.Fatalf("acquireSlot() second call returned error: %v", err)
	}
	if len(sem) != 2 {
		t.Errorf("semaphore length = %d, want 2", len(sem))
	}
}

func TestAcquireSlotFull(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // fill it up
	ctx := context.Background()

	err := acquireSlot(ctx, sem, func() bool { return false }, 50*time.Millisecond)
	if err != sandbox.ErrCapacityFull {
		t.Fatalf("acquireSlot() = %v, want ErrCapacityFull", err)
	}
}

func TestAcquireSlotEviction(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // fill it up
	ctx := context.Background()

	evicted := false
	err := acquireSlot(ctx, sem, func() bool {
		// Simulate evicting a sandbox: free the slot.
		<-sem
		evicted = true
		return true
	}, time.Second)

	if err != nil {
		t.Fatalf("acquireSlot() returned error: %v", err)
	}
	if !evicted {
		t.Error("expected eviction function to be called")
	}
}

func TestAcquireSlotContextCanceled(t *testing.T) {
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // fill it up

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := acquireSlot(ctx, sem, func() bool { return false }, 5*time.Second)
	if err != context.Canceled {
		t.Fatalf("acquireSlot() = %v, want context.Canceled", err)
	}
}

func TestAutoDetectMax(t *testing.T) {
	result := autoDetectMax(sandbox.ResourceSpec{
		CPU:    1,
		Memory: 2 << 30, // 2 GB
	})

	// Should return at least 1 on any machine.
	if result < 1 {
		t.Errorf("autoDetectMax() = %d, want >= 1", result)
	}

	// With very large resource requirements, should still return at least 1.
	result = autoDetectMax(sandbox.ResourceSpec{
		CPU:    1024,
		Memory: 1 << 40, // 1 TB
	})
	if result != 1 {
		t.Errorf("autoDetectMax() with huge resources = %d, want 1", result)
	}
}

func TestAutoDetectMaxZeroResources(t *testing.T) {
	// Zero values should not cause division by zero.
	result := autoDetectMax(sandbox.ResourceSpec{
		CPU:    0,
		Memory: 0,
	})
	if result < 1 {
		t.Errorf("autoDetectMax() with zero resources = %d, want >= 1", result)
	}
}
