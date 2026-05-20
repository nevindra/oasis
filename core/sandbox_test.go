package core_test

import "github.com/nevindra/oasis/core"

// Compile-time check: a minimal Sandbox implementation satisfies the interface.
type stubSandbox struct{}

func (stubSandbox) Close() error { return nil }

var _ core.Sandbox = stubSandbox{}
