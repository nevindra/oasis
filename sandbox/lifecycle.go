package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
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
//
// The basename test is not redundant with "**/*.tmp": it is what lets a
// caller write the short form and mean the obvious thing, and mounts in the
// wild are configured that way. See globMatches for the pattern language.
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

// globMatches tests one glob against one mount key.
//
// The pattern language is doublestar's, which is the shell's: "*" matches
// within a single path segment, "**" spans segments. That is the language
// callers already write their Exclude lists in — "node_modules/**",
// "**/__pycache__/**" — and it is not the language filepath.Match speaks.
// filepath.Match has no multi-segment wildcard at all, so under it
// "node_modules/**" matched "node_modules/pkg" and nothing deeper, and a mount
// that believed it excluded a dependency tree walked and republished the whole
// thing on every flush.
//
// It also matters that keys are slash-separated mount keys rather than host
// paths. filepath.Match splits on the OS separator, which happens to be "/" on
// the platforms this runs on and would quietly stop being right if that ever
// changed; doublestar.Match is defined on "/" regardless.
//
// A malformed pattern matches nothing, and specifically it must not match
// everything. The dangerous direction is Exclude: an exclude that matched
// everything would swallow the whole mount and silently drop every file the
// caller asked to have published. Matching nothing costs an over-eager flush
// of the files the typo meant to skip instead — visible in the backend, and
// recoverable. A malformed Include then lets nothing through rather than
// everything, which is the same choice seen from the other side: an empty
// mount points at the pattern, where an inert filter would look like a mount
// that was never configured with one.
func globMatches(pattern, key string) bool {
	if ok, err := doublestar.Match(pattern, key); err == nil && ok {
		return true
	}
	ok, err := doublestar.Match(pattern, path.Base(key))
	return err == nil && ok
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

// mountIsDeepestFor reports whether mountPath is the most specific mount in
// specs that covers fullPath. A file under a nested mount (e.g. a path
// under "/workspace/inputs") also matches its parent's prefix
// ("/workspace"), so scanning the parent alone isn't enough to tell whose
// file it is. This delegates to findMountForPath (tools.go) for the actual
// boundary check — same rule Layer 2 tool interception uses to resolve
// writes — so flush and tool interception never disagree about which mount
// owns a path.
func mountIsDeepestFor(specs []MountSpec, mountPath, fullPath string) bool {
	owner, _ := findMountForPath(specs, fullPath)
	return owner != nil && owner.Path == mountPath
}

// FlushMounts walks every writeable mount with FlushOnClose=true, scans the
// sandbox under the mount path, and publishes any deltas to the backend.
//
// For each local file:
//   - If new (no manifest entry): unconditional Put.
//   - If changed (manifest entry exists, content differs): conditional Put
//     with the manifest version as precondition. A version mismatch returns
//     a wrapped ErrVersionMismatch.
//   - If unchanged: skip. "Unchanged" is a proof, not a guess — the manifest's
//     version for the key is the sha256 of the bytes now on disk, which only a
//     content-addressed backend can produce. See backendProvenToHold for why
//     nothing weaker is allowed to skip a Put at close time. A file the
//     framework cannot prove unchanged is published, so the fallback is the
//     old unconditional behaviour rather than a dropped write.
//
// Where the sandbox implements FileHasher, those hashes come from the guest in
// one round trip per mount and the unchanged files are never downloaded at
// all. On a prefetched workspace that is most of them, and the transfer is the
// cost — the Put it also avoids is the smaller half. A guest that cannot hash,
// or a hash call that fails for any reason, falls back to downloading every
// candidate and hashing on the host: same outcome, more bytes.
//
// A path covered by a more specific, nested mount spec (e.g. "/workspace/inputs"
// underneath "/workspace") is skipped entirely here — publishing it under
// this mount's backend would duplicate a file the nested mount owns. That
// nested mount flushes it instead, if it has FlushOnClose of its own.
//
// If MirrorDeletes is true, files in the manifest that no longer exist
// locally are deleted from the backend. The same nested-mount check applies:
// a manifest entry whose path now falls under a nested mount is left alone
// rather than deleted, since this spec no longer owns it.
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
		var owned []flushTarget
		for _, fullPath := range res.Files {
			if !mountIsDeepestFor(specs, spec.Path, fullPath) {
				continue
			}
			key, ok := stripMountPrefix(spec.Path, fullPath)
			if !ok {
				continue
			}
			if !matchFilters(key, spec.Include, spec.Exclude) {
				continue
			}
			seen[key] = true
			owned = append(owned, flushTarget{key: key, fullPath: fullPath})
		}

		// Ownership and filters first, hashing second: the guest is asked
		// about the files this mount would actually publish, never about the
		// node_modules tree the excludes just removed.
		guestDigests := hashInGuest(ctx, sb, owned)
		for _, target := range owned {
			entry, _ := manifest.Lookup(spec.Path, target.key)
			if backendProvenToHold(entry, guestDigests[target.fullPath]) {
				continue
			}
			if err := flushOne(ctx, sb, spec, manifest, target.key, target.fullPath); err != nil {
				errs = append(errs, err)
			}
		}

		if spec.MirrorDeletes {
			for _, key := range manifest.Keys(spec.Path) {
				if seen[key] {
					continue
				}
				if !mountIsDeepestFor(specs, spec.Path, path.Join(spec.Path, key)) {
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

// flushTarget is one file that survived the ownership and filter checks and is
// therefore this mount's to publish, unless it turns out the backend already
// holds it. Both halves are kept because the two later steps need different
// ones: the guest is asked about sandbox paths, the backend about mount keys.
type flushTarget struct {
	key      string
	fullPath string
}

// hashInGuest asks the guest for the content hash of every file about to be
// flushed, so a file the backend already holds is recognised without being
// moved across the vsock to prove it. One round trip for the whole mount, a
// few dozen bytes per file.
//
// Every failure is silent and total: a sandbox that does not implement
// FileHasher, an ErrHashUnsupported from one that advertises the capability
// but wraps a guest without it (lazySandbox), a timeout, a broken connection —
// all return a nil map, and a nil map makes every file unknown, which sends
// flush down exactly the download-and-compare path it took before this
// existed. There is no correctness riding on the hashes, only bytes.
//
// A path the guest omitted from the result is unknown too, not unchanged. The
// guest drops paths it could not read (see FileHasher.HashFiles), and a file
// that could not be hashed is the last file to assume anything about.
func hashInGuest(ctx context.Context, sb Sandbox, targets []flushTarget) map[string]string {
	if len(targets) == 0 {
		return nil
	}
	hasher, ok := AsFileHasher(sb)
	if !ok {
		return nil
	}
	paths := make([]string, len(targets))
	for i, t := range targets {
		paths[i] = t.fullPath
	}
	hashes, err := hasher.HashFiles(ctx, paths)
	if err != nil {
		return nil
	}
	digests := make(map[string]string, len(hashes))
	for _, h := range hashes {
		digests[h.Path] = h.Digest
	}
	return digests
}

// backendProvenToHold reports whether the manifest's record of a key is proof
// that the backend is already holding exactly the content digest names.
//
// One comparison, and only one: a non-empty Version equal to the content's
// sha256. A content-addressed backend reports the content hash as the version,
// so that equality is not evidence of sameness but identity — there is nothing
// left to publish. An empty version, or a version in any other scheme (an
// ETag, a generation counter, "v1"), proves nothing, and the file is published.
//
// This is deliberately stricter than backendLikelyHolds in changes.go, which
// also accepts an equal Size. That rule is right where it lives and would be
// wrong here, and the difference is what happens after a wrong guess. The
// per-tool-call detector that wrongly calls a file unchanged sees it again on
// the next tool call, and this flush is its backstop, so the cost of guessing
// wrong is a late publish. Flush has no later pass — it is the last thing that
// runs before the sandbox goes away. A same-length rewrite skipped here is
// never published at all, which is a lost write, so equal size is not enough
// evidence to act on at close time.
func backendProvenToHold(entry MountEntry, digest string) bool {
	return digest != "" && entry.Version != "" && entry.Version == digest
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

	entry, _ := manifest.Lookup(spec.Path, key)

	// The guest either could not hash this file or was never asked; the bytes
	// are here now, so the same proof is available on the host. What it saves
	// is only the Put and the version it would mint — the transfer has already
	// happened — but on a content-addressed backend that Put is a new pointer
	// and a new history entry for a change nobody made.
	if backendProvenToHold(entry, contentDigest(body)) {
		return nil
	}

	newVer, err := spec.Backend.Put(ctx, key, "", int64(len(body)), bytes.NewReader(body), entry.Version)
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
