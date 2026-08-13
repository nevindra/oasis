package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"
)

// ChangeDetector answers the one question the per-tool-call commit path needs
// answered: which files under a mount root differ from what the backend is
// believed to hold?
//
// It is an interface because how expensively that question can be answered
// depends on what the sandbox runtime underneath is willing to tell you, and
// the commit logic must not change when a runtime gets better at it.
//
// Two implementations ship here:
//
//   - NewStatHashDetector, the one to use. It asks the guest to describe the
//     workspace, discards everything the description proves untouched, has the
//     guest hash the rest, and transfers no file bodies.
//   - NewFullScanDetector, which downloads every file it may look at on every
//     call. It needs nothing but glob and download, and it is what the other
//     degrades into, per batch, when the guest cannot hash.
//
// Implementations must be safe for concurrent use: file tools call Published
// from whichever goroutine ran the tool, and a host may dispatch tool calls in
// parallel.
//
// # What the cheap path rests on
//
// Two optional pieces of the guest protocol, both added by ticket WC-2, both
// degradable:
//
//  1. GlobResult.Entries — size and mtime per file, from a guest-side stat, in
//     the round trip that already returns the names. This rejects every file
//     the tool call did not touch, which is nearly all of them, for no extra
//     call and a few dozen bytes each. Read GlobEntry.ModTime before trusting
//     an mtime comparison: sub-second precision is what makes it sound, and an
//     mtime without it cannot separate two writes inside one second.
//
//  2. FileHasher — sha256 for a caller-supplied list of paths, computed in the
//     guest. Stat is a filter, not an answer; it says only what cannot have
//     changed. The hash is the answer, and it is also the address the content
//     is stored under on a content-addressed backend, so it is worth having for
//     its own sake.
//
// Neither changes this interface. Scan returns ChangedFile values with Digest
// set and Content nil, and the commit path downloads only the bodies it stages.
//
// # What is still missing
//
// There is no way to ask a backend "do you already hold the blob with this
// hash?", which is what would remove the last transfer: a file the agent
// rewrote to content the backend has seen before is still uploaded. Neither
// TransactionalMount nor FilesystemMount can answer it, and adding a method to
// either breaks every backend implementing it. It belongs in a separate
// optional capability, added when a backend exists that can answer it.
type ChangeDetector interface {
	// Scan reports the files under scan.Root that differ from what this
	// detector last recorded as being at the backend.
	//
	// A file it has no record of is a change unless scan.Baseline vouches for
	// it. A file it has a record of is a change only if the content differs
	// from that record. Deletions are not reported: see the package's
	// MirrorDeletes handling, and commitAfterToolCall's comment on why
	// removing a backend file is a close-time decision rather than a
	// per-tool-call one.
	//
	// Scan is best-effort by design. A file that cannot be read — one that
	// vanished between enumeration and read, a socket, a race with the
	// command that just ran — must not stop the files that could be read from
	// being reported, so Scan may return both a non-empty slice and a non-nil
	// error, and the caller commits what it got.
	//
	// Scan must not advance its own record of what the backend holds. Only
	// Committed and Published do that, because only they know the bytes
	// actually got there. A Scan whose result is dropped, or whose commit is
	// rejected, has to report the same files again next time or the change is
	// lost until the close-time flush.
	Scan(ctx context.Context, sb Sandbox, scan ChangeScan) ([]ChangedFile, error)

	// Committed tells the detector that these files, as identified by their
	// Digest, are now what the backend holds, so a later Scan must not report
	// them again. Only Path and Digest are read; Content may be nil.
	Committed(files []ChangedFile)

	// Published tells the detector that the framework itself has just written
	// these exact bytes to the backend for path, by some route other than a
	// commit — Layer 2 tool interception publishes file_write, file_edit,
	// saved browser captures and deliver_file straight through Put.
	//
	// Without this the next scan would see the framework's own write as a
	// change and commit it a second time: same bytes, new version, a history
	// entry for a change nobody made. It is the single reason a detector is
	// told about writes it did not observe.
	Published(path string, content []byte)
}

// ChangeScan describes one detector pass over one mount root.
type ChangeScan struct {
	// Root is the absolute path inside the sandbox where the mount is rooted.
	Root string

	// Owns reports whether a file found under Root is this mount's to commit.
	// It carries the deepest-mount rule and the mount's Include/Exclude globs,
	// and it is passed into the scan rather than applied to its result because
	// the difference matters: a detector that filters afterwards has already
	// paid to read node_modules and the files a nested mount owns.
	//
	// A nil Owns means everything under Root belongs to the scan.
	Owns func(path string) bool

	// Baseline reports what the backend is believed to already hold for a
	// path, if anything — the Manifest entry left by prefetch or by an earlier
	// publish.
	//
	// It exists for one case: the first scan of a root, where the detector has
	// no history of its own and every prefetched file would otherwise look new
	// and be committed straight back to the backend it came from. The entry is
	// a belief, not an observation — it says what the framework put there, not
	// what is there now — so a detector should use it to seed itself and
	// prefer its own history afterwards.
	//
	// A nil Baseline means nothing is known, and the first scan of a root
	// reports every file under it.
	Baseline func(path string) (MountEntry, bool)
}

// ChangedFile is one file a detector believes differs from what the backend
// holds.
type ChangedFile struct {
	// Path is the absolute path inside the sandbox.
	Path string

	// Size is the file's byte length as the detector saw it. Advisory: the
	// commit path stages the bytes it ends up holding and derives the exact
	// size from those. It is here so a caller can decide not to move a file at
	// all — a policy this ticket does not implement — before paying to fetch
	// it.
	Size int64

	// Digest identifies the content the detector saw, in whatever scheme the
	// detector uses. The commit path never interprets it: it carries the value
	// back in Committed so the detector can recognise its own work. Two
	// detectors need not agree on the scheme, and one must not be swapped for
	// another mid-session.
	Digest string

	// Content is the file's bytes when the detector had to read them anyway,
	// and nil when it did not. A full-scan detector always sets it, because
	// reading is how it decided; a stat-and-hash detector leaves it nil and
	// the commit path fetches only the bodies it is about to stage.
	Content []byte
}

// fullScanDetector answers "what changed?" the only way today's guest allows:
// enumerate, download everything, hash it, compare. It keeps one digest per
// absolute sandbox path — the content it believes the backend holds — and
// reports the paths whose current content hashes differently.
//
// The cost is stated plainly in NewFullScanDetector's doc because it is the
// reason the option that turns it on is off by default.
type fullScanDetector struct {
	mu   sync.Mutex
	seen map[string]string // absolute sandbox path → digest believed to be at the backend
}

// NewFullScanDetector returns a ChangeDetector that globs the mount root,
// downloads every file the scan owns, and hashes it on the host.
//
// The cost per scan is one glob round trip plus one download round trip per
// owned file, and it moves the full byte size of the owned files, whether or
// not anything changed. That is O(workspace) per tool call, so a workspace of a
// hundred files or a few large ones will dominate the tool call it follows.
//
// Prefer NewStatHashDetector, which needs nothing this does not and costs a
// fraction of it. This one remains because it asks the guest for nothing beyond
// glob and download: it is the detector for a sandbox runtime that reports
// neither file metadata nor hashes, and it is the fallback the other detector
// degrades into per file when the guest cannot answer.
func NewFullScanDetector() ChangeDetector {
	return &fullScanDetector{seen: make(map[string]string)}
}

func (d *fullScanDetector) Scan(ctx context.Context, sb Sandbox, scan ChangeScan) ([]ChangedFile, error) {
	res, err := sb.GlobFiles(ctx, GlobRequest{Pattern: "**/*", Path: scan.Root})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", scan.Root, err)
	}

	var changed []ChangedFile
	var errs []error
	for _, p := range res.Files {
		if scan.Owns != nil && !scan.Owns(p) {
			continue
		}
		body, err := readSandboxFile(ctx, sb, p)
		if err != nil {
			// A file that vanished between the glob and the read is the
			// normal case here, not an exceptional one: the command that
			// just ran may still be tidying up after itself. Report it and
			// carry on — one unreadable temp file must not stop the
			// workspace from being committed.
			errs = append(errs, err)
			continue
		}
		digest := contentDigest(body)
		if d.believesCurrent(p, digest, int64(len(body)), scan.Baseline) {
			continue
		}
		changed = append(changed, ChangedFile{
			Path:    p,
			Size:    int64(len(body)),
			Digest:  digest,
			Content: body,
		})
	}
	return changed, joinErrors(errs)
}

// believesCurrent reports whether the backend is already holding this content,
// and adopts the digest as this detector's record when the baseline vouches
// for a path it has never seen.
func (d *fullScanDetector) believesCurrent(path, digest string, size int64, baseline func(string) (MountEntry, bool)) bool {
	d.mu.Lock()
	prev, known := d.seen[path]
	d.mu.Unlock()
	if known {
		return prev == digest
	}
	if baseline == nil {
		return false
	}
	entry, tracked := baseline(path)
	if !tracked || !backendLikelyHolds(entry, digest, size) {
		return false
	}
	d.record(path, digest)
	return true
}

// backendLikelyHolds decides, for a file this detector is seeing for the first
// time, whether the manifest's belief about the backend already covers the
// bytes now on disk.
//
// Two comparisons, in order of how much they prove:
//
//   - Version equal to the digest. A backend that addresses content by its
//     hash reports that hash as the version, so this is exact, and it is the
//     shape the workspace backend is moving to.
//
//   - Size equal. Everything else. Prefetch copied these bytes in itself, so
//     equal size is decent evidence they are still the same bytes — and the
//     failure it admits is narrow: a rewrite to the identical byte length,
//     made before the first scan of this root, is taken for the original.
//     That file waits for the close-time flush, which publishes
//     unconditionally, so the outcome for it is exactly today's behaviour
//     rather than a loss.
func backendLikelyHolds(entry MountEntry, digest string, size int64) bool {
	if entry.Version != "" && entry.Version == digest {
		return true
	}
	return entry.Size == size
}

func (d *fullScanDetector) Committed(files []ChangedFile) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range files {
		d.seen[f.Path] = f.Digest
	}
}

func (d *fullScanDetector) Published(path string, content []byte) {
	d.record(path, contentDigest(content))
}

func (d *fullScanDetector) record(path, digest string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[path] = digest
}

// contentDigest is sha256 of the bytes, hex-encoded — the same address the
// content-addressed backend uses, so a detector digest and a backend version
// can be compared directly once that lands (see backendLikelyHolds).
func contentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// readSandboxFile pulls one file's bytes out of the guest.
func readSandboxFile(ctx context.Context, sb Sandbox, path string) ([]byte, error) {
	rc, err := sb.DownloadFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", path, err)
	}
	if rc == nil {
		return nil, fmt.Errorf("download %s: no content", path)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return body, nil
}

// compile-time check
var _ ChangeDetector = (*fullScanDetector)(nil)

// statHashDetector answers "what changed?" the cheap way: it asks the guest to
// describe the workspace, eliminates every file whose description proves it was
// not touched, asks the guest to hash only what is left, and moves bytes for
// nothing at all.
//
// It keeps, per absolute sandbox path, the digest it believes the backend holds
// *and the stat it observed when it last confirmed that digest*. The stat is
// what makes the next scan free: a file whose size and mtime are unchanged
// since the confirmation is not a file that needs hashing.
type statHashDetector struct {
	mu   sync.Mutex
	seen map[string]observedFile
}

// observedFile is what the detector believes about one path: the content, and
// the stat that content was last seen wearing.
type observedFile struct {
	digest string
	size   int64

	// mod is the mtime observed at the moment digest was confirmed, or the zero
	// time when the digest was learned some other way — from Committed, or from
	// Published — and no stat accompanied it.
	//
	// A zero mod is why a file is hashed once more after it is committed: the
	// detector knows what the backend holds but not what the file looks like on
	// disk, and it will not assume. That is one guest-side hash per committed
	// file per turn, which buys never mistaking a post-commit rewrite for the
	// commit's own bytes.
	mod time.Time
}

// maxHashBatch bounds how many paths go into one HashFiles call. The guest
// hashes them one at a time, so batching changes nothing about the work done —
// it bounds the size of a single request and lets a large workspace make
// progress in pieces rather than as one all-or-nothing round trip.
const maxHashBatch = 256

// NewStatHashDetector returns the ChangeDetector to use with WithToolCallCommits.
//
// Per scan it costs one glob round trip, plus one hash round trip per 256
// candidate files, and it transfers **no file bodies at all**. The commit path
// downloads only the files it is about to stage, which is the set that actually
// changed.
//
// The saving is not a constant factor. A tool call that writes one file in a
// hundred-file workspace costs, here, one glob and one hash of one file;
// NewFullScanDetector costs the same glob plus a hundred downloads and the full
// byte size of the workspace over vsock. That is the difference between
// per-tool-call commits being affordable and being a thing to leave switched
// off.
//
// # What it needs, and what it does without
//
// It uses two optional pieces of the guest protocol and degrades cleanly when
// either is missing:
//
//   - GlobResult.Entries, for size and mtime. Absent, every owned file becomes
//     a hash candidate — still no downloads, just more hashing.
//   - FileHasher, for guest-side digests. Absent (ErrHashUnsupported, an older
//     runtime, any error at all), candidates are downloaded and hashed on the
//     host, which is exactly what NewFullScanDetector does and no worse.
//
// With neither, it behaves as a full scan. It is therefore always safe to use,
// and it gets faster as the runtime underneath it improves without anything
// having to be reconfigured.
//
// # What it trades away
//
// A rewrite that preserves both size and mtime is invisible to it until the
// close-time flush. See statProvesUnchanged for which tools produce that and
// why the flush makes it a latency cost rather than a correctness one.
func NewStatHashDetector() ChangeDetector {
	return &statHashDetector{seen: make(map[string]observedFile)}
}

func (d *statHashDetector) Scan(ctx context.Context, sb Sandbox, scan ChangeScan) ([]ChangedFile, error) {
	res, err := sb.GlobFiles(ctx, GlobRequest{Pattern: "**/*", Path: scan.Root})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", scan.Root, err)
	}

	// Entries are matched by path, not by index: the guest leaves out anything
	// it could not stat, and those paths are still in Files.
	meta := make(map[string]GlobEntry, len(res.Entries))
	for _, e := range res.Entries {
		meta[e.Path] = e
	}

	candidates := make([]string, 0, len(res.Files))
	for _, p := range res.Files {
		if scan.Owns != nil && !scan.Owns(p) {
			continue
		}
		if d.statProvesUnchanged(p, meta[p]) {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	hashed, errs := d.digestAll(ctx, sb, candidates)

	var changed []ChangedFile
	for _, p := range candidates {
		h, ok := hashed[p]
		if !ok {
			// The guest would not or could not hash it. That is the normal
			// outcome for a directory caught by the glob, for a file the
			// running command deleted a moment ago, and for anything not
			// readable — none of which is worth an error, and none of which is
			// safe to call unchanged either. It is simply not reported, and the
			// close-time flush is the backstop that publishes unconditionally.
			continue
		}
		if d.believesCurrent(p, h, meta[p], scan.Baseline) {
			continue
		}
		changed = append(changed, ChangedFile{
			Path:    p,
			Size:    h.size,
			Digest:  h.digest,
			Content: h.content, // nil unless the host had to read it anyway
		})
	}
	return changed, joinErrors(errs)
}

// statProvesUnchanged reports whether metadata alone settles it, without
// hashing anything.
//
// Every condition here is required, and each rules out a way of being wrong:
//
//   - A known entry. With no record of the path there is nothing to compare to.
//   - Metadata present. A path the guest could not stat is unknown, not
//     unchanged.
//   - A stat recorded alongside the digest. After Committed or Published the
//     detector knows the content but has never seen the file; comparing against
//     a zero mtime would be comparing against nothing.
//   - Equal size and equal mtime. The actual test.
//   - A non-zero nanosecond component on the mtime. This is the one that is
//     easy to leave out and expensive to leave out. A second-granular mtime
//     cannot separate two writes inside one second, and an agent's
//     write-run-rewrite loop produces exactly that. A non-zero nanosecond field
//     proves the guest carried sub-second precision for this file, so equal
//     mtime means equal moment. See GlobEntry.ModTime.
//
// # The one thing this cannot see
//
// A rewrite that keeps both the byte length and the mtime — `cp -p`,
// `shutil.copy2`, `touch -r`, a tar extraction that restores timestamps — is
// indistinguishable here from no write at all. This is the same trade rsync,
// make and git's stat cache make, and it is made for the same reason: the
// alternative is hashing every byte of the workspace after every tool call.
//
// It costs latency, never data. The close-time flush compares content, not
// metadata, so a change hidden from every scan in the turn is still published
// when the turn ends — it lands late rather than mid-turn. Nothing downstream
// treats a late commit differently from an early one.
func (d *statHashDetector) statProvesUnchanged(path string, entry GlobEntry) bool {
	if entry.Path == "" || entry.ModTime.IsZero() || entry.ModTime.Nanosecond() == 0 {
		return false
	}
	d.mu.Lock()
	prev, known := d.seen[path]
	d.mu.Unlock()
	if !known || prev.mod.IsZero() {
		return false
	}
	return prev.size == entry.Size && prev.mod.Equal(entry.ModTime)
}

// believesCurrent decides whether a hashed candidate is already at the backend,
// and adopts what it learned so the next scan can skip the file on stat alone.
//
// Recording here does not violate Scan's promise not to advance its record of
// what the backend holds. The digest recorded is one the detector already
// believed, or one the baseline vouched for — never one this scan is about to
// report as a change. What actually advances is the *stat*, which is knowledge
// about the file on disk and not a claim about the backend.
func (d *statHashDetector) believesCurrent(path string, h hashedFile, entry GlobEntry, baseline func(string) (MountEntry, bool)) bool {
	d.mu.Lock()
	prev, known := d.seen[path]
	d.mu.Unlock()

	if known {
		if prev.digest != h.digest {
			return false
		}
		d.observe(path, h.digest, h.size, entry.ModTime)
		return true
	}
	if baseline == nil {
		return false
	}
	be, tracked := baseline(path)
	if !tracked || !backendLikelyHolds(be, h.digest, h.size) {
		return false
	}
	d.observe(path, h.digest, h.size, entry.ModTime)
	return true
}

// hashedFile is one candidate's content identity, and its bytes when the host
// had to read them to learn it.
type hashedFile struct {
	digest  string
	size    int64
	content []byte
}

// digestAll gets a digest for every candidate, preferring the guest.
//
// The fallback is per-batch rather than per-file on purpose: a guest that
// cannot hash fails the whole call, and a guest that can does not fail
// selectively — it omits what it could not read, which is a result, not a
// failure. So one failed batch means download that batch, and a batch that
// comes back short means those paths have no digest and are left alone.
func (d *statHashDetector) digestAll(ctx context.Context, sb Sandbox, candidates []string) (map[string]hashedFile, []error) {
	out := make(map[string]hashedFile, len(candidates))
	var errs []error

	hasher, canHash := AsFileHasher(sb)
	for start := 0; start < len(candidates); start += maxHashBatch {
		batch := candidates[start:min(start+maxHashBatch, len(candidates))]

		if canHash {
			hashes, err := hasher.HashFiles(ctx, batch)
			if err == nil {
				for _, fh := range hashes {
					out[fh.Path] = hashedFile{digest: fh.Digest, size: fh.Size}
				}
				continue
			}
			// Any error at all — ErrHashUnsupported from an older runtime, a
			// transport failure, a daemon that does not know the route — means
			// this batch is answered the slow way. Not reported as an error:
			// the fallback produces the same answer, and an unsupported
			// capability is not a fault.
			canHash = false
		}

		for _, p := range batch {
			body, err := readSandboxFile(ctx, sb, p)
			if err != nil {
				// Same reasoning as the missing-hash case in Scan: a file that
				// vanished between the glob and the read is ordinary. It is
				// collected so a caller can see it, and left unreported so one
				// temp file cannot stop the workspace being committed.
				errs = append(errs, err)
				continue
			}
			out[p] = hashedFile{digest: contentDigest(body), size: int64(len(body)), content: body}
		}
	}
	return out, errs
}

func (d *statHashDetector) Committed(files []ChangedFile) {
	for _, f := range files {
		// No mtime: these bytes are known to be at the backend, but the detector
		// has not seen the file since. See observedFile.mod.
		d.observe(f.Path, f.Digest, f.Size, time.Time{})
	}
}

func (d *statHashDetector) Published(path string, content []byte) {
	d.observe(path, contentDigest(content), int64(len(content)), time.Time{})
}

func (d *statHashDetector) observe(path, digest string, size int64, mod time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[path] = observedFile{digest: digest, size: size, mod: mod}
}

// compile-time check
var _ ChangeDetector = (*statHashDetector)(nil)
