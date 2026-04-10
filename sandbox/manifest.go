package sandbox

import "sync"

// Manifest tracks the backend version of every file the framework has
// prefetched into a sandbox. It is used by Layer 2 (tool interception)
// and Layer 3 (lifecycle flush) to send the correct precondition on
// writes back to the backend.
//
// Manifest is safe for concurrent use.
type Manifest struct {
	mu      sync.RWMutex
	entries map[string]map[string]MountEntry // mountPath → key → entry
}

// NewManifest returns an empty manifest.
func NewManifest() *Manifest {
	return &Manifest{entries: make(map[string]map[string]MountEntry)}
}

// Record stores or updates the entry for a key under the given mount path.
func (m *Manifest) Record(mountPath, key string, entry MountEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mp, ok := m.entries[mountPath]
	if !ok {
		mp = make(map[string]MountEntry)
		m.entries[mountPath] = mp
	}
	mp[key] = entry
}

// Version returns the recorded version for a key, if any.
func (m *Manifest) Version(mountPath, key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mp, ok := m.entries[mountPath]
	if !ok {
		return "", false
	}
	e, ok := mp[key]
	if !ok {
		return "", false
	}
	return e.Version, true
}

// Lookup returns the full entry for a key, if any.
func (m *Manifest) Lookup(mountPath, key string) (MountEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mp, ok := m.entries[mountPath]
	if !ok {
		return MountEntry{}, false
	}
	e, ok := mp[key]
	return e, ok
}

// Forget removes a key from the manifest. Used after a delete or when
// the framework decides a previously-tracked file is gone.
func (m *Manifest) Forget(mountPath, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mp, ok := m.entries[mountPath]; ok {
		delete(mp, key)
	}
}

// Keys returns all known keys for a mount path. Used by FlushMounts to
// detect locally-deleted files when MirrorDeletes is true.
func (m *Manifest) Keys(mountPath string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mp, ok := m.entries[mountPath]
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(mp))
	for k := range mp {
		keys = append(keys, k)
	}
	return keys
}
