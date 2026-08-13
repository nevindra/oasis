package sandbox

import (
	"context"
	"testing"
)

// athenaSpecs mirrors athena's internal/adapter/mount_config.go:defaultMountLayout —
// a durable /workspace with a nested, read-through /workspace/inputs view over the
// global files table. Only the parent flushes on close.
func athenaSpecs(workspace, inputs FilesystemMount) []MountSpec {
	return []MountSpec{
		{
			Path:            "/workspace",
			Backend:         workspace,
			Mode:            MountReadWrite,
			PrefetchOnStart: true,
			FlushOnClose:    true,
			Exclude: []string{
				"*.tmp",
				"*.swp",
				"**/__pycache__/**",
				"**/.cache/**",
				"node_modules/**",
			},
		},
		{
			Path:    "/workspace/inputs",
			Backend: inputs,
			Mode:    MountReadWrite,
			// no FlushOnClose: inputs are pulled, not owned
		},
	}
}

// TestFlushDoesNotLeakNestedInputsIntoParentMount guards the scenario athena's
// spreadsheet-generation skill actually runs: fetch_file stages an uploaded
// workbook into /workspace/inputs, then a shell command (python + openpyxl)
// rewrites it in place. Layer 2 tool interception never sees that write, so
// the edit reaches the backend only through FlushMounts at close.
//
// FlushMounts must resolve the deepest mount for each path (the same rule
// findMountForPath uses for tool interception) before publishing, so a file
// under a nested mount is never attributed to the parent's backend.
func TestFlushDoesNotLeakNestedInputsIntoParentMount(t *testing.T) {
	workspace := newFakeMount()
	inputs := newFakeMount()

	// The canonical uploaded file, addressed by file ID (how filesTableMount keys).
	const fileID = "0195f3aa-1111-2222-3333-444455556666"
	inputs.seed(fileID, "ORIGINAL WORKBOOK", "v1")

	sb := newRecordingSandbox()
	// fetch_file staged it under its display name, then python overwrote it.
	sb.files["/workspace/inputs/report.xlsx"] = []byte("EDITED BY OPENPYXL")

	if err := FlushMounts(context.Background(), sb, athenaSpecs(workspace, inputs), NewManifest()); err != nil {
		t.Fatalf("FlushMounts returned an error: %v", err)
	}

	// The parent /workspace backend must receive nothing for a path that
	// belongs to the nested /workspace/inputs mount.
	if got, ok := workspace.entries["inputs/report.xlsx"]; ok {
		t.Errorf("parent /workspace backend received key %q = %q; it should have been skipped in favor of the nested mount",
			"inputs/report.xlsx", string(got.data))
	}

	// The nested mount has FlushOnClose=false, so it doesn't publish either —
	// the edit simply isn't flushed anywhere by this call. What matters is
	// that the canonical uploaded file is untouched rather than shadowed by
	// a spurious duplicate under the parent.
	if e := inputs.entries[fileID]; string(e.data) != "ORIGINAL WORKBOOK" {
		t.Errorf("canonical file %s changed to %q; FlushMounts should not touch a non-flushing mount", fileID, string(e.data))
	}
}

// TestFlushExcludesDoNotCoverNestedMountPaths documents why the parent's
// Exclude list alone can't be relied on to keep nested-mount paths out of
// the parent's flush: it contains nothing that matches
// "/workspace/inputs/..." keys, so the nested-mount check in FlushMounts is
// load-bearing, not redundant with Exclude.
func TestFlushExcludesDoNotCoverNestedMountPaths(t *testing.T) {
	workspace := newFakeMount()
	inputs := newFakeMount()
	specs := athenaSpecs(workspace, inputs)

	for _, key := range []string{
		"inputs/report.xlsx",
		"inputs/sales.csv",
		"inputs/deck.pdf",
	} {
		if !matchFilters(key, specs[0].Include, specs[0].Exclude) {
			t.Errorf("key %q is filtered out by the parent mount's excludes; the nested-mount check would then be unnecessary for this case", key)
		}
	}
}

// TestFlushMountBoundaryDoesNotShadowSiblingPaths is the inverse of the leak
// test: a mount at /workspace/inputs must only shadow paths that are truly
// inside it. A sibling directory like /workspace/inputs2 merely shares a
// string prefix and must still flush to the parent — mirrors the boundary
// check documented on findMountForPath (tools.go).
func TestFlushMountBoundaryDoesNotShadowSiblingPaths(t *testing.T) {
	workspace := newFakeMount()
	inputs := newFakeMount()

	sb := newRecordingSandbox()
	sb.files["/workspace/inputs2/file.txt"] = []byte("sibling, not nested")

	if err := FlushMounts(context.Background(), sb, athenaSpecs(workspace, inputs), NewManifest()); err != nil {
		t.Fatalf("FlushMounts returned an error: %v", err)
	}

	got, ok := workspace.entries["inputs2/file.txt"]
	if !ok {
		t.Fatal("parent /workspace backend did not receive inputs2/file.txt; the boundary check incorrectly treated it as owned by the /workspace/inputs mount")
	}
	if string(got.data) != "sibling, not nested" {
		t.Errorf("inputs2/file.txt content = %q, want %q", string(got.data), "sibling, not nested")
	}
}

// TestFlushMirrorDeletesDoesNotDeleteNestedMountPaths guards the delete side
// of the same bug: a manifest entry recorded against the parent mount for a
// key that now falls under a nested mount must not be mirror-deleted just
// because it no longer shows up in the parent's own (correctly filtered)
// scan. That key isn't the parent's to manage either way.
func TestFlushMirrorDeletesDoesNotDeleteNestedMountPaths(t *testing.T) {
	workspace := newFakeMount()
	inputs := newFakeMount()

	// Represents a backend entry and manifest record left over under the
	// parent mount — e.g. from before nested mounts existed in this layout.
	workspace.seed("inputs/report.xlsx", "leftover under the parent", "v1")
	manifest := NewManifest()
	manifest.Record("/workspace", "inputs/report.xlsx", MountEntry{Key: "inputs/report.xlsx", Version: "v1"})

	sb := newRecordingSandbox()
	// No local file at /workspace/inputs/report.xlsx, so the parent's glob
	// sees it as absent — the exact condition that triggers MirrorDeletes.

	specs := []MountSpec{
		{
			Path:          "/workspace",
			Backend:       workspace,
			Mode:          MountReadWrite,
			FlushOnClose:  true,
			MirrorDeletes: true,
		},
		{
			Path:    "/workspace/inputs",
			Backend: inputs,
			Mode:    MountReadWrite,
		},
	}

	if err := FlushMounts(context.Background(), sb, specs, manifest); err != nil {
		t.Fatalf("FlushMounts returned an error: %v", err)
	}

	if _, ok := workspace.entries["inputs/report.xlsx"]; !ok {
		t.Error("MirrorDeletes removed a backend entry whose local path now belongs to a nested mount; it should have left it alone")
	}
}
