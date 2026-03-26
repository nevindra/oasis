package ixd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// sseWriter serializes Server-Sent Events to an http.ResponseWriter.
// All writes are mutex-protected so concurrent goroutines (stdout, stderr,
// keep-alive) can safely send events without interleaving.
type sseWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
	mu sync.Mutex
}

// newSSEWriter sets SSE response headers and returns a writer.
// Returns nil if the ResponseWriter does not implement http.Flusher.
func newSSEWriter(w http.ResponseWriter) *sseWriter {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fl.Flush()
	return &sseWriter{w: w, fl: fl}
}

// Send writes a single SSE event. data is JSON-marshaled before writing.
func (s *sseWriter) Send(event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, b)
	if err != nil {
		return err
	}
	s.fl.Flush()
	return nil
}

// Ping writes an SSE comment to keep the connection alive.
// Per SSE spec, lines starting with ':' are comments that clients ignore.
func (s *sseWriter) Ping() {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.w, ": ping\n\n")
	s.fl.Flush()
}

// keepAlive sends periodic pings until ctx is cancelled.
// Must be run as a goroutine. Shuts down cleanly on context cancellation.
func (s *sseWriter) keepAlive(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Ping()
		}
	}
}
