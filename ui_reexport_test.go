package oasis_test

import (
	"testing"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/core"
)

func TestUIReexports(t *testing.T) {
	if oasis.EventUIComponent != core.EventUIComponent {
		t.Fatal("oasis.EventUIComponent != core.EventUIComponent")
	}

	r := oasis.UIResult("Card", map[string]int{"a": 1})
	if r.UI == nil || r.UI.Name != "Card" {
		t.Fatalf("UIResult.UI = %+v, want Card", r.UI)
	}

	// Type aliases must be usable from the root package.
	var _ oasis.UIComponent
	var _ oasis.UIRenderable
}
