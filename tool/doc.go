// Package tool provides helpers for constructing and adapting tools that
// implement the contracts in github.com/nevindra/oasis/core.
//
// The main entry point is Erase, which adapts a type-safe core.Tool[In, Out]
// into a core.AnyTool that the agent loop can dispatch.
package tool
