package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// txFakeMount is the in-memory fakeMount plus the optional transactional
// capability, so the tests exercise the same backend twice: once through
// Put/flush, once through stage/commit.
type txFakeMount struct {
	*fakeMount

	mu      sync.Mutex
	staged  map[StagedContent][]byte
	nextRef int
	stages  int
	commits [][]MountChange // every Commit attempt, conflicting ones included
}

func newTxFakeMount() *txFakeMount {
	return &txFakeMount{
		fakeMount: newFakeMount(),
		staged:    make(map[StagedContent][]byte),
	}
}

func (m *txFakeMount) StageContent(ctx context.Context, size int64, data io.Reader) (StagedContent, error) {
	body, err := io.ReadAll(data)
	if err != nil {
		return "", err
	}
	if int64(len(body)) != size {
		return "", fmt.Errorf("size mismatch: declared %d, read %d", size, len(body))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextRef++
	m.stages++
	// Deliberately not content-addressed: identical bytes get different
	// handles, which is exactly what the contract permits and what stops the
	// framework from reading meaning into a handle.
	ref := StagedContent(fmt.Sprintf("staged-%d", m.nextRef))
	m.staged[ref] = body
	return ref, nil
}

func (m *txFakeMount) Commit(ctx context.Context, changes []MountChange) (CommitResult, error) {
	// Lock order is always txFakeMount.mu → fakeMount.mu; nothing takes them
	// the other way round.
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commits = append(m.commits, append([]MountChange(nil), changes...))
	if len(changes) == 0 {
		return CommitResult{}, nil
	}
	m.fakeMount.mu.Lock()
	defer m.fakeMount.mu.Unlock()

	seen := make(map[string]bool, len(changes))
	var conflicts []KeyConflict
	for _, ch := range changes {
		if seen[ch.Key] {
			return CommitResult{}, fmt.Errorf("duplicate key %q in one commit", ch.Key)
		}
		seen[ch.Key] = true
		cur, exists := m.fakeMount.entries[ch.Key]
		switch ch.Expect {
		case ExpectVersion:
			switch {
			case !exists:
				conflicts = append(conflicts, KeyConflict{Key: ch.Key, Have: ch.Have, Absent: true})
			case cur.version != ch.Have:
				conflicts = append(conflicts, KeyConflict{Key: ch.Key, Have: ch.Have, Want: cur.version})
			}
		case ExpectAbsent:
			if exists {
				conflicts = append(conflicts, KeyConflict{Key: ch.Key, Want: cur.version})
			}
		}
	}
	if len(conflicts) > 0 {
		// Nothing has been applied yet, so rejecting here is all-or-nothing.
		return CommitResult{}, &CommitConflictError{Conflicts: conflicts}
	}

	var consumed []StagedContent
	entries := make([]MountEntry, 0, len(changes))
	for _, ch := range changes {
		if ch.Op == OpDelete {
			delete(m.fakeMount.entries, ch.Key)
			continue
		}
		body, ok := m.staged[ch.Content]
		if !ok {
			return CommitResult{}, fmt.Errorf("unknown staged content %q for key %q", ch.Content, ch.Key)
		}
		consumed = append(consumed, ch.Content)
		newVer := "v1"
		if cur, exists := m.fakeMount.entries[ch.Key]; exists {
			newVer = cur.version + "+1"
		}
		m.fakeMount.entries[ch.Key] = fakeEntry{data: body, version: newVer, mime: ch.MimeType, mtime: time.Now()}
		entries = append(entries, MountEntry{
			Key:      ch.Key,
			Size:     int64(len(body)),
			MimeType: ch.MimeType,
			Version:  newVer,
		})
	}
	// Consumed after the loop, not during it, so one handle can land under
	// several keys in the same commit.
	for _, ref := range consumed {
		delete(m.staged, ref)
	}
	return CommitResult{Entries: entries}, nil
}

// countingMount is a plain FilesystemMount — no transactional capability —
// that records how many single-key Puts it received.
type countingMount struct {
	*fakeMount
	puts atomic.Int64
}

func (m *countingMount) Put(ctx context.Context, key, mimeType string, size int64, data io.Reader, ifVersion string) (string, error) {
	m.puts.Add(1)
	return m.fakeMount.Put(ctx, key, mimeType, size, data, ifVersion)
}

// txFile describes one file's part of a change set for stageAndCommit.
type txFile struct {
	key    string
	body   string
	mime   string
	expect VersionExpectation
	have   string
	del    bool
}

// stageAndCommit does what a caller of the capability does: stage each body on
// its own, then send one commit for the whole set. WC-7 will do this against a
// real sandbox; here it keeps the tests honest about the two-phase shape
// without pulling any of that policy into the package.
func stageAndCommit(t *testing.T, tx TransactionalMount, files ...txFile) (CommitResult, error) {
	t.Helper()
	changes := make([]MountChange, 0, len(files))
	for _, f := range files {
		if f.del {
			changes = append(changes, MountChange{Key: f.key, Op: OpDelete, Expect: f.expect, Have: f.have})
			continue
		}
		ref, err := tx.StageContent(context.Background(), int64(len(f.body)), strings.NewReader(f.body))
		if err != nil {
			t.Fatalf("StageContent %s: %v", f.key, err)
		}
		changes = append(changes, MountChange{
			Key:      f.key,
			Content:  ref,
			MimeType: f.mime,
			Expect:   f.expect,
			Have:     f.have,
		})
	}
	return tx.Commit(context.Background(), changes)
}

// ── detection ──

func TestAsTransactionalDetectsCapability(t *testing.T) {
	tx, ok := AsTransactional(newTxFakeMount())
	if !ok || tx == nil {
		t.Fatal("AsTransactional on a transactional backend = false, want true")
	}
}

func TestAsTransactionalRejectsPlainBackend(t *testing.T) {
	if _, ok := AsTransactional(newFakeMount()); ok {
		t.Fatal("AsTransactional on a plain FilesystemMount = true, want false")
	}
	if _, ok := AsTransactional(nil); ok {
		t.Fatal("AsTransactional(nil) = true, want false")
	}
}

// ── the batched commit ──

func TestTransactionalMountReceivesOneBatchedCommit(t *testing.T) {
	mount := newTxFakeMount()

	res, err := stageAndCommit(t, mount,
		txFile{key: "report.md", body: "# report", mime: "text/markdown", expect: ExpectAbsent},
		txFile{key: "data.csv", body: "id,value\n1,hi", mime: "text/csv", expect: ExpectAbsent},
		txFile{key: "notes/todo.txt", body: "later", expect: ExpectAbsent},
	)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if len(mount.commits) != 1 {
		t.Fatalf("backend saw %d commits, want exactly 1 for a three-file change", len(mount.commits))
	}
	if got := len(mount.commits[0]); got != 3 {
		t.Fatalf("commit carried %d changes, want 3", got)
	}
	if len(res.Entries) != 3 {
		t.Fatalf("CommitResult carried %d entries, want 3", len(res.Entries))
	}
	for _, e := range res.Entries {
		if e.Version == "" {
			t.Errorf("entry %q came back with no version; the next commit has nothing to assert", e.Key)
		}
	}
	for key, want := range map[string]string{
		"report.md":      "# report",
		"data.csv":       "id,value\n1,hi",
		"notes/todo.txt": "later",
	} {
		if got := string(mount.entries[key].data); got != want {
			t.Errorf("backend %q = %q, want %q", key, got, want)
		}
	}
}

func TestCommitAppliesPutAndDeleteTogether(t *testing.T) {
	mount := newTxFakeMount()
	mount.seed("old.txt", "gone soon", "v1")

	if _, err := stageAndCommit(t, mount,
		txFile{key: "new.txt", body: "hello", expect: ExpectAbsent},
		txFile{key: "old.txt", del: true, expect: ExpectVersion, have: "v1"},
	); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, ok := mount.entries["old.txt"]; ok {
		t.Error("old.txt should be gone after a commit that deletes it")
	}
	if string(mount.entries["new.txt"].data) != "hello" {
		t.Error("new.txt should exist after the same commit")
	}
}

func TestEmptyCommitIsNoOp(t *testing.T) {
	mount := newTxFakeMount()
	res, err := mount.Commit(context.Background(), nil)
	if err != nil {
		t.Fatalf("empty Commit returned %v, want nil — callers commit after every tool call", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("empty Commit produced %d entries, want 0", len(res.Entries))
	}
}

func TestStagedContentIsInvisibleBeforeCommit(t *testing.T) {
	mount := newTxFakeMount()
	if _, err := mount.StageContent(context.Background(), 5, strings.NewReader("hello")); err != nil {
		t.Fatalf("StageContent: %v", err)
	}
	entries, err := mount.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("staged content is visible to List (%d entries); it belongs to no key yet", len(entries))
	}
}

func TestCommitVersionsRoundTripThroughManifest(t *testing.T) {
	mount := newTxFakeMount()
	manifest := NewManifest()
	const mountPath = "/workspace"

	res, err := stageAndCommit(t, mount, txFile{key: "doc.md", body: "first", expect: ExpectAbsent})
	if err != nil {
		t.Fatalf("first Commit: %v", err)
	}
	for _, e := range res.Entries {
		manifest.Record(mountPath, e.Key, e)
	}

	ver, ok := manifest.Version(mountPath, "doc.md")
	if !ok {
		t.Fatal("manifest has no version after commit")
	}
	// The second commit can only work if the version the backend returned is
	// the same one it checks a precondition against.
	if _, err := stageAndCommit(t, mount, txFile{key: "doc.md", body: "second", expect: ExpectVersion, have: ver}); err != nil {
		t.Fatalf("second Commit with the version from the first: %v", err)
	}
	if got := string(mount.entries["doc.md"].data); got != "second" {
		t.Errorf("doc.md = %q, want %q", got, "second")
	}
}

// ── conflicts ──

func TestCommitConflictCarriesPerKeyDetail(t *testing.T) {
	mount := newTxFakeMount()
	mount.seed("report.md", "mine", "v1")
	mount.seed("data.csv", "mine", "v1")
	// A second writer moved one of the two keys since this caller read it.
	mount.seed("data.csv", "theirs", "v2")

	_, err := stageAndCommit(t, mount,
		txFile{key: "report.md", body: "new report", expect: ExpectVersion, have: "v1"},
		txFile{key: "data.csv", body: "new data", expect: ExpectVersion, have: "v1"},
	)
	if err == nil {
		t.Fatal("expected a conflict, got nil")
	}
	if !errors.Is(err, ErrCommitConflict) {
		t.Errorf("errors.Is(err, ErrCommitConflict) = false, want true")
	}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("errors.Is(err, ErrVersionMismatch) = false; existing callers branch on this sentinel")
	}

	var cc *CommitConflictError
	if !errors.As(err, &cc) {
		t.Fatalf("errors.As(err, *CommitConflictError) = false, got %T", err)
	}
	if len(cc.Conflicts) != 1 {
		t.Fatalf("Conflicts = %+v, want exactly the one stale key", cc.Conflicts)
	}
	c := cc.Conflicts[0]
	if c.Key != "data.csv" || c.Have != "v1" || c.Want != "v2" || c.Absent {
		t.Errorf("conflict = %+v, want {Key:data.csv Have:v1 Want:v2 Absent:false}", c)
	}

	// All-or-nothing: the key that was fine must not have been written either.
	if got := string(mount.entries["report.md"].data); got != "mine" {
		t.Errorf("report.md = %q after a rejected commit, want it untouched", got)
	}
	if got := string(mount.entries["data.csv"].data); got != "theirs" {
		t.Errorf("data.csv = %q after a rejected commit, want the other writer's content", got)
	}
}

func TestCommitConflictReportsEveryStaleKey(t *testing.T) {
	mount := newTxFakeMount()
	mount.seed("a.txt", "theirs", "v2")
	mount.seed("b.txt", "theirs", "v2")

	_, err := stageAndCommit(t, mount,
		txFile{key: "a.txt", body: "mine", expect: ExpectVersion, have: "v1"},
		txFile{key: "b.txt", body: "mine", expect: ExpectVersion, have: "v1"},
	)
	var cc *CommitConflictError
	if !errors.As(err, &cc) {
		t.Fatalf("expected *CommitConflictError, got %v", err)
	}
	if len(cc.Conflicts) != 2 {
		t.Fatalf("Conflicts = %+v, want both keys — one round trip per stale file is the point", cc.Conflicts)
	}
}

func TestCommitConflictOnDeletedKeyReportsAbsent(t *testing.T) {
	mount := newTxFakeMount()
	// The caller read this key at v1; someone deleted it since.
	_, err := stageAndCommit(t, mount, txFile{key: "gone.md", body: "mine", expect: ExpectVersion, have: "v1"})

	var cc *CommitConflictError
	if !errors.As(err, &cc) {
		t.Fatalf("expected *CommitConflictError, got %v", err)
	}
	c := cc.Conflicts[0]
	if !c.Absent {
		t.Errorf("conflict = %+v, want Absent — 'deleted' and 'changed' are different things to report", c)
	}
	if c.Want != "" {
		t.Errorf("Want = %q, want empty for an absent key", c.Want)
	}
}

func TestCommitExpectAbsentConflictsWithExistingKey(t *testing.T) {
	mount := newTxFakeMount()
	mount.seed("report.md", "someone got here first", "v7")

	_, err := stageAndCommit(t, mount, txFile{key: "report.md", body: "mine", expect: ExpectAbsent})
	var cc *CommitConflictError
	if !errors.As(err, &cc) {
		t.Fatalf("expected *CommitConflictError, got %v", err)
	}
	c := cc.Conflicts[0]
	if c.Have != "" || c.Want != "v7" {
		t.Errorf("conflict = %+v, want {Have:\"\" Want:v7}", c)
	}
	if string(mount.entries["report.md"].data) != "someone got here first" {
		t.Error("an ExpectAbsent commit overwrote an existing key")
	}
}

func TestCommitExpectAnyNeverConflicts(t *testing.T) {
	mount := newTxFakeMount()
	mount.seed("log.txt", "whatever", "v9")

	if _, err := stageAndCommit(t, mount, txFile{key: "log.txt", body: "appended"}); err != nil {
		t.Fatalf("ExpectAny (the zero value) conflicted: %v", err)
	}
	if got := string(mount.entries["log.txt"].data); got != "appended" {
		t.Errorf("log.txt = %q, want %q", got, "appended")
	}
}

func TestFailedCommitLeavesStagedContentUsable(t *testing.T) {
	mount := newTxFakeMount()
	mount.seed("stable.md", "", "v1")
	mount.seed("moved.md", "theirs", "v2")

	stable, err := mount.StageContent(context.Background(), 4, strings.NewReader("mine"))
	if err != nil {
		t.Fatalf("StageContent: %v", err)
	}
	moved, err := mount.StageContent(context.Background(), 4, strings.NewReader("mine"))
	if err != nil {
		t.Fatalf("StageContent: %v", err)
	}

	changes := []MountChange{
		{Key: "stable.md", Content: stable, Expect: ExpectVersion, Have: "v1"},
		{Key: "moved.md", Content: moved, Expect: ExpectVersion, Have: "v1"},
	}
	if _, err := mount.Commit(context.Background(), changes); err == nil {
		t.Fatal("expected a conflict on moved.md")
	}

	// Answering a conflict means re-reading and re-staging the key that moved,
	// and nothing else: the handle for the untouched key must still work.
	reMoved, err := mount.StageContent(context.Background(), 7, strings.NewReader("merged!"))
	if err != nil {
		t.Fatalf("StageContent on retry: %v", err)
	}
	retry := []MountChange{
		{Key: "stable.md", Content: stable, Expect: ExpectVersion, Have: "v1"},
		{Key: "moved.md", Content: reMoved, Expect: ExpectVersion, Have: "v2"},
	}
	if _, err := mount.Commit(context.Background(), retry); err != nil {
		t.Fatalf("retry Commit: %v", err)
	}
	if got := string(mount.entries["stable.md"].data); got != "mine" {
		t.Errorf("stable.md = %q, want the body staged before the failed commit", got)
	}
	if mount.stages != 3 {
		t.Errorf("staged %d bodies, want 3 — a retry re-stages only the conflicted key", mount.stages)
	}
}

func TestCommitConflictErrorMessageIsBounded(t *testing.T) {
	conflicts := make([]KeyConflict, 10)
	for i := range conflicts {
		conflicts[i] = KeyConflict{Key: fmt.Sprintf("f%d.txt", i), Have: "v1", Want: "v2"}
	}
	msg := (&CommitConflictError{Conflicts: conflicts}).Error()
	if !strings.Contains(msg, "10 conflicting keys") {
		t.Errorf("message does not report the total: %q", msg)
	}
	if !strings.Contains(msg, "and 6 more") {
		t.Errorf("message is not bounded to %d named keys: %q", maxConflictsInMessage, msg)
	}
	if strings.Contains(msg, "f9.txt") {
		t.Errorf("message names every key; a commit can carry a whole workspace: %q", msg)
	}
}

func TestCommitConflictErrorUnwrapsBackendCause(t *testing.T) {
	cause := errors.New("backend: serialization failure")
	err := &CommitConflictError{Conflicts: []KeyConflict{{Key: "a", Have: "v1", Want: "v2"}}, Cause: cause}
	if errors.Unwrap(err) != cause {
		t.Fatalf("Unwrap = %v, want the backend cause", errors.Unwrap(err))
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is against the backend cause = false, want true")
	}
}

// ── a backend without the capability is untouched ──

func TestPlainBackendKeepsPerFilePutFlush(t *testing.T) {
	mount := &countingMount{fakeMount: newFakeMount()}
	sb := newRecordingSandbox()
	sb.files["/workspace/a.md"] = []byte("a")
	sb.files["/workspace/b.md"] = []byte("b")

	specs := []MountSpec{{
		Path:         "/workspace",
		Backend:      mount,
		Mode:         MountReadWrite,
		FlushOnClose: true,
	}}

	if _, ok := AsTransactional(mount); ok {
		t.Fatal("countingMount must not satisfy TransactionalMount")
	}
	if err := FlushMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if got := mount.puts.Load(); got != 2 {
		t.Errorf("flush issued %d Puts, want 2 — a backend without the capability is unchanged", got)
	}
	if string(mount.entries["a.md"].data) != "a" || string(mount.entries["b.md"].data) != "b" {
		t.Error("flush did not publish both files through the plain Put path")
	}
}

// Where the capability is wired, now that something calls it.
//
// This test began life asserting that nothing in the framework commits at all
// — a guard against wiring the capability in by accident before anything was
// designed to use it. Per-tool-call commits are that design, so the guard now
// pins the boundary they drew rather than the absence of one: a tool call
// commits when the caller opted in, and the lifecycle still does not.
//
// The lifecycle staying on Put is deliberate, not leftover. Prefetch and flush
// bracket the session — flush runs when the guest filesystem has stopped
// moving and is the unconditional safety net for everything the commit path
// declined to guess about mid-turn (a file it could not read, a mount whose
// backend cannot commit, a change made before the first scan of a root). Its
// per-file Put is what a caller who never opts in still gets, unchanged.
func TestTransactionalCapabilityIsWiredAtTheToolLayerOnly(t *testing.T) {
	mount := newTxFakeMount()
	sb := newCommitSandbox(func(s *recordingSandbox) {
		writeGuestFile(s, "/workspace/b.md", "written by a command")
	})
	sb.files["/workspace/a.md"] = []byte("a")

	specs := []MountSpec{{
		Path:         "/workspace",
		Backend:      mount,
		Mode:         MountReadWrite,
		FlushOnClose: true,
	}}

	if err := FlushMounts(context.Background(), sb, specs, NewManifest()); err != nil {
		t.Fatalf("FlushMounts: %v", err)
	}
	if string(mount.entries["a.md"].data) != "a" {
		t.Error("flush did not publish through Put")
	}
	if len(mount.commits) != 0 {
		t.Errorf("flush issued %d commits; the close-time scan stays on Put", len(mount.commits))
	}

	// The tool layer, opted in, is the one caller of the capability.
	tools := Tools(sb, WithMounts(specs, NewManifest()), WithToolCallCommits(NewFullScanDetector()))
	runTool(t, tools, "shell", `{"command":"write b.md"}`)

	if len(mount.commits) != 1 {
		t.Fatalf("a tool call with commits enabled produced %d commits, want 1", len(mount.commits))
	}
	if got := string(mount.entries["b.md"].data); got != "written by a command" {
		t.Errorf("backend b.md = %q, want the command's write, committed before the next tool call", got)
	}
}

// compile-time checks
var (
	_ FilesystemMount    = (*txFakeMount)(nil)
	_ TransactionalMount = (*txFakeMount)(nil)
	_ FilesystemMount    = (*countingMount)(nil)
)
