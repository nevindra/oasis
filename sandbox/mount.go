package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// MountMode declares the direction of data flow for a FilesystemMount.
type MountMode int

const (
	// MountReadOnly: host → sandbox only. Files are prefetched at sandbox
	// start. Writes from inside the sandbox to paths under this mount are
	// allowed locally but never published to the backend.
	MountReadOnly MountMode = iota
	// MountWriteOnly: sandbox → host only. The mount is not prefetched.
	// Writes are published to the backend; reads come from the local
	// sandbox FS without consulting the backend.
	MountWriteOnly
	// MountReadWrite: bidirectional. Files are prefetched at start, writes
	// publish on success, conflicts use optimistic version checks.
	MountReadWrite
)

// Readable reports whether this mode prefetches files into the sandbox.
func (m MountMode) Readable() bool {
	return m == MountReadOnly || m == MountReadWrite
}

// Writeable reports whether this mode publishes writes to the backend.
func (m MountMode) Writeable() bool {
	return m == MountWriteOnly || m == MountReadWrite
}

// FilesystemMount abstracts a key-value-ish file storage backend that can
// back a path inside a sandbox. Implementations are owned by the host
// application (e.g. an S3 client, a local-disk store, a GCS bucket).
//
// All methods take and return logical keys, not absolute backend paths.
// The framework handles translation between sandbox paths and mount keys
// based on the MountSpec the mount is bound to.
type FilesystemMount interface {
	// List returns the entries under the given prefix. The prefix is
	// relative to the mount root. Empty prefix lists everything.
	List(ctx context.Context, prefix string) ([]MountEntry, error)

	// Open returns a reader for the file at key. Caller closes.
	Open(ctx context.Context, key string) (io.ReadCloser, error)

	// Put writes data to the file at key. ifVersion, if non-empty, must
	// match the backend's current version of the file or the put fails
	// with ErrVersionMismatch (wrapped in VersionMismatchError). An
	// empty ifVersion means "create or overwrite unconditionally" — used
	// for newly-created files that did not exist at prefetch.
	//
	// Returns the new version assigned by the backend after the write.
	Put(ctx context.Context, key string, mimeType string, size int64, data io.Reader, ifVersion string) (newVersion string, err error)

	// Delete removes the file at key. ifVersion is honored the same way
	// as Put. Empty ifVersion means unconditional delete.
	Delete(ctx context.Context, key string, ifVersion string) error

	// Stat returns metadata for a single file. Returns an error wrapping
	// ErrNotFound if the key does not exist.
	Stat(ctx context.Context, key string) (MountEntry, error)
}

// MountEntry describes a single file in a FilesystemMount.
type MountEntry struct {
	Key      string    // logical key relative to the mount root
	Size     int64     // bytes
	MimeType string    // best-effort
	Version  string    // backend version token (etag, generation, etc.)
	Modified time.Time // backend modification timestamp
}

// MountSpec attaches a FilesystemMount to a path inside the sandbox and
// declares the lifecycle policy for that mount.
type MountSpec struct {
	// Path is the absolute path inside the sandbox where the mount is
	// rooted (e.g. "/workspace/inputs"). The mount's keys are translated
	// to absolute paths by joining Path with the key.
	Path string

	// Backend is the FilesystemMount implementation that owns the data.
	Backend FilesystemMount

	// Mode declares read-only / write-only / read-write semantics.
	Mode MountMode

	// PrefetchOnStart, when true, causes PrefetchMounts to copy every
	// matching backend entry into the sandbox at start. Only meaningful
	// for readable modes.
	PrefetchOnStart bool

	// FlushOnClose, when true, causes FlushMounts to scan the sandbox
	// at close and publish any deltas. Only meaningful for writeable modes.
	FlushOnClose bool

	// MirrorDeletes, when true, causes FlushMounts to delete backend
	// entries that no longer exist locally. Default false because
	// accidental deletion is much worse than a stale leftover file.
	MirrorDeletes bool

	// Include limits prefetch and flush to keys/paths matching at least
	// one of these globs. Empty means everything.
	Include []string

	// Exclude removes paths matching any of these globs from prefetch
	// and flush, even if they match Include.
	Exclude []string
}

// ErrVersionMismatch is the sentinel returned (wrapped) when a Put or
// Delete fails its precondition check.
var ErrVersionMismatch = errors.New("version mismatch")

// VersionMismatchError carries diagnostic info about a precondition
// failure. Wraps ErrVersionMismatch via Is/Unwrap.
type VersionMismatchError struct {
	Key   string
	Have  string // version the framework had
	Want  string // version the backend reported (empty if unknown)
	Cause error
}

func (e *VersionMismatchError) Error() string {
	if e.Want != "" {
		return fmt.Sprintf("version mismatch on %q: have %q, backend has %q", e.Key, e.Have, e.Want)
	}
	return fmt.Sprintf("version mismatch on %q: have %q", e.Key, e.Have)
}

func (e *VersionMismatchError) Is(target error) bool {
	return target == ErrVersionMismatch
}

func (e *VersionMismatchError) Unwrap() error {
	return e.Cause
}
