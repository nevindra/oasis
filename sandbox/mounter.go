package sandbox

import "context"

// FilesystemMounter is an OPTIONAL capability that a Sandbox implementation
// MAY expose to indicate it can perform live, transparent mounting of a
// FilesystemMount into the running container (e.g. via FUSE, virtio-fs,
// NFS). When a sandbox satisfies this interface, the framework prefers
// the mounter over the default Layer 2 + Layer 3 publish/flush path for
// the specific mount.
//
// No sandbox runtime ships with this capability today. The interface
// exists so that adding live mounting later does not require changes to
// the framework or to applications using mounts.
//
// To opt in: a sandbox implementation embeds the FilesystemMounter
// interface or provides methods that satisfy it. The framework's mount
// machinery checks for this interface via a type assertion before
// falling back to the default path.
type FilesystemMounter interface {
	// MountFilesystem mounts a FilesystemMount into the container at
	// spec.Path. Implementations are responsible for translating the
	// FilesystemMount calls into kernel-level mount operations.
	MountFilesystem(ctx context.Context, spec MountSpec) error

	// UnmountFilesystem releases a previously-mounted filesystem.
	UnmountFilesystem(ctx context.Context, path string) error
}
