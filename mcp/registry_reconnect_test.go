package mcp

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestReconnect_BackoffReset_NoRace asserts that Registry.Reconnect resets the
// backoff counter under the entry's reconnectMu, so it cannot race a
// reconnectLoop that is concurrently reading/writing backoff.attempts.
//
// Run with -race: an unsynchronized reset in Reconnect (the bug) is flagged by
// the race detector against the loop's guarded access to backoff.attempts.
func TestReconnect_BackoffReset_NoRace(t *testing.T) {
	r := NewRegistry(WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))

	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A server entry whose reconnect attempts always fail (nil cfg => buildClient
	// errors), so reconnectLoop loops, sleeping in nextBackoff between attempts
	// and continuously touching backoff.attempts under reconnectMu.
	entry := &serverEntry{
		cfg:       StdioConfig{Name: "racer"}, // buildClient succeeds but Initialize fails fast
		tools:     make(map[string]*toolEntry),
		backoff:   &backoffState{},
		parentCtx: parentCtx,
		parent:    r,
	}
	entry.state.Store(int32(StateReconnecting))

	r.mu.Lock()
	r.servers["racer"] = entry
	r.mu.Unlock()

	// Drive an active reconnectLoop in the background.
	loopDone := make(chan struct{})
	go func() {
		entry.reconnectLoop()
		close(loopDone)
	}()

	// Hammer Reconnect concurrently while the loop is alive. Each call writes
	// backoff.attempts; if unguarded, it races the running loop.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case <-deadline:
			cancel() // let the loops observe parentCtx.Done and exit
			<-loopDone
			return
		default:
			if err := r.Reconnect(context.Background(), "racer"); err != nil {
				t.Fatalf("Reconnect: %v", err)
			}
		}
	}
}
