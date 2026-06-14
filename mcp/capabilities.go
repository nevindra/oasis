package mcp

import "context"

// Optional-capability interfaces. The core Client interface stays tools-only;
// transports opt into these and the Registry asserts them at runtime
// (PHILOSOPHY: optional capabilities via interface assertion). Unexported
// because the only implementers are framework-owned clients.

// resourceReader is implemented by transports that can list and read resources
// (both stdio and HTTP — request/response).
type resourceReader interface {
	listResources(ctx context.Context) ([]ResourceInfo, error)
	readResource(ctx context.Context, uri string) ([]ResourceContent, error)
}

// resourceSubscriber is implemented only by transports with a persistent read
// loop (stdio) — subscriptions are meaningless over stateless HTTP because no
// notifications can arrive.
type resourceSubscriber interface {
	subscribeResource(ctx context.Context, uri string) error
	unsubscribeResource(ctx context.Context, uri string) error
}

// promptClient is implemented by both stdio and HTTP transports (request/response).
type promptClient interface {
	listPrompts(ctx context.Context) ([]Prompt, error)
	getPrompt(ctx context.Context, name string, args map[string]string) (*PromptResult, error)
}

// logLevelSetter is implemented by both transports. The setLevel request works
// over HTTP, though the resulting log notifications only arrive over stdio.
type logLevelSetter interface {
	setLogLevel(ctx context.Context, level LogLevel) error
}
