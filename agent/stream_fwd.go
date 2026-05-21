package agent

import (
	"context"
	"encoding/json"
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
// newFileCapturingSink creates an intermediate StreamEvent channel for tool
// dispatch. Events are forwarded to dest; EventFileAttachment events are also
// decoded and appended to state.files so that AgentResult.Files is populated.
//
// Returns (sinkCh, wait). The caller MUST close sinkCh after all tools have
// finished writing, then call wait() to ensure the forwarder has drained.
//
// When dest is nil (non-streaming Execute path), returns (nil, func(){}) so
// contextWithStreamSink can safely receive nil and skip sink registration.
func newFileCapturingSink(ctx context.Context, dest chan<- StreamEvent, state *loopState) (chan StreamEvent, func()) {
	if dest == nil {
		return nil, func() {}
	}
	sinkCh := make(chan StreamEvent, defaultIterChBufSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range sinkCh {
			// Task 3.4: Capture file attachments into state.files.
			captureFileEvent(ev, state)
			select {
			case dest <- ev:
			case <-ctx.Done():
				// Drain so tools can finish writing.
				for range sinkCh {
				}
				return
			}
		}
	}()
	return sinkCh, wg.Wait
}

// newCapturingStreamForwarder is like newStreamForwarder but also captures
// EventFileAttachment events into state.files. Used for provider streaming paths
// where the provider may emit EventFileAttachment alongside text deltas.
func newCapturingStreamForwarder(ctx context.Context, dest chan<- StreamEvent, bufSize int, state *loopState) (chan<- StreamEvent, func()) {
	iterCh := make(chan StreamEvent, bufSize)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ev := range iterCh {
			// Task 3.4: Capture file attachments into state.files.
			captureFileEvent(ev, state)
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

// captureFileEvent checks whether ev is an EventFileAttachment and, if so,
// decodes the file metadata from ev.Content and appends it to state.files.
// The event Content carries a JSON object: {"name":"...","mime_type":"...","size":N,"url":"..."}.
func captureFileEvent(ev StreamEvent, state *loopState) {
	if ev.Type != EventFileAttachment {
		return
	}
	var att Attachment
	if err := json.Unmarshal([]byte(ev.Content), &att); err == nil {
		state.files = append(state.files, att)
	}
}

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
