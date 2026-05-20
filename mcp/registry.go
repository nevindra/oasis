package mcp

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

	oasis "github.com/nevindra/oasis"
)

// Registry is the per-process or per-agent owner of MCP server connections
// and the tools they expose. Construct via NewRegistry. Multiple agents share
// a registry by being constructed with the same *Registry passed to
// oasis.WithTools(reg.Tools()...).
type Registry struct {
	mu       sync.RWMutex
	servers  map[string]*serverEntry
	handler  LifecycleHandler // never nil; Noop default
	eventsCh chan Event       // buffered 64, drop-oldest
	logger   *slog.Logger
	defer_   *deferConfig // nil = deferred mode off

	// Registry owns its tool list. After extraction, the registry no longer
	// writes through to the agent's *oasis.ToolRegistry; the user dispenses
	// tools to the agent via reg.Tools() at construction time.
	toolMu    sync.RWMutex
	toolList  []oasis.AnyTool
	toolIndex map[string]oasis.AnyTool
}

const (
	initializeTimeout    = 10 * time.Second
	reconnectBaseDelay   = 500 * time.Millisecond
	reconnectMaxDelay    = 30 * time.Second
	reconnectMaxAttempts = 10
)

var ErrServerNotFound = errors.New("MCP server not found")
var ErrServerExists = errors.New("MCP server already registered")

// setDeferredMode is retained as an unexported runtime mutator for the tests
// that re-set the mode after construction. New callers should use the
// WithDeferredSchemas option on NewRegistry.
func (r *Registry) setDeferredMode(cfg *deferConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defer_ = cfg
}

func (r *Registry) isDeferred(serverName string) bool {
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

// serverEntry is internal: one entry per registered server.
type serverEntry struct {
	cfg         ServerConfig
	client      Client
	state       atomic.Int32 // ServerState
	info        ServerMetadata
	tools       map[string]*toolEntry
	toolsMu     sync.RWMutex
	backoff     *backoffState
	callMu      sync.Mutex // FIFO per server
	reconnectMu sync.Mutex
	cancelCtx   context.CancelFunc
	parentCtx   context.Context
	lastErr     atomic.Value
	connectAt   atomic.Int64
	parent      *Registry
}

func (e *serverEntry) reconnectLoop() {
	e.reconnectMu.Lock()
	defer e.reconnectMu.Unlock()
	if e.parentCtx.Err() != nil {
		return
	}
	e.parent.emit(Event{Type: EventReconnecting, Server: e.cfg.serverName(), Timestamp: time.Now()})

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
	e.state.Store(int32(StateDead))
	if e.parent.logger != nil {
		e.parent.logger.Warn("MCP server dead after max reconnect attempts",
			"server", e.cfg.serverName())
	}
}

type toolEntry struct {
	serverName string
	rawName    string
	aliasName  string
	fullName   string
	def        atomic.Pointer[oasis.ToolDefinition]
	schema     atomic.Value // json.RawMessage; cached schema for deferred tools
	schemaMu   sync.Mutex
}

type backoffState struct {
	attempts    int
	lastAttempt int64
}

// mcpError wraps an error for consistent storage in atomic.Value.
type mcpError struct{ err error }

func storeMCPError(v *atomic.Value, err error) { v.Store(mcpError{err: err}) }

func loadMCPError(v *atomic.Value) error {
	if x := v.Load(); x != nil {
		if me, ok := x.(mcpError); ok {
			return me.err
		}
	}
	return nil
}

// mapMCPResult converts mcp.CallToolResult to oasis.ToolResult for the agent
// loop. MCP server-reported errors (IsError=true) become ToolResult.Error.
func mapMCPResult(r *CallToolResult) *oasis.ToolResult {
	if r == nil {
		return &oasis.ToolResult{Error: "MCP returned nil result"}
	}
	var content string
	for _, block := range r.Content {
		if block.Type == "text" {
			if content != "" {
				content += "\n"
			}
			content += block.Text
		}
	}
	if r.IsError {
		return &oasis.ToolResult{Error: content}
	}
	result := oasis.TextResult(content)
	return &result
}

func (r *Registry) fireOnToolCall(server, tool string, args json.RawMessage) {
	defer func() { _ = recover() }()
	r.handler.OnToolCall(server, tool, args)
	r.emit(Event{Type: EventToolCall, Server: server, Tool: tool, Timestamp: time.Now()})
}

func (r *Registry) fireOnToolResult(server, tool string, result *CallToolResult, err error) {
	defer func() { _ = recover() }()
	r.handler.OnToolResult(server, tool, result, err)
	r.emit(Event{Type: EventToolResult, Server: server, Tool: tool, Err: err, Timestamp: time.Now()})
}

func (r *Registry) emit(e Event) {
	select {
	case r.eventsCh <- e:
	default:
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

func (r *Registry) markUnhealthy(name string, err error) {
	r.mu.RLock()
	entry, ok := r.servers[name]
	r.mu.RUnlock()
	if !ok {
		return
	}
	storeMCPError(&entry.lastErr, err)
	if entry.state.CompareAndSwap(int32(StateHealthy), int32(StateReconnecting)) {
		go entry.reconnectLoop()
	}
}

// addTool appends a tool to the registry's internal tool list under toolMu.
// Returns true if added; false if a tool with the same Name() already exists.
func (r *Registry) addTool(t oasis.AnyTool) bool {
	r.toolMu.Lock()
	defer r.toolMu.Unlock()
	if r.toolIndex == nil {
		r.toolIndex = make(map[string]oasis.AnyTool)
	}
	name := t.Name()
	if _, exists := r.toolIndex[name]; exists {
		return false
	}
	r.toolIndex[name] = t
	r.toolList = append(r.toolList, t)
	return true
}

// removeTool deletes a tool by name from the registry's internal list.
func (r *Registry) removeTool(name string) {
	r.toolMu.Lock()
	defer r.toolMu.Unlock()
	if _, ok := r.toolIndex[name]; !ok {
		return
	}
	delete(r.toolIndex, name)
	out := r.toolList[:0]
	for _, t := range r.toolList {
		if t.Name() != name {
			out = append(out, t)
		}
	}
	r.toolList = out
}

// hasTool checks whether a tool name is already registered.
func (r *Registry) hasTool(name string) bool {
	r.toolMu.RLock()
	defer r.toolMu.RUnlock()
	_, ok := r.toolIndex[name]
	return ok
}

// Tools returns a snapshot of all registered tools. When deferred-schema mode
// is on (via WithDeferredSchemas), the snapshot also includes the auto-managed
// ToolSearch tool. The returned slice is decoupled from internal state — safe
// to retain or mutate.
//
// Typical wiring:
//
//	agent := oasis.NewLLMAgent("a", "d", p, oasis.WithTools(reg.Tools()...))
//
// For runtime-changing tool sets (e.g., registering more MCP servers after
// agent construction), prefer:
//
//	agent := oasis.NewLLMAgent("a", "d", p,
//	    oasis.WithDynamicTools(func(_ context.Context, _ oasis.AgentTask) []oasis.AnyTool {
//	        return reg.Tools()
//	    }),
//	)
func (r *Registry) Tools() []oasis.AnyTool {
	r.toolMu.RLock()
	out := make([]oasis.AnyTool, 0, len(r.toolList)+1)
	out = append(out, r.toolList...)
	r.toolMu.RUnlock()

	r.mu.RLock()
	deferOn := r.defer_ != nil && r.defer_.enabled
	r.mu.RUnlock()
	if deferOn {
		out = append(out, newToolSearchTool(r))
	}
	return out
}

// toolDefinitionsForTest returns all tool definitions registered with r.
// Same-package internal helper used by mcp/*_test.go to inspect registry
// state without exposing the internal tool list to consumers.
func (r *Registry) toolDefinitionsForTest() []oasis.ToolDefinition {
	r.toolMu.RLock()
	defer r.toolMu.RUnlock()
	defs := make([]oasis.ToolDefinition, 0, len(r.toolList))
	for _, t := range r.toolList {
		defs = append(defs, t.Definition())
	}
	return defs
}

// Register connects to an MCP server, fetches its tool list, and adds each
// tool to the registry's internal tool list.
func (r *Registry) Register(ctx context.Context, cfg ServerConfig) error {
	if cfg.serverName() == "" {
		return errors.New("MCP server config: Name required")
	}
	if disabled(cfg) {
		return nil
	}
	client, err := buildClient(cfg)
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}
	return r.registerWithClient(ctx, cfg, client)
}

// registerWithClient is the test seam: accepts a pre-constructed Client.
// (Exported lowercase intentionally — package-internal; the test file lives
// in the same package.)
func (r *Registry) registerWithClient(ctx context.Context, cfg ServerConfig, client Client) error {
	name := cfg.serverName()

	r.mu.Lock()
	if _, exists := r.servers[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrServerExists, name)
	}
	parentCtx, cancelCtx := context.WithCancel(context.Background())
	entry := &serverEntry{
		cfg:       cfg,
		client:    client,
		tools:     make(map[string]*toolEntry),
		backoff:   &backoffState{},
		cancelCtx: cancelCtx,
		parentCtx: parentCtx,
		parent:    r,
	}
	entry.state.Store(int32(StateConnecting))
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
		entry.info = ServerMetadata{
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
	entry.state.Store(int32(StateHealthy))
	entry.connectAt.Store(time.Now().UnixNano())

	client.OnDisconnect(func(disconnectErr error) {
		r.markUnhealthy(name, disconnectErr)
	})

	func() {
		defer func() { _ = recover() }()
		r.handler.OnConnect(name, entry.info)
	}()
	r.emit(Event{Type: EventConnected, Server: name, Timestamp: time.Now()})
	return nil
}

func (r *Registry) failRegister(name string, entry *serverEntry, err error) {
	entry.state.Store(int32(StateDead))
	storeMCPError(&entry.lastErr, err)
	r.mu.Lock()
	delete(r.servers, name)
	r.mu.Unlock()
	if r.logger != nil {
		r.logger.Warn("MCP register failed", "server", name, "err", err)
	}
}

func (r *Registry) registerTools(entry *serverEntry, tools []ToolDefinition) error {
	serverName := entry.cfg.serverName()
	filter := getFilter(entry.cfg)
	aliases := getAliases(entry.cfg)

	if filter != nil && len(filter.Include) > 0 && len(filter.Exclude) > 0 {
		return errors.New("ToolFilter: Include and Exclude are mutually exclusive")
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

		// Collision check: only across MCP tools owned by this registry.
		if r.hasTool(fullName) {
			if r.logger != nil {
				r.logger.Warn("MCP tool name collision; skipping", "tool", fullName, "server", serverName)
			}
			continue
		}

		var params json.RawMessage
		if t.InputSchema != nil {
			if raw, ok := t.InputSchema.(json.RawMessage); ok {
				params = raw
			} else if b, merr := json.Marshal(t.InputSchema); merr == nil {
				params = b
			}
		}

		te := &toolEntry{
			serverName: serverName,
			rawName:    t.Name,
			aliasName:  shortName,
			fullName:   fullName,
		}
		def := oasis.ToolDefinition{Name: fullName, Description: t.Description}
		if deferred {
			if len(params) > 0 {
				te.schema.Store(params)
			}
		} else {
			def.Parameters = params
		}
		te.def.Store(&def)
		entry.tools[shortName] = te
		r.addTool(&toolWrapper{entry: te, server: entry, parent: r})
	}
	return nil
}

func (f *ToolFilter) matches(rawName string) bool {
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

func globMatch(pattern, name string) bool {
	ok, _ := filepath.Match(pattern, name)
	return ok
}

func disabled(cfg ServerConfig) bool {
	switch c := cfg.(type) {
	case StdioConfig:
		return c.Disabled
	case HTTPConfig:
		return c.Disabled
	}
	return false
}

func getFilter(cfg ServerConfig) *ToolFilter {
	switch c := cfg.(type) {
	case StdioConfig:
		return c.Filter
	case HTTPConfig:
		return c.Filter
	}
	return nil
}

func getAliases(cfg ServerConfig) map[string]string {
	switch c := cfg.(type) {
	case StdioConfig:
		return c.Aliases
	case HTTPConfig:
		return c.Aliases
	}
	return nil
}

func buildClient(cfg ServerConfig) (Client, error) {
	switch c := cfg.(type) {
	case StdioConfig:
		cmd := exec.Command(c.Command, c.Args...)
		cmd.Env = append(os.Environ(), envSliceFromMap(c.Env)...)
		if c.WorkDir != "" {
			cmd.Dir = c.WorkDir
		}
		return NewStdioClient(cmd)
	case HTTPConfig:
		timeout := c.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		return NewHTTPClient(c.URL, c.Headers, c.Auth, timeout), nil
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

// Unregister disconnects and removes the server, cleaning up its tools.
func (r *Registry) Unregister(ctx context.Context, name string) error {
	r.mu.Lock()
	entry, ok := r.servers[name]
	if !ok {
		r.mu.Unlock()
		return ErrServerNotFound
	}
	delete(r.servers, name)
	r.mu.Unlock()

	entry.state.Store(int32(StateDead))
	entry.cancelCtx()

	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	closeErr := entry.client.Close(closeCtx)

	entry.toolsMu.RLock()
	toolNames := make([]string, 0, len(entry.tools))
	for _, t := range entry.tools {
		toolNames = append(toolNames, t.fullName)
	}
	entry.toolsMu.RUnlock()
	for _, n := range toolNames {
		r.removeTool(n)
	}

	func() {
		defer func() { _ = recover() }()
		r.handler.OnDisconnect(name, nil)
	}()
	r.emit(Event{Type: EventDisconnected, Server: name, Timestamp: time.Now()})
	return closeErr
}

// List returns a snapshot of all registered servers' status.
func (r *Registry) List() []ServerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServerStatus, 0, len(r.servers))
	for name, e := range r.servers {
		var transport string
		switch e.cfg.(type) {
		case StdioConfig:
			transport = "stdio"
		case HTTPConfig:
			transport = "http"
		}
		lastErr := loadMCPError(&e.lastErr)
		e.toolsMu.RLock()
		toolCount := len(e.tools)
		e.toolsMu.RUnlock()
		out = append(out, ServerStatus{
			Name:        name,
			Transport:   transport,
			State:       ServerState(e.state.Load()),
			ToolCount:   toolCount,
			LastError:   lastErr,
			ConnectedAt: time.Unix(0, e.connectAt.Load()),
			Server:      e.info,
		})
	}
	return out
}

// Subscribe returns a channel of lifecycle events. Buffered 64; oldest dropped if full.
func (r *Registry) Subscribe() <-chan Event {
	return r.eventsCh
}

func (e *serverEntry) attemptReconnect() bool {
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

	oldClient := e.client
	e.client = newClient

	e.toolsMu.Lock()
	for _, t := range e.tools {
		e.parent.removeTool(t.fullName)
	}
	e.tools = make(map[string]*toolEntry)
	e.toolsMu.Unlock()

	if err := e.parent.registerTools(e, list.Tools); err != nil {
		storeMCPError(&e.lastErr, err)
		_ = newClient.Close(context.Background())
		e.client = oldClient
		return false
	}

	newClient.OnDisconnect(func(disconnectErr error) {
		e.parent.markUnhealthy(e.cfg.serverName(), disconnectErr)
	})
	e.state.Store(int32(StateHealthy))
	e.connectAt.Store(time.Now().UnixNano())
	e.parent.emit(Event{Type: EventConnected, Server: e.cfg.serverName(), Timestamp: time.Now()})
	if oldClient != nil {
		_ = oldClient.Close(context.Background())
	}
	return true
}

func nextBackoff(b *backoffState) time.Duration {
	base := reconnectBaseDelay
	for i := 0; i < b.attempts; i++ {
		base *= 2
		if base > reconnectMaxDelay {
			base = reconnectMaxDelay
			break
		}
	}
	jitterRange := int64(base) / 2
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

// Reconnect manually triggers a reconnect attempt on a server that may be Dead.
func (r *Registry) Reconnect(ctx context.Context, name string) error {
	r.mu.RLock()
	entry, ok := r.servers[name]
	r.mu.RUnlock()
	if !ok {
		return ErrServerNotFound
	}
	entry.state.Store(int32(StateReconnecting))
	entry.backoff.attempts = 0
	go entry.reconnectLoop()
	return nil
}

// Reload updates a server's config in place by Unregister + Register.
func (r *Registry) Reload(ctx context.Context, name string, cfg ServerConfig) error {
	if err := r.Unregister(ctx, name); err != nil && !errors.Is(err, ErrServerNotFound) {
		return fmt.Errorf("unregister: %w", err)
	}
	return r.Register(ctx, cfg)
}

// GetTool returns the wrapped oasis.AnyTool for direct invocation. The tool
// parameter is the short name (after alias, before mcp__ prefix).
func (r *Registry) GetTool(server, tool string) (oasis.AnyTool, bool) {
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
	return &toolWrapper{entry: te, server: entry, parent: r}, true
}

// RegisterTestClient is a test-only entry point that bypasses transport
// construction. Production code should use Register, which builds the
// transport from a ServerConfig. Intended for use with mcp/mcptest fixtures
// or other in-process Client implementations.
func (r *Registry) RegisterTestClient(ctx context.Context, cfg ServerConfig, client Client) error {
	return r.registerWithClient(ctx, cfg, client)
}
