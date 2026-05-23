package memory

import (
	"testing"
	"time"
)

// TestPersistBackpressureTimeoutIsGenerous guards against regression to a
// too-aggressive timeout. The lightweight-persist path queues behind
// full-persist goroutines that run embedding calls; embedding providers
// commonly take 5-15s. A timeout under 10s silently drops user messages
// under normal load on slow providers.
func TestPersistBackpressureTimeoutIsGenerous(t *testing.T) {
	const floor = 10 * time.Second
	if persistBackpressureTimeout < floor {
		t.Fatalf("persistBackpressureTimeout = %v, want >= %v — too aggressive, will silently drop messages when embedding is slow",
			persistBackpressureTimeout, floor)
	}
}
