// memory/store_test.go
package memory

import (
	"testing"

	"github.com/nevindra/oasis/core"
)

func TestFilter_ZeroValue(t *testing.T) {
	var f core.MemoryFilter
	if len(f.Kinds) != 0 || f.Scope != nil || f.Pinned != nil || f.Limit != 0 || f.IncludeExp {
		t.Fatalf("zero MemoryFilter not empty: %+v", f)
	}
}

// Compile-time assertion that core.MemoryItemStore is usable as the store interface.
var _ core.MemoryItemStore = (core.MemoryItemStore)(nil)
