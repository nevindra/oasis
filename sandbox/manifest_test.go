package sandbox

import (
	"testing"
	"time"
)

func TestManifestRecordAndVersion(t *testing.T) {
	m := NewManifest()
	m.Record("/workspace/inputs", "data.csv", MountEntry{
		Key:      "data.csv",
		Size:     1024,
		Version:  "etag-1",
		Modified: time.Now(),
	})

	v, ok := m.Version("/workspace/inputs", "data.csv")
	if !ok {
		t.Fatal("Version after Record returned ok=false")
	}
	if v != "etag-1" {
		t.Fatalf("Version = %q, want %q", v, "etag-1")
	}

	if _, ok := m.Version("/workspace/inputs", "missing.csv"); ok {
		t.Fatal("Version on missing key returned ok=true")
	}
}

func TestManifestUpdateOverwrites(t *testing.T) {
	m := NewManifest()
	m.Record("/m", "f", MountEntry{Key: "f", Version: "v1"})
	m.Record("/m", "f", MountEntry{Key: "f", Version: "v2"})

	v, _ := m.Version("/m", "f")
	if v != "v2" {
		t.Fatalf("Version = %q, want %q", v, "v2")
	}
}

func TestManifestForget(t *testing.T) {
	m := NewManifest()
	m.Record("/m", "f", MountEntry{Key: "f", Version: "v1"})
	m.Forget("/m", "f")

	if _, ok := m.Version("/m", "f"); ok {
		t.Fatal("Version after Forget returned ok=true")
	}
}

func TestManifestKeysIsolatesByMountPath(t *testing.T) {
	m := NewManifest()
	m.Record("/a", "shared.md", MountEntry{Key: "shared.md", Version: "vA"})
	m.Record("/b", "shared.md", MountEntry{Key: "shared.md", Version: "vB"})

	if v, _ := m.Version("/a", "shared.md"); v != "vA" {
		t.Errorf("Version(/a) = %q, want vA", v)
	}
	if v, _ := m.Version("/b", "shared.md"); v != "vB" {
		t.Errorf("Version(/b) = %q, want vB", v)
	}
}

func TestManifestKeysReturnsAllUnderMountPath(t *testing.T) {
	m := NewManifest()
	m.Record("/m", "a", MountEntry{Key: "a", Version: "v1"})
	m.Record("/m", "b", MountEntry{Key: "b", Version: "v1"})
	m.Record("/n", "c", MountEntry{Key: "c", Version: "v1"})

	keys := m.Keys("/m")
	if len(keys) != 2 {
		t.Fatalf("Keys(/m) returned %d entries, want 2: %v", len(keys), keys)
	}
	seen := map[string]bool{}
	for _, k := range keys {
		seen[k] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Errorf("Keys(/m) missing entries: %v", keys)
	}
}

func TestManifestLookupReturnsFullEntry(t *testing.T) {
	m := NewManifest()
	entry := MountEntry{Key: "f", Size: 42, MimeType: "text/plain", Version: "v1"}
	m.Record("/m", "f", entry)

	got, ok := m.Lookup("/m", "f")
	if !ok {
		t.Fatal("Lookup returned ok=false")
	}
	if got.Size != 42 || got.MimeType != "text/plain" || got.Version != "v1" {
		t.Errorf("Lookup = %+v", got)
	}
}
