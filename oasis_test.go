package oasis_test

import (
	"testing"

	"github.com/nevindra/oasis"
)

func TestUmbrellaReExportsSuspendProtocol(t *testing.T) {
	// Compilation alone is the assertion — if any of these references
	// fail to resolve, the umbrella is missing the re-export.
	type req struct{ X int }
	type resp struct{ Y int }

	var p oasis.SuspendProtocol[req, resp] = oasis.NewSuspendProtocol[req, resp]("test")
	if p.Name() != "test" {
		t.Errorf("Name() = %q, want %q", p.Name(), "test")
	}
	_ = oasis.Suspend
	var _ *oasis.ErrSuspended
}
