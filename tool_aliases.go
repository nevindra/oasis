package oasis

import (
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/tool"
)

// Erase converts a Tool[In, Out] into AnyTool. Thin forwarder to the tool
// subpackage's implementation so existing root-package callers (oasis.Erase[A, B](...))
// continue to compile during the Phase 0 migration. New code should call
// tool.Erase directly.
func Erase[In, Out any](t core.Tool[In, Out]) core.AnyTool {
	return tool.Erase(t)
}
