package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMount is an in-memory FilesystemMount for testing.
type fakeMount struct {
	mu      sync.Mutex
	entries map[string]fakeEntry
}

type fakeEntry struct {
	data    []byte
	version string
	mime    string
	mtime   time.Time
}

func newFakeMount() *fakeMount {
	return &fakeMount{entries: make(map[string]fakeEntry)}
}

func (m *fakeMount) seed(key, content, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = fakeEntry{data: []byte(content), version: version, mtime: time.Now()}
}

func (m *fakeMount) List(ctx context.Context, prefix string) ([]MountEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []MountEntry
	for k, e := range m.entries {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		out = append(out, MountEntry{Key: k, Size: int64(len(e.data)), Version: e.version, MimeType: e.mime, Modified: e.mtime})
	}
	return out, nil
}

func (m *fakeMount) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, ErrKeyNotFound
	}
	return io.NopCloser(bytes.NewReader(e.data)), nil
}

func (m *fakeMount) Put(ctx context.Context, key, mime string, size int64, data io.Reader, ifVersion string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, exists := m.entries[key]
	if exists && ifVersion != "" && cur.version != ifVersion {
		return "", &VersionMismatchError{Key: key, Have: ifVersion, Want: cur.version}
	}
	if !exists && ifVersion != "" {
		return "", &VersionMismatchError{Key: key, Have: ifVersion, Want: ""}
	}
	body, _ := io.ReadAll(data)
	newVer := "v1"
	if exists {
		newVer = cur.version + "+1"
	}
	m.entries[key] = fakeEntry{data: body, version: newVer, mime: mime, mtime: time.Now()}
	return newVer, nil
}

func (m *fakeMount) Delete(ctx context.Context, key, ifVersion string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.entries[key]
	if !ok {
		return nil
	}
	if ifVersion != "" && cur.version != ifVersion {
		return &VersionMismatchError{Key: key, Have: ifVersion, Want: cur.version}
	}
	delete(m.entries, key)
	return nil
}

func (m *fakeMount) Stat(ctx context.Context, key string) (MountEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return MountEntry{}, ErrKeyNotFound
	}
	return MountEntry{Key: key, Size: int64(len(e.data)), Version: e.version, MimeType: e.mime, Modified: e.mtime}, nil
}

// recordingSandbox is a Sandbox impl that records UploadFile / DownloadFile
// calls and stores the data in memory. Other Sandbox methods panic via the
// embedded nil interface — only methods we override are safe to call.
type recordingSandbox struct {
	Sandbox // embed nil to satisfy interface; we override what we need
	mu      sync.Mutex
	files   map[string][]byte
}

func newRecordingSandbox() *recordingSandbox {
	return &recordingSandbox{files: make(map[string][]byte)}
}

func (s *recordingSandbox) UploadFile(ctx context.Context, path string, data io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	s.files[path] = body
	return nil
}

func (s *recordingSandbox) DownloadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.files[path]
	if !ok {
		return nil, errors.New("not found in sandbox")
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (s *recordingSandbox) GlobFiles(ctx context.Context, req GlobRequest) (GlobResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.files {
		if req.Path != "" && !strings.HasPrefix(p, req.Path) {
			continue
		}
		out = append(out, p)
	}
	return GlobResult{Files: out}, nil
}

func (s *recordingSandbox) WriteFile(ctx context.Context, req WriteFileRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[req.Path] = []byte(req.Content)
	return nil
}

func (s *recordingSandbox) EditFile(ctx context.Context, req EditFileRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.files[req.Path]
	if !ok {
		return errors.New("not found")
	}
	updated := strings.Replace(string(body), req.Old, req.New, 1)
	s.files[req.Path] = []byte(updated)
	return nil
}

func (s *recordingSandbox) Close() error { return nil }

// ── Tests ──

func TestPrefetchMountsCopiesFiles(t *testing.T) {
	mount := newFakeMount()
	mount.seed("data.csv", "id,value\n1,hi", "v1")
	mount.seed("notes.md", "# notes", "v1")

	sb := newRecordingSandbox()
	specs := []MountSpec{{
		Path:            "/workspace/inputs",
		Backend:         mount,
		Mode:            MountReadOnly,
		PrefetchOnStart: true,
	}}

	manifest := NewManifest()
	if err := PrefetchMounts(context.Background(), sb, specs, manifest); err != nil {
		t.Fatalf("PrefetchMounts: %v", err)
	}

	if string(sb.files["/workspace/inputs/data.csv"]) != "id,value\n1,hi" {
		t.Errorf("data.csv content wrong: %q", sb.files["/workspace/inputs/data.csv"])
	}
	if string(sb.files["/workspace/inputs/notes.md"]) != "# notes" {
		t.Errorf("notes.md content wrong")
	}
	if v, _ := manifest.Version("/workspace/inputs", "data.csv"); v != "v1" {
		t.Errorf("manifest data.csv version = %q, want v1", v)
	}
}

func TestPrefetchMountsSkipsWriteOnlyMounts(t *testing.T) {
	mount := newFakeMount()
	mount.seed("anything", "should not be fetched", "v1")

	sb := newRecordingSandbox()
	specs := []MountSpec{{
		Path:            "/workspace/output",
		Backend:         mount,
		Mode:            MountWriteOnly,
		PrefetchOnStart: true, // should be ignored for write-only
	}}

	if err := PrefetchMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("PrefetchMounts: %v", err)
	}
	if len(sb.files) != 0 {
		t.Errorf("write-only mount should not prefetch, got %d files", len(sb.files))
	}
}

func TestPrefetchMountsSkipsWhenPrefetchOnStartFalse(t *testing.T) {
	mount := newFakeMount()
	mount.seed("data.csv", "x", "v1")

	sb := newRecordingSandbox()
	specs := []MountSpec{{
		Path:            "/workspace/inputs",
		Backend:         mount,
		Mode:            MountReadWrite,
		PrefetchOnStart: false,
	}}

	if err := PrefetchMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("PrefetchMounts: %v", err)
	}
	if len(sb.files) != 0 {
		t.Errorf("PrefetchOnStart=false should not prefetch, got %d files", len(sb.files))
	}
}

func TestPrefetchMountsHonorsExclude(t *testing.T) {
	mount := newFakeMount()
	mount.seed("keep.csv", "data", "v1")
	mount.seed("temp.tmp", "junk", "v1")

	sb := newRecordingSandbox()
	specs := []MountSpec{{
		Path:            "/workspace/inputs",
		Backend:         mount,
		Mode:            MountReadOnly,
		PrefetchOnStart: true,
		Exclude:         []string{"*.tmp"},
	}}

	if err := PrefetchMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("PrefetchMounts: %v", err)
	}
	if _, ok := sb.files["/workspace/inputs/keep.csv"]; !ok {
		t.Error("keep.csv missing")
	}
	if _, ok := sb.files["/workspace/inputs/temp.tmp"]; ok {
		t.Error("temp.tmp should be excluded")
	}
}
