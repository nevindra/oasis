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
	cmd          *exec.Cmd      // nil when constructed via NewStdioClientFromPipes (testing)
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	framer       *framer
	disconnectFn atomic.Value // func(error)
	closeOnce    sync.Once
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
	go c.watchProcess()
	return c, nil
}

// NewStdioClientFromPipes constructs a StdioClient from existing pipes,
// bypassing exec.Cmd. Useful for testing and in-process MCP servers.
func NewStdioClientFromPipes(r io.ReadCloser, w io.WriteCloser) *StdioClient {
	c := &StdioClient{stdin: w, stdout: r}
	c.framer = newFramer(r, w)
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
	params := json.RawMessage(`{
        "protocolVersion":"2024-11-05",
        "capabilities":{"tools":{},"resources":{}},
        "clientInfo":{"name":"oasis","version":"0.x.0"}
    }`)
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

// CallTool invokes a named tool on the MCP server with the given arguments.
func (c *StdioClient) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	params, _ := json.Marshal(map[string]interface{}{
		"name":      name,
		"arguments": json.RawMessage(args),
	})
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
