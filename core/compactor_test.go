package core_test

import (
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestCompactRequestDefaultScopeIsFull(t *testing.T) {
	var req core.CompactRequest
	if req.Scope != core.ScopeFull {
		t.Errorf("expected ScopeFull as zero value, got %v", req.Scope)
	}
}

func TestScopeToolResultsOnlyDistinctFromFull(t *testing.T) {
	if core.ScopeFull == core.ScopeToolResultsOnly {
		t.Error("ScopeFull and ScopeToolResultsOnly must be distinct")
	}
}
