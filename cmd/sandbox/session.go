package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sessionEntry records the workspace directory and last access time.
type sessionEntry struct {
	dir        string
	lastAccess time.Time
}

// sessionManager creates, reuses, and evicts per-session workspace directories.
// All exported methods are safe for concurrent use.
type sessionManager struct {
	root     string
	ttl      time.Duration
	mu       sync.Mutex
	sessions map[string]*sessionEntry
	stopCh   chan struct{}
	doneCh   chan struct{}
}

func newSessionManager(root string, ttl time.Duration) *sessionManager {
	return &sessionManager{
		root:     root,
		ttl:      ttl,
		sessions: make(map[string]*sessionEntry),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// start launches the background cleanup goroutine.
func (m *sessionManager) start(interval time.Duration) {
	go m.runCleanup(interval)
}

// get returns the workspace directory for sessionID, creating it if necessary.
func (m *sessionManager) get(sessionID string) (string, error) {
	safe := filepath.Base(sessionID)
	if safe == "." || safe == "" {
		return "", fmt.Errorf("invalid session_id: %q", sessionID)
	}

	m.mu.Lock()
	entry, ok := m.sessions[safe]
	if !ok {
		dir := filepath.Join(m.root, safe)
		entry = &sessionEntry{dir: dir}
		m.sessions[safe] = entry
	}
	entry.lastAccess = time.Now()
	dir := entry.dir
	m.mu.Unlock()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create workspace %q: %w", dir, err)
	}
	return dir, nil
}

// delete removes the session entry and its workspace directory.
func (m *sessionManager) delete(sessionID string) error {
	safe := filepath.Base(sessionID)

	m.mu.Lock()
	entry, ok := m.sessions[safe]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	dir := entry.dir
	delete(m.sessions, safe)
	m.mu.Unlock()

	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove workspace %q: %w", dir, err)
	}
	return nil
}

// close stops the cleanup goroutine and waits for it to exit.
func (m *sessionManager) close() {
	close(m.stopCh)
	<-m.doneCh
}

// runCleanup runs the TTL eviction loop until stopCh is closed.
func (m *sessionManager) runCleanup(interval time.Duration) {
	defer close(m.doneCh)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.evictExpired()
		case <-m.stopCh:
			return
		}
	}
}

// evictExpired removes sessions whose last access exceeds the TTL.
// Removes from the map atomically under lock, then cleans up directories
// outside the lock to avoid holding it during disk I/O.
func (m *sessionManager) evictExpired() {
	m.mu.Lock()
	var dirs []string
	for id, entry := range m.sessions {
		if time.Since(entry.lastAccess) > m.ttl {
			dirs = append(dirs, entry.dir)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()

	for _, dir := range dirs {
		os.RemoveAll(dir)
	}
}
