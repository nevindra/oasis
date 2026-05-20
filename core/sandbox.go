package core

// Sandbox is the minimal interface the agent framework uses to hold a
// reference to a sandbox. Implementations live in satellite packages
// (e.g. github.com/nevindra/oasis/sandbox) so the framework core stays
// free of heavy dependencies like Docker SDKs.
//
// The full sandbox API (shell, file I/O, browser, etc.) is defined in the
// satellite package. The core interface exists solely to give the
// agent.Config.sandbox field a concrete type without importing the satellite.
//
// Used by WithSandbox to attach a sandbox to an agent. Pass any
// implementation that satisfies this interface.
type Sandbox interface {
	// Close releases any resources held by this sandbox instance. Safe to
	// call multiple times. Container lifecycle (stop, remove) is managed
	// by the sandbox manager, not by Close.
	Close() error
}
