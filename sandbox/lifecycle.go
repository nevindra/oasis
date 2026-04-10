package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	pathfilter "path/filepath"
	"strings"
)

// PrefetchMounts walks every readable mount with PrefetchOnStart=true and
// copies its backend entries into the sandbox's local filesystem. Each
// fetched file's version is recorded in the manifest so that subsequent
// writes can send the correct precondition.
//
// Errors from individual file fetches are aggregated; the function returns
// after attempting all files.
func PrefetchMounts(ctx context.Context, sb Sandbox, specs []MountSpec, manifest *Manifest) error {
	var errs []error
	for _, spec := range specs {
		if !spec.Mode.Readable() || !spec.PrefetchOnStart || spec.Backend == nil {
			continue
		}
		entries, err := spec.Backend.List(ctx, "")
		if err != nil {
			errs = append(errs, fmt.Errorf("list mount %q: %w", spec.Path, err))
			continue
		}
		for _, entry := range entries {
			if !matchFilters(entry.Key, spec.Include, spec.Exclude) {
				continue
			}
			if err := prefetchOne(ctx, sb, spec, entry); err != nil {
				errs = append(errs, fmt.Errorf("prefetch %s/%s: %w", spec.Path, entry.Key, err))
				continue
			}
			manifest.Record(spec.Path, entry.Key, entry)
		}
	}
	if len(errs) > 0 {
		return joinErrors(errs)
	}
	return nil
}

func prefetchOne(ctx context.Context, sb Sandbox, spec MountSpec, entry MountEntry) error {
	rc, err := spec.Backend.Open(ctx, entry.Key)
	if err != nil {
		return err
	}
	defer rc.Close()

	target := path.Join(spec.Path, entry.Key)
	return sb.UploadFile(ctx, target, rc)
}

// matchFilters returns true if key passes the include/exclude globs.
// Empty includes mean "all". Excludes apply after includes. Both the
// full key and its basename are tested against each glob, so patterns
// like "*.tmp" match a key like "sub/dir/file.tmp" via the basename.
func matchFilters(key string, include, exclude []string) bool {
	if len(include) > 0 {
		matched := false
		for _, g := range include {
			if globMatches(g, key) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, g := range exclude {
		if globMatches(g, key) {
			return false
		}
	}
	return true
}

func globMatches(pattern, key string) bool {
	if ok, _ := pathfilter.Match(pattern, key); ok {
		return true
	}
	if ok, _ := pathfilter.Match(pattern, pathfilter.Base(key)); ok {
		return true
	}
	return false
}

// joinErrors aggregates a slice of errors into a single error.
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return &multiError{errs: errs, msg: fmt.Sprintf("%d errors: %v", len(errs), msgs)}
}

type multiError struct {
	errs []error
	msg  string
}

func (m *multiError) Error() string   { return m.msg }
func (m *multiError) Unwrap() []error { return m.errs }

// FlushMounts walks every writeable mount with FlushOnClose=true, scans the
// sandbox under the mount path, and publishes any deltas to the backend.
//
// For each local file:
//   - If new (no manifest entry): unconditional Put.
//   - If changed (manifest entry exists, content differs): conditional Put
//     with the manifest version as precondition. A version mismatch returns
//     a wrapped ErrVersionMismatch.
//   - If unchanged: skip (the local read-back matches the manifest version).
//
// If MirrorDeletes is true, files in the manifest that no longer exist
// locally are deleted from the backend.
//
// Errors are aggregated; the function attempts every file before returning.
func FlushMounts(ctx context.Context, sb Sandbox, specs []MountSpec, manifest *Manifest) error {
	var errs []error
	for _, spec := range specs {
		if !spec.Mode.Writable() || !spec.FlushOnClose || spec.Backend == nil {
			continue
		}

		res, err := sb.GlobFiles(ctx, GlobRequest{Pattern: "**/*", Path: spec.Path})
		if err != nil {
			errs = append(errs, fmt.Errorf("glob mount %q: %w", spec.Path, err))
			continue
		}

		seen := make(map[string]bool)
		for _, fullPath := range res.Files {
			key, ok := stripMountPrefix(spec.Path, fullPath)
			if !ok {
				continue
			}
			if !matchFilters(key, spec.Include, spec.Exclude) {
				continue
			}
			seen[key] = true
			if err := flushOne(ctx, sb, spec, manifest, key, fullPath); err != nil {
				errs = append(errs, err)
			}
		}

		if spec.MirrorDeletes {
			for _, key := range manifest.Keys(spec.Path) {
				if seen[key] {
					continue
				}
				ver, _ := manifest.Version(spec.Path, key)
				if err := spec.Backend.Delete(ctx, key, ver); err != nil {
					errs = append(errs, fmt.Errorf("delete %s/%s: %w", spec.Path, key, err))
					continue
				}
				manifest.Forget(spec.Path, key)
			}
		}
	}
	if len(errs) > 0 {
		return joinErrors(errs)
	}
	return nil
}

func flushOne(ctx context.Context, sb Sandbox, spec MountSpec, manifest *Manifest, key, fullPath string) error {
	rc, err := sb.DownloadFile(ctx, fullPath)
	if err != nil {
		return fmt.Errorf("download %s: %w", fullPath, err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read %s: %w", fullPath, err)
	}

	ver, _ := manifest.Version(spec.Path, key)

	newVer, err := spec.Backend.Put(ctx, key, "", int64(len(body)), bytes.NewReader(body), ver)
	if err != nil {
		return fmt.Errorf("put %s/%s: %w", spec.Path, key, err)
	}
	manifest.Record(spec.Path, key, MountEntry{Key: key, Size: int64(len(body)), Version: newVer})
	return nil
}

func stripMountPrefix(mountPath, fullPath string) (string, bool) {
	if !strings.HasPrefix(fullPath, mountPath) {
		return "", false
	}
	rel := strings.TrimPrefix(fullPath, mountPath)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "", false
	}
	return rel, true
}
