package agent

import (
	"context"
	"sync"
)

// defaultIterChBufSize is the per-iteration StreamEvent forwarder buffer.
// Holding at 64 (the pre-Phase-4 value) until a real-workload measurement
// justifies a reduction. Phase 4 finding 4.1.a originally proposed dropping
// this to 16-32 based on observed fill, but that decision needs deployment
// telemetry (e.g. instrumenting `cmd/bot_example` against live LLM streaming),
// which is out of reach in current dev environments. BenchmarkIterChStreaming
// in loop_bench_test.go is the regression guard for any future change.
const defaultIterChBufSize = 64

// newStreamForwarder creates an intermediate StreamEvent channel and spawns a
// goroutine that forwards events from it to dest until the channel is closed
// by the producer (typically provider.ChatStream's defer-close) or ctx is
// cancelled.
//
// Returns (iterCh, wait). Callers pass iterCh to the producer (provider) and
// MUST call wait() after the producer returns to ensure the forwarder
// finishes drain-and-exit before subsequent code observes dest.
//
// The forwarder pattern centralizes three previously duplicated sites in
// runLoop and ensures the producer's defer-close doesn't touch dest directly
// (the caller's safeCloseCh is the sole closer of dest).
func newStreamForwarder(ctx context.Context, dest chan<- StreamEvent, bufSize int) (chan<- StreamEvent, func()) {
	iterCh := make(chan StreamEvent, bufSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range iterCh {
			select {
			case dest <- ev:
			case <-ctx.Done():
				// Drain remaining events so the producer can close iterCh.
				for range iterCh {
				}
				return
			}
		}
	}()
	return iterCh, wg.Wait
}
