package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

// StdioClient runs an MCP server as a child process and communicates via
// newline-delimited JSON-RPC over stdin/stdout.
type StdioClient struct {
	cmd          *exec.Cmd // nil when constructed via NewStdioClientFromPipes (testing)
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	framer       *framer
	disconnectFn atomic.Value // func(error)
	onNotify     atomic.Value // notifyFn set by the registry; nil until set
	closeOnce    sync.Once
	roots        []Root

	progressEnabled bool
	progressSeq     atomic.Int64
}

// NewStdioClient spawns a child process and connects pipes.
func NewStdioClient(cmd *exec.Cmd) (*StdioClient, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}
	c := &StdioClient{cmd: cmd, stdin: stdin, stdout: stdout}
	c.framer = newFramer(stdout, stdin)
	c.framer.onNotify(c.handleNotification)
	c.framer.onRequest(c.handleServerRequest)
	go c.watchProcess()
	return c, nil
}

// NewStdioClientFromPipes constructs a StdioClient from existing pipes,
// bypassing exec.Cmd. Useful for testing and in-process MCP servers.
func NewStdioClientFromPipes(r io.ReadCloser, w io.WriteCloser) *StdioClient {
	c := &StdioClient{stdin: w, stdout: r}
	c.framer = newFramer(r, w)
	c.framer.onNotify(c.handleNotification)
	c.framer.onRequest(c.handleServerRequest)
	go c.watchPipesEOF()
	return c
}

func (c *StdioClient) watchProcess() {
	if c.cmd == nil {
		return
	}
	err := c.cmd.Wait()
	c.notifyDisconnect(fmt.Errorf("process exited: %w", err))
}

func (c *StdioClient) watchPipesEOF() {
	// For tests: wait until framer.readLoop signals done, then notify.
	<-c.framer.readDone
	var err error
	if e, _ := c.framer.readErr.Load().(error); e != nil {
		err = e
	}
	if err == nil {
		err = io.EOF
	}
	c.notifyDisconnect(err)
}

func (c *StdioClient) notifyDisconnect(err error) {
	if fn, ok := c.disconnectFn.Load().(func(error)); ok && fn != nil {
		fn(err)
	}
}

// OnDisconnect registers a callback invoked when the server connection is lost.
func (c *StdioClient) OnDisconnect(fn func(error)) {
	c.disconnectFn.Store(fn)
}

// Initialize performs the MCP initialize handshake and returns the server's capabilities.
func (c *StdioClient) Initialize(ctx context.Context) (*InitializeResult, error) {
	caps := `{"tools":{},"resources":{}`
	if len(c.roots) > 0 {
		caps += `,"roots":{"listChanged":false}`
	}
	caps += `}`
	params := json.RawMessage(fmt.Sprintf(`{
        "protocolVersion":%q,
        "capabilities":%s,
        "clientInfo":{"name":"oasis","version":"0.x.0"}
    }`, protocolVersion, caps))
	raw, err := c.framer.Call(ctx, "initialize", params)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	var res InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode initialize result: %w", err)
	}
	// Send "notifications/initialized" handshake completion.
	if err := c.framer.Notify(ctx, "notifications/initialized", json.RawMessage(`{}`)); err != nil {
		return nil, fmt.Errorf("send initialized notification: %w", err)
	}
	return &res, nil
}

// ListTools returns the tools advertised by the MCP server.
func (c *StdioClient) ListTools(ctx context.Context) (*ListToolsResult, error) {
	raw, err := c.framer.Call(ctx, "tools/list", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	var res ListToolsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode list tools: %w", err)
	}
	return &res, nil
}

func (c *StdioClient) setProgressEnabled(on bool) { c.progressEnabled = on }

// toolCallMeta is the optional _meta object carrying a progress token. Encoded
// only when progress is enabled (pointer + omitempty on the parent).
type toolCallMeta struct {
	ProgressToken string `json:"progressToken"`
}

// toolCallRequest is the typed tools/call params payload. Field order and tags
// reproduce the original map-based marshal byte-for-byte: Go sorts map keys, so
// the wire order is _meta, arguments, name. Meta is a pointer so omitempty drops
// it entirely when progress is off.
//
// Why typed: CallTool is an agent hot path; marshaling a concrete struct avoids
// the per-call map allocations (and the nested _meta map) the map form required.
type toolCallRequest struct {
	Meta      *toolCallMeta   `json:"_meta,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
	Name      string          `json:"name"`
}

// CallTool invokes a named tool on the MCP server with the given arguments.
func (c *StdioClient) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	payload := toolCallRequest{Arguments: args, Name: name}
	if c.progressEnabled {
		// Encode the tool name in the token so the registry recovers it without
		// a correlation map: "<tool>#<seq>".
		seq := c.progressSeq.Add(1)
		payload.Meta = &toolCallMeta{ProgressToken: fmt.Sprintf("%s#%d", name, seq)}
	}
	params, _ := json.Marshal(payload)
	raw, err := c.framer.Call(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	var res CallToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode call tool: %w", err)
	}
	return &res, nil
}

func (c *StdioClient) listResources(ctx context.Context) ([]ResourceInfo, error) {
	raw, err := c.framer.Call(ctx, "resources/list", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	return decodeResourceList(raw)
}

func (c *StdioClient) readResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	params, _ := json.Marshal(map[string]string{"uri": uri})
	raw, err := c.framer.Call(ctx, "resources/read", params)
	if err != nil {
		return nil, err
	}
	return decodeResourceRead(raw)
}

func (c *StdioClient) listPrompts(ctx context.Context) ([]Prompt, error) {
	raw, err := c.framer.Call(ctx, "prompts/list", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	return decodePromptsList(raw)
}

func (c *StdioClient) getPrompt(ctx context.Context, name string, args map[string]string) (*PromptResult, error) {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	raw, err := c.framer.Call(ctx, "prompts/get", params)
	if err != nil {
		return nil, err
	}
	return decodePromptGet(raw)
}

func (c *StdioClient) subscribeResource(ctx context.Context, uri string) error {
	params, _ := json.Marshal(map[string]string{"uri": uri})
	_, err := c.framer.Call(ctx, "resources/subscribe", params)
	return err
}

func (c *StdioClient) unsubscribeResource(ctx context.Context, uri string) error {
	params, _ := json.Marshal(map[string]string{"uri": uri})
	_, err := c.framer.Call(ctx, "resources/unsubscribe", params)
	return err
}

func (c *StdioClient) setLogLevel(ctx context.Context, level LogLevel) error {
	params, _ := json.Marshal(map[string]string{"level": string(level)})
	_, err := c.framer.Call(ctx, "logging/setLevel", params)
	return err
}

// setNotificationHandler registers a callback for server-initiated
// notifications (progress, logging, resource/prompt changes). The registry
// wires this to its event router. Replaces any previous handler.
func (c *StdioClient) setNotificationHandler(fn func(method string, params json.RawMessage)) {
	c.onNotify.Store(notifyFn(fn))
}

func (c *StdioClient) handleNotification(method string, params json.RawMessage) {
	if v := c.onNotify.Load(); v != nil {
		if fn, _ := v.(notifyFn); fn != nil {
			fn(method, params)
		}
	}
}

// setRoots configures the filesystem roots advertised to the server and
// returned from roots/list. Call before Initialize.
func (c *StdioClient) setRoots(roots []Root) { c.roots = roots }

// handleServerRequest answers server→client requests. Only fast, non-blocking
// methods belong here (see framer.onRequest). ping is always answered so servers
// don't treat the client as dead.
func (c *StdioClient) handleServerRequest(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "ping":
		return struct{}{}, nil
	case "roots/list":
		list := make([]map[string]string, len(c.roots))
		for i, r := range c.roots {
			list[i] = map[string]string{"uri": r.URI, "name": r.Name}
		}
		return map[string]any{"roots": list}, nil
	default:
		return nil, &rpcError{Code: errCodeMethodNotFound, Message: "method not found: " + method}
	}
}

// Close shuts down the client and any child process.
func (c *StdioClient) Close(ctx context.Context) error {
	var firstErr error
	c.closeOnce.Do(func() {
		c.framer.Close()
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.stdout != nil {
			_ = c.stdout.Close()
		}
		if c.cmd != nil && c.cmd.Process != nil {
			// Give graceful exit a moment; otherwise kill.
			done := make(chan error, 1)
			go func() { done <- c.cmd.Wait() }()
			select {
			case <-done:
			case <-ctx.Done():
				_ = c.cmd.Process.Kill()
				firstErr = ctx.Err()
			}
		}
	})
	return firstErr
}
