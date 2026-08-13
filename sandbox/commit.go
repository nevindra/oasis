package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	oasis "github.com/nevindra/oasis/core"
)

// commitAfterToolCall publishes whatever the tool call that just returned
// wrote, through the transactional capability, before the next tool call
// starts.
//
// It exists because Layer 2 only sees the writes it performs itself.
// file_write and file_edit publish as they go; everything a command writes —
// which is every CLI toolchain an agent uses, and therefore most of the files
// a turn produces — is invisible to the backend until the close-time flush. A
// VM that dies mid-turn takes all of it, and nothing can show the user a
// document appearing while it is being written.
//
// Three deliberate limits:
//
//   - Only mounts whose backend implements TransactionalMount. A plain backend
//     keeps today's prefetch/Put/flush behaviour untouched: without stage and
//     commit, publishing a multi-file change per tool call would be a stream
//     of independent Puts with a read-then-write precondition, which is more
//     writes and more races than doing it once at close.
//
//   - Only Mode.Writable() mounts, and FlushOnClose is not consulted. This is
//     Layer 2 generalised, not Layer 3 moved earlier: a mount that accepts
//     tool-interception writes but declines the close-time scan (athena's
//     nested inputs view is exactly that) is saying where files land, not
//     when. Mode is the flag that decides whether a write reaches the backend
//     at all, so Mode is what this asks.
//
//   - No deletions, whatever MirrorDeletes says. Removing a backend file on
//     the strength of a mid-turn scan means trusting that the file is gone
//     rather than not yet written, not currently being replaced, and not
//     missing because the enumeration was truncated. Getting that wrong
//     deletes a user's document. Deletion stays a close-time decision, where
//     the guest filesystem has stopped moving.
func commitAfterToolCall(ctx context.Context, sb Sandbox, cfg *toolsConfig) error {
	if cfg == nil || cfg.detector == nil || len(cfg.mounts) == 0 {
		return nil
	}
	// One sweep at a time. Two concurrent sweeps would scan the same root,
	// both find the same file, and race to commit it — one of them losing its
	// own precondition and reporting a conflict caused by nobody but itself.
	cfg.commitMu.Lock()
	defer cfg.commitMu.Unlock()

	var errs []error
	for i := range cfg.mounts {
		spec := &cfg.mounts[i]
		if !spec.Mode.Writable() || spec.Backend == nil {
			continue
		}
		tx, ok := AsTransactional(spec.Backend)
		if !ok {
			continue
		}
		if err := commitMount(ctx, sb, cfg, spec, tx); err != nil {
			errs = append(errs, err)
		}
	}
	return joinErrors(errs)
}

// commitMount stages and commits one mount's share of what changed.
//
// Staging is per file and the commit is one call, which is the shape the
// capability was built for: the caller holds one body at a time, and either
// every file the tool call wrote is at the backend or none of it is. A reader
// never sees half of a change an agent made in one step.
func commitMount(ctx context.Context, sb Sandbox, cfg *toolsConfig, spec *MountSpec, tx TransactionalMount) error {
	changed, errs := scanMount(ctx, sb, cfg, spec)
	if len(changed) == 0 {
		// An empty commit is explicitly a no-op rather than an error, so
		// calling it here would be legal. Skipping it saves a round trip on
		// the common case: most tool calls write nothing.
		return joinErrors(errs)
	}

	changes := make([]MountChange, 0, len(changed))
	staged := make([]ChangedFile, 0, len(changed))
	for _, f := range changed {
		key, ok := stripMountPrefix(spec.Path, f.Path)
		if !ok {
			continue
		}
		body := f.Content
		if body == nil {
			// The detector knew the file changed without reading it — the
			// shape a stat-and-hash detector has. Fetch only what is about to
			// be staged.
			var err error
			if body, err = readSandboxFile(ctx, sb, f.Path); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		ref, err := tx.StageContent(ctx, int64(len(body)), bytes.NewReader(body))
		if err != nil {
			errs = append(errs, fmt.Errorf("stage %s/%s: %w", spec.Path, key, err))
			continue
		}
		expect, have := commitPrecondition(cfg.manifest, spec, key)
		changes = append(changes, MountChange{
			Key:      key,
			Content:  ref,
			MimeType: mimeTypeForPath(f.Path),
			Expect:   expect,
			Have:     have,
		})
		staged = append(staged, ChangedFile{Path: f.Path, Size: int64(len(body)), Digest: f.Digest})
	}
	if len(changes) == 0 {
		return joinErrors(errs)
	}

	res, err := tx.Commit(ctx, changes)
	if err != nil {
		// Nothing was applied, so nothing may be recorded about what this
		// commit tried to write: the detector is told nothing, which leaves
		// these files reported again by the next scan. A rejected commit costs
		// a retry, never a lost write.
		//
		// What the rejection *taught* the framework is a separate thing, and it
		// is recorded. See adoptRejectedVersions.
		adoptRejectedVersions(ctx, cfg.manifest, spec, err)
		errs = append(errs, &mountCommitError{spec: spec, err: err})
		return joinErrors(errs)
	}
	if cfg.manifest != nil {
		// The versions the backend just assigned are what the next commit for
		// these keys must assert. Dropping them means conflicting with
		// ourselves on the very next tool call.
		for _, e := range res.Entries {
			cfg.manifest.Record(spec.Path, e.Key, e)
		}
	}
	cfg.detector.Committed(staged)
	return joinErrors(errs)
}

// scanMount asks the detector what changed under one mount, with the mount's
// ownership rules and the framework's beliefs about the backend attached.
func scanMount(ctx context.Context, sb Sandbox, cfg *toolsConfig, spec *MountSpec) ([]ChangedFile, []error) {
	mounts := cfg.mounts
	changed, err := cfg.detector.Scan(ctx, sb, ChangeScan{
		Root: spec.Path,
		Owns: func(p string) bool { return mountOwnsPath(mounts, spec, p) },
		Baseline: func(p string) (MountEntry, bool) {
			if cfg.manifest == nil {
				return MountEntry{}, false
			}
			key, ok := stripMountPrefix(spec.Path, p)
			if !ok {
				return MountEntry{}, false
			}
			return cfg.manifest.Lookup(spec.Path, key)
		},
	})
	if err != nil {
		return changed, []error{err}
	}
	return changed, nil
}

// mountOwnsPath reports whether a file found under spec.Path is spec's to
// commit: the deepest mount covering it, and not filtered out by its globs.
//
// Both halves matter and both already exist. mountIsDeepestFor is the rule
// Layer 2 resolves writes with and the rule the close-time flush was taught in
// WC-1 — a file under a nested mount belongs to the nested mount, not to the
// parent whose prefix it also matches. matchFilters is the same
// Include/Exclude pass prefetch and flush apply, so a path excluded from those
// is excluded from this too: node_modules and __pycache__ are not workspace
// documents just because a commit happens more often than a flush.
//
// Worth knowing before turning per-tool-call commits on: those globs are
// doublestar patterns tested against the key and its basename, so "**" spans
// path segments and "node_modules/**" does exclude "node_modules/pkg/index.js"
// — a mount that believes it excludes a deep tree does. That is what makes
// this affordable per tool call, because the excludes are the only thing
// standing between a full-scan detector and a dependency tree, and they carry
// that weight here on every call rather than once per session.
func mountOwnsPath(specs []MountSpec, spec *MountSpec, fullPath string) bool {
	if !mountIsDeepestFor(specs, spec.Path, fullPath) {
		return false
	}
	key, ok := stripMountPrefix(spec.Path, fullPath)
	if !ok {
		return false
	}
	return matchFilters(key, spec.Include, spec.Exclude)
}

// commitPrecondition picks the claim this framework can honestly make about a
// key, from the only state it has: whether the manifest tracks it, and whether
// this mount was ever read.
//
//   - Tracked with a version — prefetched, published earlier in this session,
//     or learned from a rejection (adoptRejectedVersions). ExpectVersion on
//     that version. The ordinary case, and the one that catches a second
//     writer.
//
//   - Tracked with an empty version. The backend gave no token to assert, so
//     asserting one would be a lie and asserting absence a worse one: the key
//     exists. ExpectAny, which is what Put does with an empty ifVersion today.
//
//   - Untracked on a mount that was prefetched. The framework listed this
//     backend at start and this key was not in it (or was filtered out, in
//     which case it is not being committed either). "Nobody else has this
//     name" is the true claim: ExpectAbsent. It fails if someone created the
//     key in between — which is the point, and is what an unconditional Put
//     silently loses today.
//
//   - Untracked on a mount that was never prefetched — write-only, or
//     PrefetchOnStart=false. The framework has never read this backend and has
//     no idea what is under that key. ExpectAny, exactly as ExpectAny's own
//     doc describes. Claiming absence here would reject every write to a
//     read-through mount whose keys the framework never enumerated.
func commitPrecondition(manifest *Manifest, spec *MountSpec, key string) (VersionExpectation, string) {
	if manifest != nil {
		if ver, tracked := manifest.Version(spec.Path, key); tracked {
			if ver == "" {
				return ExpectAny, ""
			}
			return ExpectVersion, ver
		}
	}
	if spec.Mode.Readable() && spec.PrefetchOnStart {
		return ExpectAbsent, ""
	}
	return ExpectAny, ""
}

// maxConflictStats bounds the extra round trips a single rejection may spend
// learning versions the backend declined to report. A commit can carry a whole
// workspace, and a failure path is the wrong place to discover an unbounded
// loop. Past the bound those keys keep their old precondition and stay
// unwinnable — the message already tells the model to stop after a second
// rejection, so the outcome is a stalled file rather than a hung turn.
const maxConflictStats = 32

// adoptRejectedVersions updates the manifest with what a rejection just
// revealed about the backend.
//
// # Why this exists
//
// Without it the retry loop cannot close. A rejected commit leaves the manifest
// asserting the version the framework already knows is stale, so the model
// re-reads, merges, writes again — and the framework sends the same dead
// precondition, which the backend rejects for the same reason, forever. The
// tool result tells the model to fix something only the framework can fix.
//
// # Why it is safe
//
// Adopting a version is not adopting content. The file inside the sandbox is
// still the model's own version; only the framework's belief about what the
// backend currently holds moves forward. The next commit therefore says "I know
// this key is at v2" and writes the model's bytes over it — an *informed*
// overwrite, and informed is the whole promise: commitConflictNote showed the
// model what changed, named the file, and told it to merge before retrying.
//
// The protection stays live. A third writer landing between the rejection and
// the retry moves the key to v3, and the retry — now asserting v2 — is rejected
// again. What is given up is that nothing forces the model to actually read
// what it was shown. That is the same trade a re-read-then-write loop makes
// everywhere, and the alternative is not a stricter guarantee, it is a loop
// that never terminates.
//
// The three outcomes need three different answers, for the reasons
// conflictStatusOf names:
//
//   - Absent: the key is gone. Forget it, so the next attempt claims the name
//     is free — which is now true — instead of asserting a version of a file
//     that no longer exists.
//   - Want set: the ordinary case. Record it, no round trip.
//   - Want empty and the key present: the backend rejected an ExpectAbsent
//     claim without naming a version. Asking it directly is the only way to
//     learn one, and without it this key can never be written at all.
//
// A recorded entry carries the version and nothing else. Size and mtime from
// before the conflict describe content this key no longer holds, and a stale
// size is worse than an absent one — backendLikelyHolds reads equal size as
// evidence of equal content.
func adoptRejectedVersions(ctx context.Context, manifest *Manifest, spec *MountSpec, err error) {
	if manifest == nil {
		return
	}
	var conflict *CommitConflictError
	if !errors.As(err, &conflict) {
		return
	}

	stats := 0
	for _, c := range conflict.Conflicts {
		switch {
		case c.Absent:
			manifest.Forget(spec.Path, c.Key)
		case c.Want != "":
			manifest.Record(spec.Path, c.Key, MountEntry{Key: c.Key, Version: c.Want})
		case spec.Backend != nil && stats < maxConflictStats:
			stats++
			entry, serr := spec.Backend.Stat(ctx, c.Key)
			if serr != nil {
				// The backend would not say. Leaving the manifest alone is the
				// honest outcome: an invented version would be a claim the
				// framework cannot support, and the key simply stays rejected.
				continue
			}
			manifest.Record(spec.Path, c.Key, entry)
		}
	}
}

// toolExec is the shape of every tool body in this package.
type toolExec = func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error)

// commitAfterWrite wraps a tool whose execution can write anywhere in the
// sandbox, so that what it wrote is at the backend before the model sees the
// result.
//
// With no detector configured it returns the tool body unchanged — not a
// wrapper that checks a flag, the body itself — so a caller that has not
// opted in runs precisely the code it ran before this existed.
//
// The commit runs whatever the tool reported. A command that exits non-zero,
// a script that raised halfway, a call that timed out: all of them may have
// written files first, and the timeout is the case where the work is most
// likely to be lost.
func commitAfterWrite(sb Sandbox, cfg *toolsConfig, exec toolExec) toolExec {
	if cfg == nil || cfg.detector == nil {
		return exec
	}
	return func(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
		res, err := exec(ctx, args)
		if cerr := commitAfterToolCall(ctx, sb, cfg); cerr != nil {
			res = noteCommitFailure(ctx, res, cerr)
		}
		return res, err
	}
}

// mountCommitError names the mount whose commit the backend refused.
//
// *CommitConflictError reports which keys failed but not which backend they
// belong to, and answering a rejection needs both. A key is meaningless to a
// model that only ever sees sandbox paths until it is joined to the mount
// root, and the stored content has to be read back through that mount's own
// backend — the nested-inputs mount and its parent hold different files under
// similar-looking keys. Carrying the spec is what a fmt.Errorf prefix could
// only put in a string.
type mountCommitError struct {
	spec *MountSpec
	err  error
}

func (e *mountCommitError) Error() string {
	return "commit " + e.spec.Path + ": " + e.err.Error()
}

func (e *mountCommitError) Unwrap() error { return e.err }

// noteCommitFailure appends a failed commit to a tool result without turning
// the tool call into a failure. The command did run and its output is real;
// reporting it as an error would have the model run it again, which for a
// build or a migration is worse than the failed commit.
func noteCommitFailure(ctx context.Context, res oasis.ToolResult, err error) oasis.ToolResult {
	note := "[" + commitFailureNote(ctx, err) + "]"
	switch {
	case res.Error != "":
		res.Error += "\n" + note
	case res.Content != "":
		res.Content += "\n\n" + note
	default:
		res.Content = note
	}
	return res
}

// ── explaining a rejected commit to the model ──
//
// The rejection is not the feature; the message is. A model handed "version
// mismatch" retries with the same stale bytes — which fails identically, for
// the same reason, every time — or gives up and tells the user the operation
// failed. Neither is recoverable. What makes it recoverable is being told
// which file moved, what that file holds now, whether it was changed or
// deleted, and to re-read before writing again.
//
// The stored content has to be fetched from the backend, and it is the only
// view of it the model can get. The copy inside the sandbox is the agent's own
// uncommitted write — that is why a commit was attempted at all — so file_read
// on that path returns what the agent wrote, not what the other writer stored.
// The note therefore carries the bytes, and says plainly which copy is which.
//
// A non-conflict failure (the backend is down, the guest could not serve a
// file) gets a different message and no re-read instruction. Nothing changed
// underneath the model, there is nothing for it to re-read, and telling it
// otherwise would send it looking for a second writer that does not exist.

// The bounds on that message. A commit can carry a whole workspace and one
// file can be 50 MB of binary, so every part of the note is capped. It exists
// to make the model act correctly, not to reproduce the workspace in its
// context.
const (
	// maxConflictFilesDetailed is how many rejected files get a block of their
	// own with the stored content inlined. Almost every real conflict is one
	// file; three covers the rest without ever spending more than a few
	// thousand tokens explaining a rejection. Files past it are still named.
	maxConflictFilesDetailed = 3

	// maxConflictFilesNamed is how many rejected files are named at all. Past
	// it the note reports a count: two hundred paths is not something a model
	// can act on, and the action for every one of them is the same.
	maxConflictFilesNamed = 10

	// maxConflictContentBytes is how much of one file's stored content is
	// inlined — roughly a thousand tokens. Longer text is cut here and said to
	// be cut; the head of a document is where its structure is, so a cut
	// preview is still worth more than none. It is also the read bound: the
	// backend is never asked for more than this, so a 50 MB file costs one
	// bounded read rather than a 50 MB transfer.
	maxConflictContentBytes = 4 * 1024

	// maxOtherErrorsInMessage bounds the unrelated failures named alongside a
	// rejection. They are context, not the instruction, and a sweep that
	// failed on twenty temp files must not bury the one thing to act on.
	maxOtherErrorsInMessage = 2
)

// The three things that can have happened to a rejected key, in the vocabulary
// the message uses. They are three labels rather than one because they call
// for three different actions: a changed file is re-read and merged, a deleted
// file is a decision the agent must not make silently, and a taken name is
// somebody else's file that must not be overwritten by a create.
const (
	statusChanged = "CHANGED"
	statusDeleted = "DELETED"
	statusTaken   = "ALREADY EXISTS"
)

// conflictStatusOf reads the status out of the payload, the same way
// CommitConflictError.Error does: Absent means the key is gone, and an empty
// Have means the caller claimed the name was free (ExpectAbsent) and it was
// not.
func conflictStatusOf(c KeyConflict) string {
	switch {
	case c.Absent:
		return statusDeleted
	case c.Have == "":
		return statusTaken
	default:
		return statusChanged
	}
}

// rejectedFile is one conflicted key resolved into what the model needs: the
// path it knows the file by, what happened to it, and where to read it back
// from.
type rejectedFile struct {
	path   string
	status string
	key    string
	spec   *MountSpec
}

func commitFailureNote(ctx context.Context, err error) string {
	rejected, others := splitCommitFailure(err)
	if len(rejected) == 0 {
		return "workspace commit failed: " + err.Error() + "\n" + commitFailureAdvice
	}
	return commitConflictNote(ctx, rejected, others)
}

// commitFailureAdvice is what to say when the commit failed for a reason the
// model did not cause and cannot fix. It carries no re-read instruction on
// purpose: there is nothing to re-read, and the one thing that must not happen
// is the model re-running a build or a migration to "fix" a storage error.
const commitFailureAdvice = "The tool call itself ran and its output above is real — do not re-run it. " +
	"This is a storage failure, not a conflict with another writer: nothing changed underneath you and there is nothing to re-read. " +
	"Your files are still in the sandbox, and the next commit or the flush at the end of the turn will try to save them again. " +
	"If it keeps failing, tell the user instead of repeating the work."

func commitConflictNote(ctx context.Context, rejected []*mountCommitError, others []error) string {
	files := rejectedFiles(rejected)
	if len(files) == 0 {
		// Unreachable: splitCommitFailure only classifies an error as a
		// rejection when it names at least one key. Kept so that a backend
		// that breaks that promise still gets a true message rather than an
		// instruction about nothing.
		return "workspace commit failed: " + rejected[0].Error() + "\n" + commitFailureAdvice
	}

	var b strings.Builder
	b.WriteString("workspace commit failed: ")
	b.WriteString(conflictHeadline(files))
	b.WriteString(" Your write was rejected and nothing was saved.\n")

	detailed := min(len(files), maxConflictFilesDetailed)
	for _, f := range files[:detailed] {
		b.WriteString("\n")
		b.WriteString(f.path)
		b.WriteString(" — ")
		b.WriteString(f.status)
		b.WriteString(". ")
		b.WriteString(describeStoredContent(ctx, f))
		b.WriteString("\n")
	}

	if named := min(len(files), maxConflictFilesNamed); named > detailed {
		b.WriteString("\nAlso rejected, content not shown: ")
		for i, f := range files[detailed:named] {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s (%s)", f.path, f.status)
		}
		b.WriteString(".\n")
		if len(files) > named {
			fmt.Fprintf(&b, "And %d more files were rejected the same way.\n", len(files)-named)
		}
	}

	b.WriteString("\n")
	b.WriteString(conflictInstructions(files))
	if line := otherErrorsLine(others); line != "" {
		b.WriteString("\n")
		b.WriteString(line)
	}
	return b.String()
}

// conflictHeadline states what happened in one clause. The single-file case
// says which of the three things it was, because that is the whole story and
// the model should not have to read on to learn it.
func conflictHeadline(files []rejectedFile) string {
	if len(files) > 1 {
		return fmt.Sprintf("%d of the files you were saving were changed or removed in storage by another writer since you last read them.", len(files))
	}
	f := files[0]
	switch f.status {
	case statusDeleted:
		return fmt.Sprintf("%s was deleted in storage by another writer since you last read it.", f.path)
	case statusTaken:
		return fmt.Sprintf("%s already exists in storage — another writer created it under the name you were claiming.", f.path)
	default:
		return fmt.Sprintf("%s was changed in storage by another writer since you last read it.", f.path)
	}
}

// conflictInstructions is the part of the note that is a prompt rather than a
// report. Only the statuses actually present get a line, so the common case —
// one changed file — reads as two sentences instead of a policy document.
func conflictInstructions(files []rejectedFile) string {
	present := make(map[string]bool, 3)
	for _, f := range files {
		present[f.status] = true
	}

	var b strings.Builder
	b.WriteString("What to do next:\n")
	if present[statusChanged] {
		b.WriteString("- " + statusChanged + ": re-read the file, re-apply your change on top of the stored content shown above, and write it again. Where no content is shown you have not seen what the file holds — inspect it before overwriting.\n")
	}
	if present[statusDeleted] {
		b.WriteString("- " + statusDeleted + ": someone removed this file. Do not recreate it as a side effect of retrying — decide whether recreating it is what the user wants, and ask if you are not sure.\n")
	}
	if present[statusTaken] {
		b.WriteString("- " + statusTaken + ": you were creating this file, but the name is taken by content you did not write. Merge with it or choose another name; do not overwrite it blind.\n")
	}
	b.WriteString("The copy of each file inside the sandbox is your own uncommitted version, not the stored one. ")
	b.WriteString("Do not re-run the tool call above — it already ran and its output is real. ")
	b.WriteString("If the same file is rejected a second time, stop retrying and tell the user it is being changed by someone else.")
	return b.String()
}

// describeStoredContent reads back what the backend holds for one rejected key
// and renders it, bounded, as the sentence that follows the status.
//
// Every failure path here degrades to naming the conflict without the content.
// Losing the bytes costs the model a merge; losing the message would cost it
// the only signal that anything went wrong at all.
func describeStoredContent(ctx context.Context, f rejectedFile) string {
	if f.status == statusDeleted {
		return "The stored file is gone, so there is no current content to merge with."
	}
	if f.spec == nil || f.spec.Backend == nil {
		return "Its current stored content is not available here."
	}
	rc, err := f.spec.Backend.Open(ctx, f.key)
	if err != nil {
		return fmt.Sprintf("Its current stored content could not be read back (%v), so it is not shown here; inspect the file before overwriting it.", err)
	}
	defer rc.Close()

	// Bounded at the read, not after it: the backend is never asked to ship a
	// whole 50 MB file to print four kilobytes of it.
	body, err := io.ReadAll(io.LimitReader(rc, maxConflictContentBytes+1))
	if err != nil {
		return fmt.Sprintf("Its current stored content could not be read back (%v), so it is not shown here; inspect the file before overwriting it.", err)
	}

	truncated := len(body) > maxConflictContentBytes
	if truncated {
		body = trimPartialRune(body[:maxConflictContentBytes])
	}
	if len(body) == 0 {
		return "Its current stored content is empty (0 B) — the file exists but holds nothing."
	}
	if !isDisplayableText(body) {
		return "Its current stored content is binary, not text, so it is not reproduced here — re-read the file with a file tool rather than expecting its bytes in this message."
	}

	marker := f.path
	lead := fmt.Sprintf("Its current stored content (%s):", humanSize(int64(len(body))))
	if truncated {
		marker += " (truncated)"
		lead = fmt.Sprintf("Its current stored content starts like this (first %s; the stored file is longer):", humanSize(int64(len(body))))
	}
	text := string(body)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return lead + "\n--- BEGIN " + marker + " ---\n" + text + "--- END " + marker + " ---"
}

// rejectedFiles flattens every mount's conflicts into one list in the
// vocabulary the message speaks: absolute sandbox paths, not mount keys.
//
// Sorted by path, because the order a backend reports conflicts in is not
// meaningful and the note bounds itself by taking a prefix of this list. An
// unsorted list would show the model three arbitrary files out of twenty, and
// three different ones on the retry.
func rejectedFiles(rejected []*mountCommitError) []rejectedFile {
	var files []rejectedFile
	for _, r := range rejected {
		var cc *CommitConflictError
		if !errors.As(r.err, &cc) {
			continue
		}
		for _, c := range cc.Conflicts {
			files = append(files, rejectedFile{
				path:   path.Join(r.spec.Path, c.Key),
				status: conflictStatusOf(c),
				key:    c.Key,
				spec:   r.spec,
			})
		}
	}
	slices.SortFunc(files, func(a, b rejectedFile) int { return strings.Compare(a.path, b.path) })
	return files
}

// otherErrorsLine mentions failures that were not conflicts but happened in
// the same sweep — an unreadable temp file, a second mount whose backend was
// down. They are reported because dropping them would hide a real failure, and
// bounded because they are not what the model has to act on.
func otherErrorsLine(others []error) string {
	if len(others) == 0 {
		return ""
	}
	msgs := make([]string, 0, maxOtherErrorsInMessage)
	for _, e := range others[:min(len(others), maxOtherErrorsInMessage)] {
		msgs = append(msgs, e.Error())
	}
	line := "The same sweep also reported: " + strings.Join(msgs, "; ")
	if len(others) > maxOtherErrorsInMessage {
		line += fmt.Sprintf("; and %d more", len(others)-maxOtherErrorsInMessage)
	}
	return line + ". Those are not conflicts and need no re-read."
}

// splitCommitFailure separates the rejections the model must answer from the
// failures it cannot do anything about.
//
// joinErrors builds a tree, so this walks it: a join is transparent and
// everything else is a leaf. A mountCommitError that wraps something other
// than a conflict is a plain failure of that mount and lands in others, where
// its Error() still names the mount.
func splitCommitFailure(err error) (rejected []*mountCommitError, others []error) {
	walkJoinedErrors(err, func(e error) {
		if mce, ok := e.(*mountCommitError); ok {
			var cc *CommitConflictError
			if errors.As(mce.err, &cc) && len(cc.Conflicts) > 0 {
				rejected = append(rejected, mce)
				return
			}
		}
		others = append(others, e)
	})
	return rejected, others
}

func walkJoinedErrors(err error, visit func(error)) {
	if err == nil {
		return
	}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			walkJoinedErrors(e, visit)
		}
		return
	}
	visit(err)
}

// isDisplayableText decides whether bytes can go into a prompt at all. A NUL
// byte is legal UTF-8 and is the clearest single sign that a file is not text,
// which is what keeps a conflicted .xlsx out of the model's context.
func isDisplayableText(b []byte) bool {
	return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0
}

// trimPartialRune drops the incomplete rune a byte-bounded cut can leave at
// the end, so a truncated preview is still valid UTF-8 and is not mistaken for
// binary.
func trimPartialRune(b []byte) []byte {
	for range utf8.UTFMax {
		if len(b) == 0 {
			break
		}
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size > 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}
