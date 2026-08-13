package sandbox

import (
	"context"
	"errors"
)

// ErrHashUnsupported is returned by HashFiles when the sandbox runtime behind
// this Sandbox has no hashing operation — an older guest, or a runtime that
// never implemented one.
//
// It is an error rather than a missing method because of Lazy: a lazy sandbox
// cannot know what its inner sandbox supports until something forces it to be
// created, so it must advertise the capability and answer honestly later. Every
// caller therefore has to handle "this failed" anyway, and a caller that treats
// this error the same as a network failure — by falling back to reading the
// bytes — is correct without ever testing for it specifically.
var ErrHashUnsupported = errors.New("sandbox: underlying sandbox cannot hash files")

// FileHasher is an optional Sandbox capability: computing content hashes inside
// the guest, so the host can learn whether a file changed without moving it.
//
// It is a separate interface rather than a Sandbox method because adding a
// method to Sandbox breaks every implementation of it, in this repo and out of
// it. This is the same shape FilesystemMounter and AsTransactional use — detect
// by type assertion, degrade to the slow path when absent.
//
// # Why this is the operation that matters
//
// The host's real question is "which of these files must I upload?", and the
// expensive part of answering it is transferring bytes over vsock. GlobEntry
// metadata narrows the candidates for free but cannot confirm a file is
// unchanged (see its doc). A hash confirms it, costs one round trip for any
// number of files, and moves a few dozen bytes each.
//
// The hash is also the address the content is stored under once a backend is
// content-addressed, so a host that already holds a blob with that hash never
// needs the file's body at all — not to compare it, and not to store it.
type FileHasher interface {
	// HashFiles returns the sha256 of each path's current content, hex-encoded
	// in lower case, computed inside the guest.
	//
	// It is best-effort by the same reasoning as ChangeDetector.Scan: these
	// paths were enumerated a moment ago by a caller that is racing whatever
	// the tool call just started, so a path that has since been deleted,
	// replaced by a directory, or was never readable must not cost the callers
	// the results for every other path. An unreadable path is omitted from the
	// result — it is not an error, and it is not a zero-value entry — so a
	// caller must match results by Path rather than by position, and treat a
	// missing path as unknown rather than as unchanged.
	//
	// A returned error means the operation as a whole failed and no result is
	// usable. ErrHashUnsupported means it can never succeed on this sandbox.
	HashFiles(ctx context.Context, paths []string) ([]FileHash, error)
}

// FileHash is one path's content hash as computed inside the guest.
type FileHash struct {
	// Path is the absolute path inside the sandbox, exactly as it was passed
	// to HashFiles.
	Path string

	// Digest is the lower-case hex sha256 of the file's content — the same
	// scheme the framework hashes with on the host, so a guest digest and a
	// host digest are directly comparable, and so is a content-addressed
	// backend's version token.
	Digest string

	// Size is the byte length the guest hashed. It is the length the digest
	// covers, which is what makes the two consistent with each other even if
	// the file changes immediately afterwards.
	Size int64
}

// AsFileHasher reports whether a Sandbox can hash files inside the guest.
//
// A true result means the call is available, not that it will succeed: a lazy
// sandbox advertises the capability before it knows what it wraps and answers
// ErrHashUnsupported at call time. Callers must handle a failed HashFiles
// regardless.
func AsFileHasher(sb Sandbox) (FileHasher, bool) {
	if sb == nil {
		return nil, false
	}
	h, ok := sb.(FileHasher)
	return h, ok
}
