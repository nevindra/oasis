package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// TransactionalMount is an OPTIONAL capability a FilesystemMount backend MAY
// implement so that a change touching several files lands as one unit, and a
// writer whose reads went stale is rejected instead of silently winning.
//
// The default write path cannot promise either. Every write is its own Put —
// Layer 2 publishes one file per tool call, Layer 3 publishes one file per
// scanned delta — so a two-file change reaches the backend as two independent
// writes: a reader can observe the first without the second, and a failure
// between them leaves the backend holding half a change. Put's ifVersion
// precondition is per-call and, on real backends, implemented as a
// read-then-write, which leaves a window for a second writer to land between
// the check and the write.
//
// This capability replaces that with two phases:
//
//   - StageContent hands the backend the bytes for one file and gets back an
//     opaque handle. Staging is per file, so the caller holds one body in
//     memory at a time rather than the whole change set, and a file whose
//     content the caller can tell is unchanged is never staged at all.
//   - Commit carries the whole change set — each key, what it should become,
//     and the version state the caller believed that key was at — and applies
//     all of it or none of it.
//
// Detection is a type assertion at the call site (see AsTransactional), the
// same shape FilesystemMounter and BrowserSandbox use. A backend that does not
// implement it keeps today's Put/prefetch/flush behaviour exactly; nothing in
// the framework requires the capability, and nothing about it changes what a
// FilesystemMount must do.
//
// Implementations must be safe for concurrent use: the framework may stage and
// commit from several tool calls at once.
//
// # Preconditions are per key, deliberately
//
// A commit carries one precondition per key it changes. It does not carry a
// single token for the mount as a whole, and this is a choice, not an
// oversight.
//
// A whole-mount token would be simpler for a backend that already keeps a
// commit chain, and it would be wrong here. Two agents working in the same
// workspace on different files are not in conflict — one writing report.md
// must not lose to the other writing data.csv — but under a single token every
// commit not built on the very latest state of the whole mount is rejected,
// including commits that touch nothing the other writer touched. That
// serialises unrelated work and turns every concurrent agent into a retry
// loop. Per-key preconditions reject exactly the writers whose own reads went
// stale, and nobody else.
//
// It is also the only state the framework actually holds. Manifest tracks a
// version per (mount, key) because that is what prefetch learns, key by key;
// the framework never observes a mount as a whole and could not compute a
// mount-wide token without inventing one it has no way to keep accurate.
//
// A host is free to record a mount-wide commit chain of its own, for history
// or to undo a whole turn. That is the host's bookkeeping. This interface does
// not ask for it, does not return a commit id, and does not care whether one
// exists — the framework has no meaning to attach to such an id, and an
// exported field it cannot use would be a contract it has to keep forever.
//
// The limit of per-key preconditions is worth naming: they protect the keys in
// the change set, not keys the caller merely read. A commit whose content was
// derived from a file it does not write has no way to declare that dependency.
//
// # What a backend must guarantee
//
// Atomicity is per Commit call and covers exactly the changes in that call.
// Either every change is visible to a later List, Open, or Stat, or none is.
// There is no guarantee across two Commit calls; a caller that needs two
// changes to land together puts them in one commit.
//
// Preconditions are evaluated against the same state the changes are applied
// to. Checking every precondition and then applying the changes is not enough
// if another writer can land in between — the check and the apply must not be
// separable. That separation is the defect this capability exists to remove,
// so a backend that reintroduces it has not implemented this interface.
//
// Staged content is invisible until a commit makes it visible: it belongs to
// no key, and List, Open, and Stat must not report it. A handle stays valid
// until a commit consumes it successfully. A commit that fails leaves every
// handle in it usable, which is what lets a caller answering a conflict
// re-stage only the key that conflicted and reuse the rest. Content that is
// never committed is the backend's to reclaim on its own schedule: the caller
// can die between staging and committing — that mid-turn failure is much of
// the reason this capability exists — so no discard call could be relied on,
// and none is offered.
//
// Staging is not idempotent and is not required to be. Staging identical bytes
// twice may return the same handle or two different ones; the framework never
// compares handles and never infers from one that the backend already holds
// some content. The same handle may appear in more than one change of a single
// commit, which is how one body lands under two keys.
//
// Changes within one commit are unordered — a backend may apply them in any
// order — and no key may appear twice. Two changes to one key would carry
// contradictory preconditions, so a backend should reject that rather than
// pick a winner.
//
// Versions returned in CommitResult live in the same namespace as
// MountEntry.Version from List and Stat, and are what a later commit's
// MountChange.Have is checked against. Without that round trip a caller cannot
// make a second commit to a key it just wrote.
//
// # What a backend may assume about the caller
//
// The size passed to StageContent is exact, the same promise Put makes. Keys
// in a MountChange are logical keys relative to the mount root, the same
// vocabulary Put and MountEntry.Key use. A Commit with no changes is a no-op,
// not an error — the caller may commit unconditionally after every tool call
// and let most of them be empty.
type TransactionalMount interface {
	// StageContent hands the backend the bytes for one file and returns an
	// opaque handle to them. The content belongs to no key until a Commit
	// gives it one, and may never get one.
	//
	// size must be the exact byte length of data, as with Put. The backend
	// must consume data fully before returning and must not retain the
	// reader — the caller is free to reuse the underlying buffer afterwards.
	//
	// No key is passed, because content and name are separate things here: the
	// same bytes may be committed under several keys, and the key a change
	// targets is not known to be final while it is being staged.
	StageContent(ctx context.Context, size int64, data io.Reader) (StagedContent, error)

	// Commit applies every change in changes, or none of them.
	//
	// Each change carries its own precondition. If any precondition fails the
	// commit is rejected whole and the backend is left exactly as it was, with
	// a *CommitConflictError naming every key that failed. Any other failure
	// returns an ordinary error and must likewise leave nothing applied.
	//
	// An empty or nil changes slice is a no-op that returns a zero
	// CommitResult and a nil error.
	Commit(ctx context.Context, changes []MountChange) (CommitResult, error)
}

// StagedContent is an opaque handle to content a backend has accepted but has
// not yet made visible under any key.
//
// Only the backend that minted a handle can interpret it. The framework
// carries it from StageContent to Commit and does nothing else with it: it
// never parses one, compares two, or persists one beyond the sandbox session.
// That opacity is what keeps this interface storage-agnostic — a backend may
// make the handle a content hash, a temporary key, a row id, or an upload id,
// and may change its mind later without the framework noticing.
type StagedContent string

// ChangeOp is what a MountChange does to its key.
type ChangeOp int

const (
	// OpPut writes Content to Key, creating or replacing whatever is there.
	// The zero value, because a change set is nearly always writes.
	OpPut ChangeOp = iota
	// OpDelete removes Key. Content must be empty.
	OpDelete
)

// VersionExpectation is the precondition a MountChange places on its key: what
// the caller believed the backend held for that key when it last read it.
//
// The zero value, ExpectAny, is unconditional — the same meaning Put gives an
// empty ifVersion — so a caller moving a write from Put to Commit keeps
// today's semantics by leaving the field alone.
type VersionExpectation int

const (
	// ExpectAny applies the change whatever the backend currently holds, and
	// can never produce a conflict. Use it when the caller has no belief to
	// assert: a write-only mount is never prefetched, so the framework has
	// never read it and cannot honestly claim to know what is there.
	ExpectAny VersionExpectation = iota

	// ExpectVersion requires the backend's current version of the key to equal
	// MountChange.Have. The ordinary case for a file the framework prefetched
	// or last wrote itself, and the one that catches a second writer.
	ExpectVersion

	// ExpectAbsent requires the key not to exist. The honest precondition for
	// a file the agent has just created: the claim being made is "nobody else
	// has this name", and it fails if the key exists whatever its version.
	//
	// It is deliberately distinct from ExpectAny, which is what Put collapses
	// this case into today: an unconditional write of a "new" file silently
	// overwrites a file someone else created under the same name in between.
	ExpectAbsent
)

// MountChange is one key's share of a commit: what that key should become, and
// what the caller believed it was.
type MountChange struct {
	// Key is the logical key relative to the mount root — the same vocabulary
	// FilesystemMount.Put and MountEntry.Key use.
	Key string

	// Op selects write (the zero value) or delete.
	Op ChangeOp

	// Content is the handle StageContent returned for the bytes this key
	// should hold. Required for OpPut, and must be empty for OpDelete.
	Content StagedContent

	// MimeType is the type to record for Key, best-effort, as with Put.
	//
	// It travels with the key rather than with the staged content because the
	// framework derives it from the key's extension: the same bytes committed
	// under two names can legitimately carry two types.
	MimeType string

	// Expect selects which precondition applies to this key. Have carries the
	// version for ExpectVersion and is ignored otherwise.
	//
	// The precondition covers this key alone. A commit is rejected whole if
	// any of its keys fail, but keys the commit does not name are neither
	// checked nor affected — see the interface doc for why that is the
	// deliberate scope.
	Expect VersionExpectation
	Have   string
}

// CommitResult reports what a successful commit produced.
type CommitResult struct {
	// Entries holds one MountEntry per OpPut change, carrying the version the
	// backend assigned to that key. Deletions produce no entry.
	//
	// The caller needs these to keep its Manifest current: the version
	// recorded here is what the next commit for that key must send as
	// MountChange.Have. A caller that drops them sends a stale precondition
	// next time and conflicts with itself.
	Entries []MountEntry
}

// ErrCommitConflict is the sentinel returned (wrapped in CommitConflictError)
// when a Commit is rejected because at least one key's precondition failed.
var ErrCommitConflict = errors.New("commit conflict")

// KeyConflict describes one key whose precondition failed, in enough detail
// for the caller to explain the rejection to whoever caused it and retry.
type KeyConflict struct {
	// Key is the logical key that failed its precondition.
	Key string

	// Have is the version the caller asserted, echoing MountChange.Have. Empty
	// when the change asserted ExpectAbsent, since that claim names no version.
	Have string

	// Want is the version the backend holds now — what a retry must be built
	// on. Empty when Absent is true, or when the backend cannot report a
	// version for the key.
	Want string

	// Absent reports that the backend has no entry for Key at all. It
	// distinguishes "someone else changed this file" from "someone else
	// deleted it", which are different things to tell a caller: one is a
	// re-read, the other is a decision.
	Absent bool
}

// CommitConflictError reports a Commit rejected because the caller's beliefs
// about one or more keys turned out to be stale. Nothing in the commit was
// applied.
//
// It matches both ErrCommitConflict and ErrVersionMismatch under errors.Is: a
// commit conflict is a version mismatch, only plural, and callers that already
// branch on ErrVersionMismatch keep working when a backend gains this
// capability. Use errors.As to reach the per-key detail. Unwrap returns the
// backend's own error (Cause) rather than either sentinel, the same split
// VersionMismatchError makes.
type CommitConflictError struct {
	// Conflicts names the keys whose preconditions failed. It always holds at
	// least one entry, and should hold every key that failed — a backend that
	// reports only the first leaves the caller re-reading one file per retry.
	Conflicts []KeyConflict

	// Cause is the backend's own error, if it has one to give.
	Cause error
}

// maxConflictsInMessage bounds how many keys an error string names. A commit
// can carry an entire workspace, and an error message is not the place to
// render one — the full set is in Conflicts.
const maxConflictsInMessage = 4

func (e *CommitConflictError) Error() string {
	if len(e.Conflicts) == 0 {
		return "commit conflict"
	}
	var b strings.Builder
	noun := "keys"
	if len(e.Conflicts) == 1 {
		noun = "key"
	}
	fmt.Fprintf(&b, "commit rejected: %d conflicting %s: ", len(e.Conflicts), noun)
	for i, c := range e.Conflicts {
		if i == maxConflictsInMessage {
			fmt.Fprintf(&b, ", and %d more", len(e.Conflicts)-maxConflictsInMessage)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		switch {
		case c.Absent:
			fmt.Fprintf(&b, "%q (have %q, backend no longer has it)", c.Key, c.Have)
		case c.Have == "":
			fmt.Fprintf(&b, "%q (expected absent, backend has %q)", c.Key, c.Want)
		default:
			fmt.Fprintf(&b, "%q (have %q, backend has %q)", c.Key, c.Have, c.Want)
		}
	}
	return b.String()
}

func (e *CommitConflictError) Is(target error) bool {
	return target == ErrCommitConflict || target == ErrVersionMismatch
}

func (e *CommitConflictError) Unwrap() error {
	return e.Cause
}

// AsTransactional returns backend's TransactionalMount capability, or
// (nil, false) if the backend does not implement it. A nil backend is not
// transactional.
//
// It exists to give the detection one name and one documented meaning, so that
// every caller asks the same question the same way rather than open-coding an
// assertion whose failure mode (a typed-nil backend that asserts true and
// panics on use) is easy to get wrong quietly.
//
// The answer is about the backend only. Whether the caller is allowed to write
// through this mount at all is a separate question the mount's Mode answers,
// and callers must still ask it: a read-only mount's backend may well
// implement this capability, and committing through one anyway would be a bug
// this function cannot see.
func AsTransactional(backend FilesystemMount) (TransactionalMount, bool) {
	if backend == nil {
		return nil, false
	}
	tx, ok := backend.(TransactionalMount)
	return tx, ok
}
