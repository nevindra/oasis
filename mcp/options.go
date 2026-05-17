package mcp

import (
	"log/slog"
	"sync"

	oasis "github.com/nevindra/oasis"
)

// RegistryOption configures a Registry at construction time.
type RegistryOption func(*Registry)

// WithLogger sets the slog.Logger used by the registry for warnings (registration
// failures, name collisions, reconnect attempts). Defaults to slog.Default().
func WithLogger(l *slog.Logger) RegistryOption {
	return func(r *Registry) { r.logger = l }
}

// WithLifecycleHandler installs a handler that receives connect/disconnect/
// tool-call/tool-result events for every MCP server registered with this
// Registry. Pass nil to reset to no-op.
func WithLifecycleHandler(h LifecycleHandler) RegistryOption {
	return func(r *Registry) {
		if h == nil {
			h = NoopLifecycle{}
		}
		r.handler = h
	}
}

// WithDeferredSchemas opts the registry into deferred schema loading. MCP tools
// are advertised to the LLM by name + description only; their input schemas
// are loaded on-demand via the auto-included ToolSearch tool (returned by
// Tools()).
//
// When enabled, callers should prepend DeferredToolsPromptSection() to the
// agent's system prompt so the model knows how to use ToolSearch.
//
// Trade-off: adds one LLM round-trip per novel MCP tool call, but saves the
// context tokens of all unloaded schemas (~600 tokens/tool average). Worth it
// for setups with 20+ MCP tools; net loss for fewer than 10.
func WithDeferredSchemas(opts ...DeferOption) RegistryOption {
	return func(r *Registry) {
		cfg := &deferConfig{enabled: true}
		for _, o := range opts {
			o(cfg)
		}
		r.defer_ = cfg
	}
}

// NewRegistry constructs a fresh registry. Multiple agents can share one
// registry by passing the same *Registry pointer to each agent's
// WithTools(reg.Tools()...) at construction.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{
		servers:  make(map[string]*serverEntry),
		handler:  NoopLifecycle{},
		eventsCh: make(chan Event, 64),
		logger:   slog.Default(),
		toolList: nil,
		toolMu:   sync.RWMutex{},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Compile-time guard: Registry tools satisfy oasis.AnyTool.
var _ oasis.AnyTool = (*toolWrapper)(nil)
