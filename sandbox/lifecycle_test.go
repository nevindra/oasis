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

	// dirs are paths the guest's glob reports but that cannot be downloaded —
	// directories, which a real guest returns with a trailing slash. Kept out
	// of files on purpose so DownloadFile fails on them exactly as it does in
	// the VM, rather than quietly succeeding with empty bytes.
	dirs []string
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
	for _, p := range s.dirs {
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

func TestFlushMountsPublishesNewFiles(t *testing.T) {
	mount := newFakeMount()
	sb := newRecordingSandbox()
	sb.files["/workspace/output/report.md"] = []byte("hello")

	specs := []MountSpec{{
		Path:         "/workspace/output",
		Backend:      mount,
		Mode:         MountReadWrite,
		FlushOnClose: true,
	}}

	if err := FlushMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if string(mount.entries["report.md"].data) != "hello" {
		t.Errorf("backend report.md = %q, want %q", mount.entries["report.md"].data, "hello")
	}
}

// A directory under the mount root must not make the whole flush report an
// error. Found against a live VM: the guest's glob returns directories with a
// trailing slash, flushOne tried to download one, and because FlushMounts
// aggregates every error it hits, a single `mkdir` under /workspace made the
// flush return non-nil on effectively every turn. Nothing corrupt landed — the
// download fails before the Put — but the caller logs that as "flush mounts
// failed", so a real flush failure was indistinguishable from an agent making
// a folder.
func TestFlushMountsIgnoresDirectoryEntriesFromTheGuestGlob(t *testing.T) {
	mount := newFakeMount()
	sb := newRecordingSandbox()
	sb.files["/workspace/output/data/inner.csv"] = []byte("a,b")
	sb.dirs = []string{"/workspace/output/data/"}

	specs := []MountSpec{{
		Path:         "/workspace/output",
		Backend:      mount,
		Mode:         MountReadWrite,
		FlushOnClose: true,
	}}

	if err := FlushMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if string(mount.entries["data/inner.csv"].data) != "a,b" {
		t.Errorf("backend data/inner.csv = %q, want %q", mount.entries["data/inner.csv"].data, "a,b")
	}
	if _, ok := mount.entries["data/"]; ok {
		t.Error("backend holds a key for the directory itself")
	}
}

func TestFlushMountsPublishesModifiedFiles(t *testing.T) {
	mount := newFakeMount()
	mount.seed("notes.md", "old", "v1")
	sb := newRecordingSandbox()
	sb.files["/workspace/output/notes.md"] = []byte("new")

	manifest := NewManifest()
	manifest.Record("/workspace/output", "notes.md", MountEntry{Key: "notes.md", Version: "v1"})

	specs := []MountSpec{{
		Path:         "/workspace/output",
		Backend:      mount,
		Mode:         MountReadWrite,
		FlushOnClose: true,
	}}

	if err := FlushMounts(context.Background(), sb, specs, manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if string(mount.entries["notes.md"].data) != "new" {
		t.Errorf("backend notes.md = %q, want %q", mount.entries["notes.md"].data, "new")
	}
}

func TestFlushMountsConflictReturnsError(t *testing.T) {
	mount := newFakeMount()
	mount.seed("notes.md", "remote-changed", "v2")
	sb := newRecordingSandbox()
	sb.files["/workspace/output/notes.md"] = []byte("local")

	manifest := NewManifest()
	manifest.Record("/workspace/output", "notes.md", MountEntry{Key: "notes.md", Version: "v1"})

	specs := []MountSpec{{
		Path:         "/workspace/output",
		Backend:      mount,
		Mode:         MountReadWrite,
		FlushOnClose: true,
	}}

	err := FlushMounts(context.Background(), sb, specs, manifest)
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("error = %v, want ErrVersionMismatch", err)
	}
	if string(mount.entries["notes.md"].data) != "remote-changed" {
		t.Error("backend should be unchanged after rejected put")
	}
}

func TestFlushMountsNoMirrorDeletesByDefault(t *testing.T) {
	mount := newFakeMount()
	mount.seed("stale.md", "still here", "v1")
	sb := newRecordingSandbox()

	manifest := NewManifest()
	manifest.Record("/workspace/output", "stale.md", MountEntry{Key: "stale.md", Version: "v1"})

	specs := []MountSpec{{
		Path:         "/workspace/output",
		Backend:      mount,
		Mode:         MountReadWrite,
		FlushOnClose: true,
	}}

	if err := FlushMounts(context.Background(), sb, specs, manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if _, ok := mount.entries["stale.md"]; !ok {
		t.Error("stale.md should NOT be deleted with MirrorDeletes=false")
	}
}

func TestFlushMountsMirrorDeletes(t *testing.T) {
	mount := newFakeMount()
	mount.seed("gone.md", "x", "v1")
	sb := newRecordingSandbox()

	manifest := NewManifest()
	manifest.Record("/workspace/output", "gone.md", MountEntry{Key: "gone.md", Version: "v1"})

	specs := []MountSpec{{
		Path:          "/workspace/output",
		Backend:       mount,
		Mode:          MountReadWrite,
		FlushOnClose:  true,
		MirrorDeletes: true,
	}}

	if err := FlushMounts(context.Background(), sb, specs, manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if _, ok := mount.entries["gone.md"]; ok {
		t.Error("gone.md should be deleted from backend")
	}
}

func TestFlushMountsSkipsReadOnly(t *testing.T) {
	mount := newFakeMount()
	sb := newRecordingSandbox()
	sb.files["/workspace/inputs/extra.md"] = []byte("local-only")

	specs := []MountSpec{{
		Path:         "/workspace/inputs",
		Backend:      mount,
		Mode:         MountReadOnly,
		FlushOnClose: true, // ignored for read-only
	}}

	if err := FlushMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if len(mount.entries) != 0 {
		t.Errorf("read-only mount should not flush, backend has %d entries", len(mount.entries))
	}
}

// ── filter harness ──

// putRecordingMount is a fakeMount that remembers which keys were Put, so a
// test can assert a Put did not happen. The backend state alone cannot show
// that: a skipped Put and a Put of identical bytes leave the same content
// behind, and only the recorded call separates them.
type putRecordingMount struct {
	*fakeMount
	recMu sync.Mutex
	puts  []string
}

func newPutRecordingMount() *putRecordingMount {
	return &putRecordingMount{fakeMount: newFakeMount()}
}

func (m *putRecordingMount) Put(ctx context.Context, key, mime string, size int64, data io.Reader, ifVersion string) (string, error) {
	m.recMu.Lock()
	m.puts = append(m.puts, key)
	m.recMu.Unlock()
	return m.fakeMount.Put(ctx, key, mime, size, data, ifVersion)
}

func (m *putRecordingMount) putKeys() []string {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	return append([]string(nil), m.puts...)
}

// downloadRecordingSandbox records every DownloadFile. It deliberately does
// not implement FileHasher — it is the "guest that cannot hash" half of the
// fallback tests.
type downloadRecordingSandbox struct {
	*recordingSandbox
	recMu     sync.Mutex
	downloads []string
}

func newDownloadRecordingSandbox() *downloadRecordingSandbox {
	return &downloadRecordingSandbox{recordingSandbox: newRecordingSandbox()}
}

func (s *downloadRecordingSandbox) DownloadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	s.recMu.Lock()
	s.downloads = append(s.downloads, path)
	s.recMu.Unlock()
	return s.recordingSandbox.DownloadFile(ctx, path)
}

func (s *downloadRecordingSandbox) downloadedPaths() []string {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	return append([]string(nil), s.downloads...)
}

// guestHashingSandbox is the same sandbox with FileHasher bolted on: it hashes
// its own in-memory files, minus the ones in omit (which stand for paths the
// guest could not read) and short-circuited by err (a guest that cannot hash
// at all).
type guestHashingSandbox struct {
	*downloadRecordingSandbox
	err       error
	omit      map[string]bool
	hashCalls int
	hashed    []string
}

func newGuestHashingSandbox() *guestHashingSandbox {
	return &guestHashingSandbox{downloadRecordingSandbox: newDownloadRecordingSandbox()}
}

func (s *guestHashingSandbox) HashFiles(ctx context.Context, paths []string) ([]FileHash, error) {
	s.hashCalls++
	s.hashed = append(s.hashed, paths...)
	if s.err != nil {
		return nil, s.err
	}
	var out []FileHash
	for _, p := range paths {
		if s.omit[p] {
			continue
		}
		body, ok := s.files[p]
		if !ok {
			continue
		}
		out = append(out, FileHash{Path: p, Digest: contentDigest(body), Size: int64(len(body))})
	}
	return out, nil
}

// ── exclude globs ──

// TestExcludeGlobsAthenaConfigures pins the five patterns athena's workspace
// mount is configured with, by name. Three of them (the ones with "**")
// matched nothing at all under filepath.Match, so every flush walked and
// republished the dependency trees and caches the mount believed it excluded.
func TestExcludeGlobsAthenaConfigures(t *testing.T) {
	cases := []struct {
		pattern    string
		excluded   string
		kept       string
		keptReason string
	}{
		{"*.tmp", "build/scratch.tmp", "notes/tmp.md", "the suffix, not the name"},
		{"*.swp", "docs/.notes.md.swp", "swap.md", "a name that merely starts the same"},
		{"**/__pycache__/**", "a/b/__pycache__/x.pyc", "a/b/pycache/x.pyc", "the directory is spelled differently"},
		{"**/.cache/**", "app/.cache/blob", "app/cache/blob", "no leading dot"},
		{"node_modules/**", "node_modules/pkg/index.js", "src/node_modules_notes.md", "a file about node_modules is not in it"},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			if matchFilters(tc.excluded, nil, []string{tc.pattern}) {
				t.Errorf("exclude %q did not exclude %q", tc.pattern, tc.excluded)
			}
			if !matchFilters(tc.kept, nil, []string{tc.pattern}) {
				t.Errorf("exclude %q wrongly excluded %q (%s)", tc.pattern, tc.kept, tc.keptReason)
			}
		})
	}
}

// TestExcludeGlobsSpanPathSegments covers the depths the athena patterns have
// to reach beyond the single example each gets above: "**" is only useful if
// it matches any number of segments, including none.
func TestExcludeGlobsSpanPathSegments(t *testing.T) {
	for _, key := range []string{
		"node_modules/index.js",
		"node_modules/pkg/index.js",
		"node_modules/@scope/pkg/dist/deep/index.js",
	} {
		if matchFilters(key, nil, []string{"node_modules/**"}) {
			t.Errorf("node_modules/** did not exclude %q", key)
		}
	}
	for _, key := range []string{
		".cache/blob",            // "**/" matching zero leading segments
		"app/.cache/blob",        //
		"a/b/c/.cache/d/e/f.bin", // segments on both sides
	} {
		if matchFilters(key, nil, []string{"**/.cache/**"}) {
			t.Errorf("**/.cache/** did not exclude %q", key)
		}
	}
}

// TestGlobMatchesBasenameAsWellAsKey guards the two-way test matchFilters
// documents. "*.tmp" is a suffix rule as everyone writing one intends it, not
// a rule about files in the mount root, and mounts in the wild rely on that.
func TestGlobMatchesBasenameAsWellAsKey(t *testing.T) {
	if !globMatches("*.tmp", "sub/dir/file.tmp") {
		t.Error(`"*.tmp" must match "sub/dir/file.tmp" through the basename test`)
	}
	if !globMatches("*.tmp", "file.tmp") {
		t.Error(`"*.tmp" must match a key in the mount root`)
	}
	if globMatches("*.tmp", "sub/dir/file.md") {
		t.Error(`"*.tmp" must not match a key with another suffix`)
	}
}

// TestInvalidGlobMatchesNothing pins which way a malformed pattern fails.
// Matching everything is the dangerous direction: as an exclude it would
// swallow the whole mount and silently drop every file the caller asked to
// publish. Matching nothing costs an over-eager flush instead — visible, and
// recoverable.
func TestInvalidGlobMatchesNothing(t *testing.T) {
	const bad = "[unterminated"
	if globMatches(bad, "anything.txt") {
		t.Errorf("malformed pattern %q matched a key", bad)
	}
	if !matchFilters("anything.txt", nil, []string{bad}) {
		t.Errorf("malformed exclude %q dropped a file; an unparseable pattern must not exclude", bad)
	}
	if matchFilters("anything.txt", []string{bad}, nil) {
		t.Errorf("malformed include %q let a file through; it must let nothing through", bad)
	}
}

// ── the close-time skip ──

// TestFlushSkipsPutWhenManifestVersionIsTheContentHash is the whole point of
// the strict rule: a content-addressed backend reports the content hash as the
// version, so a manifest version equal to the file's sha256 is proof the bytes
// are already there. Publishing them again would mint a new pointer and a new
// history entry for a change nobody made.
func TestFlushSkipsPutWhenManifestVersionIsTheContentHash(t *testing.T) {
	const body = "untouched by the agent"
	digest := contentDigest([]byte(body))

	mount := newPutRecordingMount()
	mount.seed("report.md", body, digest)
	sb := newRecordingSandbox()
	sb.files["/workspace/output/report.md"] = []byte(body)

	manifest := NewManifest()
	manifest.Record("/workspace/output", "report.md", MountEntry{Key: "report.md", Size: int64(len(body)), Version: digest})

	if err := FlushMounts(context.Background(), sb, flushSpec(mount), manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if got := mount.putKeys(); len(got) != 0 {
		t.Errorf("flush issued Puts %v for a file the backend already holds", got)
	}
	if v, _ := manifest.Version("/workspace/output", "report.md"); v != digest {
		t.Errorf("manifest version = %q, want the untouched %q", v, digest)
	}
}

func TestFlushPublishesChangedFile(t *testing.T) {
	const was = "the prefetched content"
	digest := contentDigest([]byte(was))

	mount := newPutRecordingMount()
	mount.seed("report.md", was, digest)
	sb := newRecordingSandbox()
	sb.files["/workspace/output/report.md"] = []byte("what the agent wrote instead")

	manifest := NewManifest()
	manifest.Record("/workspace/output", "report.md", MountEntry{Key: "report.md", Size: int64(len(was)), Version: digest})

	if err := FlushMounts(context.Background(), sb, flushSpec(mount), manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if got := mount.putKeys(); len(got) != 1 || got[0] != "report.md" {
		t.Errorf("flush issued Puts %v, want exactly [report.md]", got)
	}
	if got := string(mount.entries["report.md"].data); got != "what the agent wrote instead" {
		t.Errorf("backend report.md = %q, want the agent's write", got)
	}
}

// TestFlushPublishesWhenTheVersionProvesNothing covers the conservative path:
// the manifest has no version, or a version in a scheme that says nothing
// about content. Neither is evidence, so the file is published exactly as it
// was before the skip existed.
func TestFlushPublishesWhenTheVersionProvesNothing(t *testing.T) {
	const body = "identical on both sides"

	cases := []struct {
		name    string
		version string
	}{
		{"empty version", ""},
		{"opaque version token", "v1"},
		{"etag rather than a content hash", `W/"3f80f-1b6"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mount := newPutRecordingMount()
			mount.seed("report.md", body, tc.version)
			sb := newRecordingSandbox()
			sb.files["/workspace/output/report.md"] = []byte(body)

			manifest := NewManifest()
			manifest.Record("/workspace/output", "report.md", MountEntry{Key: "report.md", Size: int64(len(body)), Version: tc.version})

			if err := FlushMounts(context.Background(), sb, flushSpec(mount), manifest); err != nil {
				t.Fatalf("FlushMounts: %v", err)
			}
			if got := mount.putKeys(); len(got) != 1 {
				t.Errorf("flush issued Puts %v, want exactly one — a version that proves nothing must not skip", got)
			}
		})
	}
}

// TestFlushPublishesSameLengthRewrite is the reason the close-time rule is
// stricter than backendLikelyHolds in changes.go. That one accepts equal size,
// because a per-tool-call detector that guesses wrong gets another look on the
// next call and a final one here. Nothing runs after this flush, so a
// same-length rewrite skipped here would never be published at all.
func TestFlushPublishesSameLengthRewrite(t *testing.T) {
	const was = "AAAAAAAAAAAA"
	const now = "BBBBBBBBBBBB"
	if len(was) != len(now) {
		t.Fatal("the fixtures must be the same length for this test to mean anything")
	}
	digest := contentDigest([]byte(was))

	mount := newPutRecordingMount()
	mount.seed("report.md", was, digest)
	sb := newRecordingSandbox()
	sb.files["/workspace/output/report.md"] = []byte(now)

	manifest := NewManifest()
	manifest.Record("/workspace/output", "report.md", MountEntry{Key: "report.md", Size: int64(len(was)), Version: digest})

	if err := FlushMounts(context.Background(), sb, flushSpec(mount), manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if got := mount.putKeys(); len(got) != 1 {
		t.Fatalf("flush issued Puts %v for a same-length rewrite, want exactly one — skipping it is a lost write", got)
	}
	if got := string(mount.entries["report.md"].data); got != now {
		t.Errorf("backend report.md = %q, want %q", got, now)
	}
}

// ── the guest hasher ──

// TestFlushWithoutFileHasherDownloadsEverything pins the unchanged behaviour
// of a guest that cannot hash: every candidate is still downloaded, and the
// skip is decided on the host from the bytes it read.
func TestFlushWithoutFileHasherDownloadsEverything(t *testing.T) {
	const same = "unchanged"
	digest := contentDigest([]byte(same))

	mount := newPutRecordingMount()
	mount.seed("same.md", same, digest)
	sb := newDownloadRecordingSandbox()
	sb.files["/workspace/output/same.md"] = []byte(same)
	sb.files["/workspace/output/changed.md"] = []byte("new file")

	if _, ok := AsFileHasher(sb); ok {
		t.Fatal("downloadRecordingSandbox must not implement FileHasher")
	}

	manifest := NewManifest()
	manifest.Record("/workspace/output", "same.md", MountEntry{Key: "same.md", Size: int64(len(same)), Version: digest})

	if err := FlushMounts(context.Background(), sb, flushSpec(mount), manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if got := len(sb.downloadedPaths()); got != 2 {
		t.Errorf("flush downloaded %d files, want 2 — a guest that cannot hash behaves exactly as before", got)
	}
	if got := mount.putKeys(); len(got) != 1 || got[0] != "changed.md" {
		t.Errorf("flush issued Puts %v, want exactly [changed.md]", got)
	}
}

// TestFlushWithFileHasherSkipsDownloads is the win the capability buys: on a
// prefetched workspace most files are unchanged, and the transfer — not the
// Put — is what they cost.
func TestFlushWithFileHasherSkipsDownloads(t *testing.T) {
	const same = "unchanged"
	digest := contentDigest([]byte(same))

	mount := newPutRecordingMount()
	mount.seed("same.md", same, digest)
	mount.seed("edited.md", "was", contentDigest([]byte("was")))
	sb := newGuestHashingSandbox()
	sb.files["/workspace/output/same.md"] = []byte(same)
	sb.files["/workspace/output/edited.md"] = []byte("now")

	manifest := NewManifest()
	manifest.Record("/workspace/output", "same.md", MountEntry{Key: "same.md", Size: int64(len(same)), Version: digest})
	manifest.Record("/workspace/output", "edited.md", MountEntry{Key: "edited.md", Size: 3, Version: contentDigest([]byte("was"))})

	if err := FlushMounts(context.Background(), sb, flushSpec(mount), manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if sb.hashCalls != 1 {
		t.Errorf("flush made %d HashFiles calls, want 1 per mount", sb.hashCalls)
	}
	if got := sb.downloadedPaths(); len(got) != 1 || got[0] != "/workspace/output/edited.md" {
		t.Errorf("flush downloaded %v, want only the file the guest said had changed", got)
	}
	if got := mount.putKeys(); len(got) != 1 || got[0] != "edited.md" {
		t.Errorf("flush issued Puts %v, want exactly [edited.md]", got)
	}
}

// TestFlushTreatsAPathMissingFromTheHashResultAsUnknown: the guest omits paths
// it could not read, and "could not read" is the last thing to call unchanged.
func TestFlushTreatsAPathMissingFromTheHashResultAsUnknown(t *testing.T) {
	const body = "identical on both sides"
	digest := contentDigest([]byte(body))

	mount := newPutRecordingMount()
	mount.seed("report.md", body, digest)
	sb := newGuestHashingSandbox()
	sb.files["/workspace/output/report.md"] = []byte(body)
	sb.omit = map[string]bool{"/workspace/output/report.md": true}

	manifest := NewManifest()
	manifest.Record("/workspace/output", "report.md", MountEntry{Key: "report.md", Size: int64(len(body)), Version: digest})

	if err := FlushMounts(context.Background(), sb, flushSpec(mount), manifest); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if got := sb.downloadedPaths(); len(got) != 1 {
		t.Errorf("flush downloaded %v, want the omitted path fetched rather than assumed unchanged", got)
	}
	// Downloaded, then found identical on the host, so still no Put. The
	// hasher only ever saves the transfer; it never changes the decision.
	if got := mount.putKeys(); len(got) != 0 {
		t.Errorf("flush issued Puts %v after reading identical bytes", got)
	}
}

// TestFlushFallsBackWhenTheGuestCannotHash covers both failure shapes at once:
// ErrHashUnsupported from a sandbox that advertises the capability but wraps a
// guest without it, and an ordinary transport failure. Neither may change the
// outcome, only the number of bytes moved.
func TestFlushFallsBackWhenTheGuestCannotHash(t *testing.T) {
	for _, hashErr := range []error{ErrHashUnsupported, errors.New("vsock: connection reset")} {
		t.Run(hashErr.Error(), func(t *testing.T) {
			const same = "unchanged"
			digest := contentDigest([]byte(same))

			mount := newPutRecordingMount()
			mount.seed("same.md", same, digest)
			sb := newGuestHashingSandbox()
			sb.err = hashErr
			sb.files["/workspace/output/same.md"] = []byte(same)
			sb.files["/workspace/output/changed.md"] = []byte("new file")

			manifest := NewManifest()
			manifest.Record("/workspace/output", "same.md", MountEntry{Key: "same.md", Size: int64(len(same)), Version: digest})

			if err := FlushMounts(context.Background(), sb, flushSpec(mount), manifest); err != nil {
				t.Fatalf("FlushMounts: %v", err)
			}
			if got := len(sb.downloadedPaths()); got != 2 {
				t.Errorf("flush downloaded %d files, want 2 — a failed hash falls back to reading every candidate", got)
			}
			if got := mount.putKeys(); len(got) != 1 || got[0] != "changed.md" {
				t.Errorf("flush issued Puts %v, want exactly [changed.md]", got)
			}
		})
	}
}

// TestFlushDoesNotHashExcludedFiles: ownership and filters run before the
// hash, so the guest is never asked about the tree the excludes just removed.
func TestFlushDoesNotHashExcludedFiles(t *testing.T) {
	mount := newPutRecordingMount()
	sb := newGuestHashingSandbox()
	sb.files["/workspace/output/report.md"] = []byte("a document")
	sb.files["/workspace/output/node_modules/pkg/index.js"] = []byte("a dependency")

	specs := flushSpec(mount)
	specs[0].Exclude = []string{"node_modules/**"}

	if err := FlushMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	for _, p := range sb.hashed {
		if strings.Contains(p, "node_modules") {
			t.Errorf("flush asked the guest to hash %q, which the mount excludes", p)
		}
	}
	if got := mount.putKeys(); len(got) != 1 || got[0] != "report.md" {
		t.Errorf("flush issued Puts %v, want exactly [report.md]", got)
	}
}

// flushSpec is the one-mount layout every flush test above shares.
func flushSpec(backend FilesystemMount) []MountSpec {
	return []MountSpec{{
		Path:         "/workspace/output",
		Backend:      backend,
		Mode:         MountReadWrite,
		FlushOnClose: true,
	}}
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
