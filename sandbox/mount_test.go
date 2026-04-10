package sandbox

import (
	"errors"
	"testing"
)

func TestMountModeDefaultIsReadOnly(t *testing.T) {
	var m MountMode
	if m != MountReadOnly {
		t.Fatalf("zero MountMode = %v, want MountReadOnly", m)
	}
}

func TestMountModeWritable(t *testing.T) {
	cases := []struct {
		mode MountMode
		want bool
	}{
		{MountReadOnly, false},
		{MountWriteOnly, true},
		{MountReadWrite, true},
	}
	for _, c := range cases {
		if got := c.mode.Writable(); got != c.want {
			t.Errorf("MountMode(%d).Writable() = %v, want %v", c.mode, got, c.want)
		}
	}
}

func TestMountModeReadable(t *testing.T) {
	cases := []struct {
		mode MountMode
		want bool
	}{
		{MountReadOnly, true},
		{MountWriteOnly, false},
		{MountReadWrite, true},
	}
	for _, c := range cases {
		if got := c.mode.Readable(); got != c.want {
			t.Errorf("MountMode(%d).Readable() = %v, want %v", c.mode, got, c.want)
		}
	}
}

func TestErrVersionMismatchIs(t *testing.T) {
	wrapped := errors.New("backend rejected: precondition failed")
	err := &VersionMismatchError{Key: "foo.txt", Have: "v1", Want: "v2", Cause: wrapped}
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("errors.Is(VersionMismatchError, ErrVersionMismatch) = false, want true")
	}
	if errors.Unwrap(err) != wrapped {
		t.Fatalf("Unwrap = %v, want %v", errors.Unwrap(err), wrapped)
	}
}
