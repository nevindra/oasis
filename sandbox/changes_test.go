package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The clock these tests date files with. A fixed base with a non-zero
// nanosecond component, because that component is load-bearing: it is what
// tells the detector the guest carried sub-second precision and that an equal
// mtime therefore means an equal moment. Tests that need the opposite build a
// whole-second time explicitly.
var fakeClock = time.Date(2026, 8, 13, 4, 30, 0, 123456789, time.UTC)

type fakeFile struct {
	content []byte
	mod     time.Time
}

// statSandbox is a Sandbox that only implements what a ChangeDetector calls.
// The embedded nil interface makes every other method a panic, which is the
// point: a detector reaching for anything else is a test failure, not a
// silently-mocked success.
//
// It deliberately does NOT implement FileHasher — hashingSandbox wraps it for
// that — so the two halves of the detector's fallback can both be exercised.
type statSandbox struct {
	Sandbox

	mu    sync.Mutex
	files map[string]*fakeFile

	// noEntries makes glob answer with names only, the way a sandbox runtime
	// that predates the metadata protocol does.
	noEntries bool

	globs     atomic.Int64
	downloads atomic.Int64
}

func newStatSandbox() *statSandbox {
	return &statSandbox{files: make(map[string]*fakeFile)}
}

func (s *statSandbox) put(path, content string) {
	s.putAt(path, content, fakeClock)
}

func (s *statSandbox) putAt(path, content string, mod time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = &fakeFile{content: []byte(content), mod: mod}
}

func (s *statSandbox) GlobFiles(ctx context.Context, req GlobRequest) (GlobResult, error) {
	s.globs.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()

	var res GlobResult
	for p := range s.files {
		if !strings.HasPrefix(p, req.Path) {
			continue
		}
		res.Files = append(res.Files, p)
	}
	sort.Strings(res.Files)
	if s.noEntries {
		return res, nil
	}
	for _, p := range res.Files {
		f := s.files[p]
		res.Entries = append(res.Entries, GlobEntry{Path: p, Size: int64(len(f.content)), ModTime: f.mod})
	}
	return res, nil
}

func (s *statSandbox) DownloadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	s.downloads.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.files[path]
	if !ok {
		return nil, fmt.Errorf("no such file: %s", path)
	}
	return io.NopCloser(strings.NewReader(string(f.content))), nil
}

// hashingSandbox adds the FileHasher capability to statSandbox.
type hashingSandbox struct {
	*statSandbox

	// err, when set, fails every HashFiles call — the transport failure and the
	// ErrHashUnsupported case share this path.
	err error

	// omit names paths the guest will not return a hash for: a directory the
	// glob caught, a file the command deleted a moment ago.
	omit map[string]bool

	calls     atomic.Int64
	pathsSeen atomic.Int64
}

func newHashingSandbox() *hashingSandbox {
	return &hashingSandbox{statSandbox: newStatSandbox(), omit: make(map[string]bool)}
}

func (s *hashingSandbox) HashFiles(ctx context.Context, paths []string) ([]FileHash, error) {
	s.calls.Add(1)
	s.pathsSeen.Add(int64(len(paths)))
	if s.err != nil {
		return nil, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]FileHash, 0, len(paths))
	for _, p := range paths {
		if s.omit[p] {
			continue
		}
		f, ok := s.files[p]
		if !ok {
			continue
		}
		out = append(out, FileHash{Path: p, Digest: contentDigest(f.content), Size: int64(len(f.content))})
	}
	return out, nil
}

var _ FileHasher = (*hashingSandbox)(nil)

func scanPaths(t *testing.T, d ChangeDetector, sb Sandbox, root string) []string {
	t.Helper()
	changed, err := d.Scan(context.Background(), sb, ChangeScan{Root: root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	paths := make([]string, 0, len(changed))
	for _, c := range changed {
		paths = append(paths, c.Path)
	}
	sort.Strings(paths)
	return paths
}

func TestStatHashDetectorReportsEverythingOnFirstScan(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/a.txt", "alpha")
	sb.put("/workspace/b.txt", "beta")

	got := scanPaths(t, NewStatHashDetector(), sb, "/workspace")
	want := []string{"/workspace/a.txt", "/workspace/b.txt"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("first scan = %v, want %v", got, want)
	}
	if n := sb.downloads.Load(); n != 0 {
		t.Errorf("downloads = %d, want 0: the detector must not move file bodies when the guest can hash", n)
	}
}

func TestStatHashDetectorMovesNoBodies(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/report.xlsx", "workbook bytes")

	changed, err := NewStatHashDetector().Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed = %d files, want 1", len(changed))
	}
	if changed[0].Content != nil {
		t.Error("Content must be nil when the guest hashed the file: the commit path fetches only what it stages")
	}
	if changed[0].Digest != contentDigest([]byte("workbook bytes")) {
		t.Errorf("Digest = %q, want the sha256 of the content", changed[0].Digest)
	}
	if changed[0].Size != 14 {
		t.Errorf("Size = %d, want 14", changed[0].Size)
	}
}

func TestStatHashDetectorSecondScanCostsNoHashing(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/a.txt", "alpha")
	sb.put("/workspace/b.txt", "beta")

	d := NewStatHashDetector()
	first, err := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	d.Committed(first)

	// Committed leaves the detector knowing the content but not the stat, so
	// the next scan hashes once more to learn it. That scan is the one that
	// records the stat; the one after it is the cheap steady state.
	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 0 {
		t.Fatalf("scan after Committed = %v, want no changes", got)
	}
	before := sb.calls.Load()

	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 0 {
		t.Fatalf("steady-state scan = %v, want no changes", got)
	}
	if after := sb.calls.Load(); after != before {
		t.Errorf("HashFiles calls went %d -> %d; an unchanged workspace must cost stat comparison only", before, after)
	}
	if n := sb.downloads.Load(); n != 0 {
		t.Errorf("downloads = %d, want 0 across the whole test", n)
	}
}

// The write-run-rewrite case: an agent rewrites a file to a different value of
// the same byte length, moments after the last one, inside the same second.
// Sub-second mtime precision is what separates them, and it is why
// GlobEntry.ModTime is a time.Time rather than a second count.
func TestStatHashDetectorCatchesSameSizeRewriteInSameSecond(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/data.csv", "1,2,3")

	d := NewStatHashDetector()
	first, _ := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	d.Committed(first)
	scanPaths(t, d, sb, "/workspace") // settle the recorded stat

	// Same length, same second, a few microseconds later — what a real
	// filesystem stamps on the second write.
	sb.putAt("/workspace/data.csv", "9,8,7", fakeClock.Add(3*time.Microsecond))

	got := scanPaths(t, d, sb, "/workspace")
	if len(got) != 1 || got[0] != "/workspace/data.csv" {
		t.Fatalf("rewrite in the same second = %v, want the file reported as changed", got)
	}
}

// The documented blind spot, pinned so that closing it later is a deliberate
// act and not a surprise: a rewrite that preserves both size and mtime — what
// `cp -p`, `shutil.copy2` and a timestamp-restoring tar extraction produce — is
// indistinguishable from no write, and the detector says nothing.
//
// This costs latency, not data. The close-time flush compares content rather
// than metadata, so the change is still published when the turn ends. The
// second half of this test is the half that matters.
func TestStatHashDetectorDefersMtimePreservingRewriteToFlush(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/data.csv", "1,2,3")

	d := NewStatHashDetector()
	first, _ := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	d.Committed(first)
	scanPaths(t, d, sb, "/workspace") // settle the recorded stat

	sb.putAt("/workspace/data.csv", "9,8,7", fakeClock) // same size, same mtime

	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 0 {
		t.Fatalf("scan = %v; this case is documented as invisible to stat — if it is now caught, "+
			"update statProvesUnchanged's doc and NewStatHashDetector's, then delete this test", got)
	}

	// The backstop: the content on disk differs from the digest the framework
	// believes the backend holds, which is exactly what flushOne compares.
	d.(*statHashDetector).mu.Lock()
	believed := d.(*statHashDetector).seen["/workspace/data.csv"].digest
	d.(*statHashDetector).mu.Unlock()
	if believed == contentDigest([]byte("9,8,7")) {
		t.Fatal("the detector believes the backend already holds the new content; the flush would skip it and the write would be lost")
	}
}

// A whole-second mtime cannot prove two writes were the same write, so the
// detector must not trust it — every scan re-hashes, and correctness survives a
// guest whose filesystem coarsens timestamps.
func TestStatHashDetectorDistrustsWholeSecondMtime(t *testing.T) {
	coarse := time.Date(2026, 8, 13, 4, 30, 0, 0, time.UTC)
	sb := newHashingSandbox()
	sb.putAt("/workspace/a.txt", "alpha", coarse)

	d := NewStatHashDetector()
	first, _ := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	d.Committed(first)

	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 0 {
		t.Fatalf("unchanged file reported as %v", got)
	}
	before := sb.calls.Load()
	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 0 {
		t.Fatalf("unchanged file reported as %v", got)
	}
	if sb.calls.Load() == before {
		t.Error("a whole-second mtime was treated as proof of no change; it must stay a hash candidate")
	}

	// And the correctness this buys: a same-length rewrite at the same coarse
	// timestamp is still caught.
	sb.putAt("/workspace/a.txt", "ALPHA", coarse)
	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 1 {
		t.Fatalf("same-length rewrite at a coarse mtime = %v, want it reported", got)
	}
}

func TestStatHashDetectorDetectsSizeChange(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/a.txt", "alpha")

	d := NewStatHashDetector()
	first, _ := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	d.Committed(first)
	scanPaths(t, d, sb, "/workspace")

	sb.putAt("/workspace/a.txt", "alpha plus more", fakeClock.Add(time.Second))
	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 1 {
		t.Fatalf("grown file = %v, want it reported as changed", got)
	}
}

func TestStatHashDetectorFallsBackWhenGuestCannotHash(t *testing.T) {
	// A plain statSandbox has no FileHasher at all.
	sb := newStatSandbox()
	sb.put("/workspace/a.txt", "alpha")

	d := NewStatHashDetector()
	changed, err := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed = %d, want 1", len(changed))
	}
	if changed[0].Content == nil {
		t.Error("Content must carry the bytes when the host had to read them anyway")
	}
	if sb.downloads.Load() != 1 {
		t.Errorf("downloads = %d, want 1", sb.downloads.Load())
	}
	if changed[0].Digest != contentDigest([]byte("alpha")) {
		t.Error("host-computed digest must match the guest's scheme")
	}
}

func TestStatHashDetectorFallsBackOnHashError(t *testing.T) {
	sb := newHashingSandbox()
	sb.err = errors.New("vsock reset")
	sb.put("/workspace/a.txt", "alpha")

	d := NewStatHashDetector()
	changed, err := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	if err != nil {
		t.Fatalf("a failed hash must fall back, not fail the scan: %v", err)
	}
	if len(changed) != 1 || changed[0].Content == nil {
		t.Fatalf("changed = %+v, want one entry carrying downloaded content", changed)
	}
	if sb.downloads.Load() != 1 {
		t.Errorf("downloads = %d, want 1", sb.downloads.Load())
	}
}

func TestStatHashDetectorFallsBackOnUnsupported(t *testing.T) {
	sb := newHashingSandbox()
	sb.err = ErrHashUnsupported
	sb.put("/workspace/a.txt", "alpha")

	changed, err := NewStatHashDetector().Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	if err != nil {
		t.Fatalf("ErrHashUnsupported is not a fault: %v", err)
	}
	if len(changed) != 1 {
		t.Fatalf("changed = %d, want 1", len(changed))
	}
}

func TestStatHashDetectorWithoutGlobEntriesStillCorrect(t *testing.T) {
	sb := newHashingSandbox()
	sb.noEntries = true
	sb.put("/workspace/a.txt", "alpha")

	d := NewStatHashDetector()
	first, err := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("changed = %d, want 1", len(first))
	}
	d.Committed(first)

	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 0 {
		t.Fatalf("unchanged file reported as %v", got)
	}
	// Without metadata every file stays a candidate forever — hashing every
	// scan, but never transferring a body and never wrong.
	if sb.downloads.Load() != 0 {
		t.Errorf("downloads = %d, want 0", sb.downloads.Load())
	}
	sb.put("/workspace/a.txt", "beta!")
	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 1 {
		t.Fatalf("changed file = %v, want it reported", got)
	}
}

func TestStatHashDetectorUnhashablePathIsNotAChange(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/a.txt", "alpha")
	sb.put("/workspace/subdir", "") // stands in for a directory the glob caught
	sb.omit["/workspace/subdir"] = true

	d := NewStatHashDetector()
	changed, err := d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	if err != nil {
		t.Fatalf("an unhashable path is ordinary, not an error: %v", err)
	}
	if len(changed) != 1 || changed[0].Path != "/workspace/a.txt" {
		t.Fatalf("changed = %+v, want only a.txt", changed)
	}
}

func TestStatHashDetectorHonoursOwns(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/keep.txt", "yes")
	sb.put("/workspace/node_modules/pkg/index.js", "no")

	changed, err := NewStatHashDetector().Scan(context.Background(), sb, ChangeScan{
		Root: "/workspace",
		Owns: func(p string) bool { return !strings.Contains(p, "node_modules") },
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(changed) != 1 || changed[0].Path != "/workspace/keep.txt" {
		t.Fatalf("changed = %+v, want only keep.txt", changed)
	}
	// Disowned files must be filtered before they cost anything, not after.
	if sb.pathsSeen.Load() != 1 {
		t.Errorf("hashed %d paths, want 1: ownership must be applied before hashing", sb.pathsSeen.Load())
	}
}

func TestStatHashDetectorBaselineSuppressesPrefetchedFiles(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/prefetched.txt", "from the backend")
	sb.put("/workspace/fresh.txt", "written by the agent")

	digest := contentDigest([]byte("from the backend"))
	changed, err := NewStatHashDetector().Scan(context.Background(), sb, ChangeScan{
		Root: "/workspace",
		Baseline: func(p string) (MountEntry, bool) {
			if p == "/workspace/prefetched.txt" {
				return MountEntry{Key: "prefetched.txt", Version: digest, Size: 16}, true
			}
			return MountEntry{}, false
		},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(changed) != 1 || changed[0].Path != "/workspace/fresh.txt" {
		t.Fatalf("changed = %+v, want only fresh.txt: a prefetched file must not be committed straight back", changed)
	}
}

func TestStatHashDetectorPublishedIsNotReported(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/written.txt", "by file_write")

	d := NewStatHashDetector()
	d.Published("/workspace/written.txt", []byte("by file_write"))

	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 0 {
		t.Fatalf("scan = %v, want nothing: Layer 2 already put these bytes at the backend", got)
	}
}

func TestStatHashDetectorRepeatsUncommittedChanges(t *testing.T) {
	sb := newHashingSandbox()
	sb.put("/workspace/a.txt", "alpha")

	d := NewStatHashDetector()
	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 1 {
		t.Fatalf("first scan = %v", got)
	}
	// The commit was rejected, so Committed was never called. The change must
	// still be pending — a dropped scan result cannot lose the write.
	if got := scanPaths(t, d, sb, "/workspace"); len(got) != 1 {
		t.Fatalf("second scan = %v, want the change reported again", got)
	}
}

func TestStatHashDetectorBatchesLargeCandidateSets(t *testing.T) {
	sb := newHashingSandbox()
	const total = maxHashBatch*2 + 7
	for i := range total {
		sb.put(fmt.Sprintf("/workspace/f%04d.txt", i), fmt.Sprintf("content %d", i))
	}

	changed, err := NewStatHashDetector().Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(changed) != total {
		t.Fatalf("changed = %d, want %d: batching must not drop candidates", len(changed), total)
	}
	if got, want := sb.calls.Load(), int64(3); got != want {
		t.Errorf("HashFiles calls = %d, want %d", got, want)
	}
	if got := sb.pathsSeen.Load(); got != total {
		t.Errorf("hashed %d paths, want %d", got, total)
	}
	if sb.downloads.Load() != 0 {
		t.Errorf("downloads = %d, want 0", sb.downloads.Load())
	}
}

func TestStatHashDetectorConcurrentPublishIsSafe(t *testing.T) {
	sb := newHashingSandbox()
	d := NewStatHashDetector()
	for i := range 50 {
		sb.put(fmt.Sprintf("/workspace/f%d.txt", i), "x")
	}

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Published(fmt.Sprintf("/workspace/f%d.txt", i), []byte("x"))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = d.Scan(context.Background(), sb, ChangeScan{Root: "/workspace"})
	}()
	wg.Wait()
}
