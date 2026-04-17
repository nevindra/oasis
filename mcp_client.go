package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/oasis/mcp"
)

// MCPRegistry is the per-process or per-agent owner of MCP server connections.
// Public for use with WithSharedMCPRegistry.
type MCPRegistry struct {
	mu       sync.RWMutex
	servers  map[string]*mcpServerEntry
	handler  MCPLifecycleHandler // never nil; Noop default
	eventsCh chan MCPEvent       // buffered 64, drop-oldest
	logger   *slog.Logger
	toolReg  *ToolRegistry
	defer_   *deferConfig // nil = deferred mode off (Plan α-2)
}

// SetDeferredMode configures deferred MCP-schema behavior. Call BEFORE
// registering servers so newly-registered tools respect the mode. Typically
// invoked by NewLLMAgent when WithDeferredSchemas was passed.
func (r *MCPRegistry) SetDeferredMode(cfg *deferConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defer_ = cfg
}

// isDeferred reports whether the named server's tools should be registered
// without their input schemas.
func (r *MCPRegistry) isDeferred(serverName string) bool {
	r.mu.RLock()
	cfg := r.defer_
	r.mu.RUnlock()
	if cfg == nil || !cfg.enabled {
		return false
	}
	if cfg.exclude[serverName] {
		return false
	}
	return true
}

// MCPController is the user-facing controller returned by (*LLMAgent).MCP().
// Currently a thin wrapper around *MCPRegistry but kept as a distinct type to
// allow future per-agent scoping (e.g., view-filtered registries).
type MCPController struct {
	reg *MCPRegistry
}

// internal entry per server.
type mcpServerEntry struct {
	cfg         MCPServerConfig
	client      mcp.Client
	state       atomic.Int32 // MCPServerState
	info        MCPServerInfo
	tools       map[string]*mcpToolEntry
	toolsMu     sync.RWMutex
	backoff     *backoffState
	callMu      sync.Mutex // FIFO per server
	reconnectMu sync.Mutex
	cancelCtx   context.CancelFunc
	parentCtx   context.Context
	lastErr     atomic.Value
	connectAt   atomic.Int64
	parent      *MCPRegistry
}

// reconnectLoop runs in its own goroutine. Attempts up to reconnectMaxAttempts
// reconnects with exponential backoff + jitter; on success transitions to Healthy.
// On failure exhaustion, transitions to Dead.
func (e *mcpServerEntry) reconnectLoop() {
	e.reconnectMu.Lock()
	defer e.reconnectMu.Unlock()
	if e.parentCtx.Err() != nil {
		return
	}

	e.parent.emit(MCPEvent{Type: MCPEventReconnecting, Server: e.cfg.mcpServerName(), Timestamp: time.Now()})

	e.backoff.attempts = 0
	for e.backoff.attempts < reconnectMaxAttempts {
		select {
		case <-e.parentCtx.Done():
			return
		default:
		}

		delay := nextBackoff(e.backoff)
		select {
		case <-time.After(delay):
		case <-e.parentCtx.Done():
			return
		}

		if e.attemptReconnect() {
			return
		}
		e.backoff.attempts++
	}
	e.state.Store(int32(MCPStateDead))
	if e.parent.logger != nil {
		e.parent.logger.Warn("MCP server dead after max reconnect attempts",
			"server", e.cfg.mcpServerName())
	}
}

type mcpToolEntry struct {
	serverName string       // "github"
	rawName    string       // "create_issue"
	aliasName  string       // "" or override
	fullName   string       // "mcp__github__create_issue"
	def        atomic.Pointer[ToolDefinition]
	schema     atomic.Value // json.RawMessage; cached schema for deferred tools (Plan α-2)
	schemaMu   sync.Mutex   // serializes lazy fetch
}

type backoffState struct {
	attempts    int
	lastAttempt int64 // unix nano
}

// mcpError wraps an error for consistent storage in atomic.Value.
// atomic.Value requires every Store call to use the same concrete type.
type mcpError struct{ err error }

func storeMCPError(v *atomic.Value, err error) {
	v.Store(mcpError{err: err})
}

func loadMCPError(v *atomic.Value) error {
	if x := v.Load(); x != nil {
		if me, ok := x.(mcpError); ok {
			return me.err
		}
	}
	return nil
}

// mapMCPResult converts mcp.CallToolResult to oasis.ToolResult.
// MCP server-reported errors (IsError=true) become ToolResult.Error.
func mapMCPResult(r *mcp.CallToolResult) *ToolResult {
	if r == nil {
		return &ToolResult{Error: "MCP returned nil result"}
	}
	var content string
	for _, block := range r.Content {
		if block.Type == "text" {
			if content != "" {
				content += "\n"
			}
			content += block.Text
		}
		// image/resource blocks: TODO Plan α-2 or follow-up — for now just text.
	}
	if r.IsError {
		return &ToolResult{Error: content}
	}
	return &ToolResult{Content: content}
}

func mapMCPResultToPublic(r *mcp.CallToolResult) *MCPToolResult {
	if r == nil {
		return nil
	}
	out := &MCPToolResult{IsError: r.IsError, Meta: r.Meta}
	for _, b := range r.Content {
		out.Content = append(out.Content, MCPContent{
			Type:     b.Type,
			Text:     b.Text,
			Data:     b.Data,
			MimeType: b.MimeType,
			URI:      b.URI,
		})
	}
	return out
}

func (r *MCPRegistry) fireOnToolCall(server, tool string, args json.RawMessage) {
	defer func() { _ = recover() }() // user code; never crash registry
	r.handler.OnToolCall(server, tool, args)
	r.emit(MCPEvent{Type: MCPEventToolCall, Server: server, Tool: tool, Timestamp: time.Now()})
}

func (r *MCPRegistry) fireOnToolResult(server, tool string, result *MCPToolResult, err error) {
	defer func() { _ = recover() }()
	r.handler.OnToolResult(server, tool, result, err)
	r.emit(MCPEvent{Type: MCPEventToolResult, Server: server, Tool: tool, Err: err, Timestamp: time.Now()})
}

func (r *MCPRegistry) emit(e MCPEvent) {
	select {
	case r.eventsCh <- e:
	default:
		// Buffer full — drop oldest, retry once.
		select {
		case <-r.eventsCh:
		default:
		}
		select {
		case r.eventsCh <- e:
		default:
		}
	}
}

// markUnhealthy is called by tool wrappers on transport errors.
// Triggers reconnect goroutine if not already running.
func (r *MCPRegistry) markUnhealthy(name string, err error) {
	r.mu.RLock()
	entry, ok := r.servers[name]
	r.mu.RUnlock()
	if !ok {
		return
	}
	storeMCPError(&entry.lastErr, err)
	if entry.state.CompareAndSwap(int32(MCPStateHealthy), int32(MCPStateReconnecting)) {
		go entry.reconnectLoop()
	}
}

// ---- Task 10.1: Register / Unregister / List ----

const (
	initializeTimeout    = 10 * time.Second
	reconnectBaseDelay   = 500 * time.Millisecond
	reconnectMaxDelay    = 30 * time.Second
	reconnectMaxAttempts = 10
)

// ErrServerNotFound is returned when a named MCP server is not registered.
var ErrServerNotFound = errors.New("MCP server not found")

// ErrServerExists is returned when a server with the same name is already registered.
var ErrServerExists = errors.New("MCP server already registered")

// NewSharedMCPRegistry constructs a registry intended to be shared across
// multiple agents in the same process via WithSharedMCPRegistry option.
func NewSharedMCPRegistry() *MCPRegistry {
	return &MCPRegistry{
		servers:  make(map[string]*mcpServerEntry),
		handler:  NoopMCPLifecycle{},
		eventsCh: make(chan MCPEvent, 64),
		logger:   slog.Default(),
		toolReg:  NewToolRegistry(),
	}
}

// SetLifecycleHandler replaces the lifecycle handler. Pass nil to reset to noop.
func (r *MCPRegistry) SetLifecycleHandler(h MCPLifecycleHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h == nil {
		h = NoopMCPLifecycle{}
	}
	r.handler = h
}

// Register connects to an MCP server, fetches its tool list, and registers
// each tool into the underlying ToolRegistry with namespacing.
func (r *MCPRegistry) Register(ctx context.Context, cfg MCPServerConfig) error {
	if cfg.mcpServerName() == "" {
		return errors.New("MCP server config: Name required")
	}
	// Disabled = silent skip (intentional: lets users keep config but toggle).
	if disabled(cfg) {
		return nil
	}

	client, err := buildClient(cfg)
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}
	return r.registerWithClient(ctx, cfg, client)
}

// registerWithClient is the test seam: accepts a pre-constructed mcp.Client.
func (r *MCPRegistry) registerWithClient(ctx context.Context, cfg MCPServerConfig, client mcp.Client) error {
	name := cfg.mcpServerName()

	r.mu.Lock()
	if _, exists := r.servers[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrServerExists, name)
	}
	parentCtx, cancelCtx := context.WithCancel(context.Background())
	entry := &mcpServerEntry{
		cfg:       cfg,
		client:    client,
		tools:     make(map[string]*mcpToolEntry),
		backoff:   &backoffState{},
		cancelCtx: cancelCtx,
		parentCtx: parentCtx,
		parent:    r,
	}
	entry.state.Store(int32(MCPStateConnecting))
	r.servers[name] = entry
	r.mu.Unlock()

	initCtx, cancelInit := context.WithTimeout(ctx, initializeTimeout)
	defer cancelInit()

	info, err := client.Initialize(initCtx)
	if err != nil {
		r.failRegister(name, entry, err)
		return fmt.Errorf("initialize %q: %w", name, err)
	}
	list, err := client.ListTools(initCtx)
	if err != nil {
		r.failRegister(name, entry, err)
		return fmt.Errorf("list tools %q: %w", name, err)
	}

	if info != nil {
		entry.info = MCPServerInfo{
			Name:            info.ServerInfo.Name,
			Version:         info.ServerInfo.Version,
			ProtocolVersion: info.ProtocolVersion,
			Capabilities:    info.Capabilities,
		}
	}

	if err := r.registerTools(entry, list.Tools); err != nil {
		r.failRegister(name, entry, err)
		return err
	}

	entry.state.Store(int32(MCPStateHealthy))
	entry.connectAt.Store(time.Now().UnixNano())

	// Wire disconnect callback for reconnect.
	client.OnDisconnect(func(disconnectErr error) {
		r.markUnhealthy(name, disconnectErr)
	})

	// Lifecycle dispatch (panic-safe).
	func() {
		defer func() { _ = recover() }()
		r.handler.OnConnect(name, entry.info)
	}()
	r.emit(MCPEvent{Type: MCPEventConnected, Server: name, Timestamp: time.Now()})

	return nil
}

func (r *MCPRegistry) failRegister(name string, entry *mcpServerEntry, err error) {
	entry.state.Store(int32(MCPStateDead))
	storeMCPError(&entry.lastErr, err)
	r.mu.Lock()
	delete(r.servers, name)
	r.mu.Unlock()
	if r.logger != nil {
		r.logger.Warn("MCP register failed", "server", name, "err", err)
	}
}

func (r *MCPRegistry) registerTools(entry *mcpServerEntry, tools []mcp.ToolDefinition) error {
	serverName := entry.cfg.mcpServerName()
	filter := getFilter(entry.cfg)
	aliases := getAliases(entry.cfg)

	if filter != nil && len(filter.Include) > 0 && len(filter.Exclude) > 0 {
		return errors.New("MCPToolFilter: Include and Exclude are mutually exclusive")
	}

	deferred := r.isDeferred(serverName)

	entry.toolsMu.Lock()
	defer entry.toolsMu.Unlock()

	for _, t := range tools {
		if filter != nil && !filter.matches(t.Name) {
			continue
		}
		shortName := t.Name
		if alias, ok := aliases[t.Name]; ok && alias != "" {
			shortName = alias
		}
		fullName := "mcp__" + serverName + "__" + shortName

		// Collision check: don't overwrite a tool from another server.
		if _, exists := r.toolReg.index[fullName]; exists {
			if r.logger != nil {
				r.logger.Warn("MCP tool name collision; skipping", "tool", fullName, "server", serverName)
			}
			continue
		}

		// Marshal InputSchema (any) to json.RawMessage for oasis.ToolDefinition.Parameters.
		var params json.RawMessage
		if t.InputSchema != nil {
			if raw, ok := t.InputSchema.(json.RawMessage); ok {
				params = raw
			} else {
				if b, merr := json.Marshal(t.InputSchema); merr == nil {
					params = b
				}
			}
		}

		toolEntry := &mcpToolEntry{
			serverName: serverName,
			rawName:    t.Name,
			aliasName:  shortName,
			fullName:   fullName,
		}
		def := ToolDefinition{Name: fullName, Description: t.Description}
		if deferred {
			// Hide the schema from Definitions(); cache it so EnsureSchema can
			// restore it without re-fetching from the server.
			if len(params) > 0 {
				toolEntry.schema.Store(params)
			}
		} else {
			def.Parameters = params
		}
		toolEntry.def.Store(&def)
		entry.tools[shortName] = toolEntry
		r.toolReg.Add(&mcpToolWrapper{entry: toolEntry, server: entry, parent: r})
	}
	return nil
}

// matches returns true if the raw tool name passes filter rules.
// Include = whitelist; Exclude = blacklist; mutual exclusion enforced upstream.
func (f *MCPToolFilter) matches(rawName string) bool {
	if len(f.Include) > 0 {
		for _, p := range f.Include {
			if globMatch(p, rawName) {
				return true
			}
		}
		return false
	}
	for _, p := range f.Exclude {
		if globMatch(p, rawName) {
			return false
		}
	}
	return true
}

// globMatch supports "*" wildcards using path/filepath.Match semantics.
func globMatch(pattern, name string) bool {
	ok, _ := filepath.Match(pattern, name)
	return ok
}

func disabled(cfg MCPServerConfig) bool {
	switch c := cfg.(type) {
	case StdioMCPConfig:
		return c.Disabled
	case HTTPMCPConfig:
		return c.Disabled
	}
	return false
}

func getFilter(cfg MCPServerConfig) *MCPToolFilter {
	switch c := cfg.(type) {
	case StdioMCPConfig:
		return c.Filter
	case HTTPMCPConfig:
		return c.Filter
	}
	return nil
}

func getAliases(cfg MCPServerConfig) map[string]string {
	switch c := cfg.(type) {
	case StdioMCPConfig:
		return c.Aliases
	case HTTPMCPConfig:
		return c.Aliases
	}
	return nil
}

func buildClient(cfg MCPServerConfig) (mcp.Client, error) {
	switch c := cfg.(type) {
	case StdioMCPConfig:
		cmd := exec.Command(c.Command, c.Args...)
		cmd.Env = append(os.Environ(), envSliceFromMap(c.Env)...)
		if c.WorkDir != "" {
			cmd.Dir = c.WorkDir
		}
		return mcp.NewStdioClient(cmd)
	case HTTPMCPConfig:
		timeout := c.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		return mcp.NewHTTPClient(c.URL, c.Headers, c.Auth, timeout), nil
	default:
		return nil, fmt.Errorf("unknown MCP server config type: %T", cfg)
	}
}

func envSliceFromMap(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// Unregister disconnects and removes the server, cleaning up its tools from the registry.
func (r *MCPRegistry) Unregister(ctx context.Context, name string) error {
	r.mu.Lock()
	entry, ok := r.servers[name]
	if !ok {
		r.mu.Unlock()
		return ErrServerNotFound
	}
	delete(r.servers, name)
	r.mu.Unlock()

	// Mark removed BEFORE close so concurrent calls see state change.
	entry.state.Store(int32(MCPStateDead))
	entry.cancelCtx()

	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	closeErr := entry.client.Close(closeCtx)

	// Remove tools from ToolRegistry.
	entry.toolsMu.RLock()
	toolNames := make([]string, 0, len(entry.tools))
	for _, t := range entry.tools {
		toolNames = append(toolNames, t.fullName)
	}
	entry.toolsMu.RUnlock()
	for _, n := range toolNames {
		_ = r.toolReg.Remove(n)
	}

	func() {
		defer func() { _ = recover() }()
		r.handler.OnDisconnect(name, nil)
	}()
	r.emit(MCPEvent{Type: MCPEventDisconnected, Server: name, Timestamp: time.Now()})

	return closeErr
}

// List returns a snapshot of all registered servers' status.
func (r *MCPRegistry) List() []MCPServerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]MCPServerStatus, 0, len(r.servers))
	for name, e := range r.servers {
		var transport string
		switch e.cfg.(type) {
		case StdioMCPConfig:
			transport = "stdio"
		case HTTPMCPConfig:
			transport = "http"
		}
		lastErr := loadMCPError(&e.lastErr)
		e.toolsMu.RLock()
		toolCount := len(e.tools)
		e.toolsMu.RUnlock()
		out = append(out, MCPServerStatus{
			Name:        name,
			Transport:   transport,
			State:       MCPServerState(e.state.Load()),
			ToolCount:   toolCount,
			LastError:   lastErr,
			ConnectedAt: time.Unix(0, e.connectAt.Load()),
			ServerInfo:  e.info,
		})
	}
	return out
}

// Subscribe returns a channel of lifecycle events. Buffered 64; oldest dropped if full.
func (r *MCPRegistry) Subscribe() <-chan MCPEvent {
	return r.eventsCh
}

// ---- Task 10.2: Reconnect loop helpers ----

func (e *mcpServerEntry) attemptReconnect() bool {
	newClient, err := buildClient(e.cfg)
	if err != nil {
		storeMCPError(&e.lastErr, err)
		return false
	}

	initCtx, cancel := context.WithTimeout(e.parentCtx, initializeTimeout)
	defer cancel()
	if _, err := newClient.Initialize(initCtx); err != nil {
		storeMCPError(&e.lastErr, err)
		_ = newClient.Close(context.Background())
		return false
	}
	list, err := newClient.ListTools(initCtx)
	if err != nil {
		storeMCPError(&e.lastErr, err)
		_ = newClient.Close(context.Background())
		return false
	}

	// Swap client + tools atomically under toolsMu.
	oldClient := e.client
	e.client = newClient

	// Remove old tools, re-register new ones.
	e.toolsMu.Lock()
	for _, t := range e.tools {
		_ = e.parent.toolReg.Remove(t.fullName)
	}
	e.tools = make(map[string]*mcpToolEntry)
	e.toolsMu.Unlock()

	if err := e.parent.registerTools(e, list.Tools); err != nil {
		storeMCPError(&e.lastErr, err)
		_ = newClient.Close(context.Background())
		e.client = oldClient
		return false
	}

	newClient.OnDisconnect(func(disconnectErr error) {
		e.parent.markUnhealthy(e.cfg.mcpServerName(), disconnectErr)
	})

	e.state.Store(int32(MCPStateHealthy))
	e.connectAt.Store(time.Now().UnixNano())
	e.parent.emit(MCPEvent{Type: MCPEventConnected, Server: e.cfg.mcpServerName(), Timestamp: time.Now()})
	if oldClient != nil {
		_ = oldClient.Close(context.Background())
	}
	return true
}

// nextBackoff computes the next backoff duration with exponential growth and ±25% jitter.
func nextBackoff(b *backoffState) time.Duration {
	base := reconnectBaseDelay
	// Shift left by attempts: 500ms, 1s, 2s, 4s, 8s, 16s, 30s (cap)
	for i := 0; i < b.attempts; i++ {
		base *= 2
		if base > reconnectMaxDelay {
			base = reconnectMaxDelay
			break
		}
	}
	// ±25% jitter
	jitterRange := int64(base) / 2 // 50% of d — half as negative, half as positive → ±25%
	var jitter int64
	if jitterRange > 0 {
		jitter = rand.Int63n(jitterRange) - jitterRange/2
	}
	d := time.Duration(int64(base) + jitter)
	if d < 0 {
		d = 0
	}
	return d
}

// ---- Task 10.3: Reconnect / Reload / GetTool ----

// Reconnect manually triggers a reconnect attempt on a server that may be Dead
// (after exhausted backoff). Returns immediately; reconnect runs in background.
func (r *MCPRegistry) Reconnect(ctx context.Context, name string) error {
	r.mu.RLock()
	entry, ok := r.servers[name]
	r.mu.RUnlock()
	if !ok {
		return ErrServerNotFound
	}
	entry.state.Store(int32(MCPStateReconnecting))
	entry.backoff.attempts = 0
	go entry.reconnectLoop()
	return nil
}

// Reload updates a server's config in place by Unregister + Register.
// Brief downtime — caller responsible for any tool-call coordination.
func (r *MCPRegistry) Reload(ctx context.Context, name string, cfg MCPServerConfig) error {
	if err := r.Unregister(ctx, name); err != nil && !errors.Is(err, ErrServerNotFound) {
		return fmt.Errorf("unregister: %w", err)
	}
	return r.Register(ctx, cfg)
}

// GetTool returns the wrapped Tool for direct invocation (testing/debug).
// The tool parameter is the short name (after alias, before mcp__ prefix).
func (r *MCPRegistry) GetTool(server, tool string) (Tool, bool) {
	r.mu.RLock()
	entry, ok := r.servers[server]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	entry.toolsMu.RLock()
	defer entry.toolsMu.RUnlock()
	te, ok := entry.tools[tool]
	if !ok {
		return nil, false
	}
	return &mcpToolWrapper{entry: te, server: entry, parent: r}, true
}

// ---- MCPController methods (Task 11.1) ----

// Register connects to an MCP server and adds it to the agent's registry.
func (c *MCPController) Register(ctx context.Context, cfg MCPServerConfig) error {
	return c.reg.Register(ctx, cfg)
}

// Unregister disconnects and removes the named server from the agent's registry.
func (c *MCPController) Unregister(ctx context.Context, name string) error {
	return c.reg.Unregister(ctx, name)
}

// Reconnect manually triggers a reconnect attempt on a server that may be Dead.
func (c *MCPController) Reconnect(ctx context.Context, name string) error {
	return c.reg.Reconnect(ctx, name)
}

// Reload updates a server's config in place via Unregister + Register.
func (c *MCPController) Reload(ctx context.Context, name string, cfg MCPServerConfig) error {
	return c.reg.Reload(ctx, name, cfg)
}

// GetTool returns the wrapped Tool for the given server + short tool name.
func (c *MCPController) GetTool(server, tool string) (Tool, bool) {
	return c.reg.GetTool(server, tool)
}

// List returns a snapshot of all registered servers' status.
func (c *MCPController) List() []MCPServerStatus {
	return c.reg.List()
}

// Subscribe returns a channel of lifecycle events from the underlying registry.
func (c *MCPController) Subscribe() <-chan MCPEvent {
	return c.reg.Subscribe()
}
