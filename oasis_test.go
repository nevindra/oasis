package oasis_test

import (
	"testing"

	"github.com/nevindra/oasis"
)

// TestUmbrellaReExportsSuspendProtocol asserts that the curated [oasis] umbrella
// re-exports the typed HITL primitives. If any reference fails to resolve, the
// umbrella is missing the re-export.
func TestUmbrellaReExportsSuspendProtocol(t *testing.T) {
	type req struct{ X int }
	type resp struct{ Y int }

	var p oasis.SuspendProtocol[req, resp] = oasis.NewSuspendProtocol[req, resp]("test")
	if p.Name() != "test" {
		t.Errorf("Name() = %q, want %q", p.Name(), "test")
	}
	var _ *oasis.ErrSuspended
}
