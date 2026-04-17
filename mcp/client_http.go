package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// HTTPClient is an MCP client that communicates over HTTP (stateless JSON-RPC).
// Each method call is an independent POST request. It is safe for concurrent use.
type HTTPClient struct {
	url          string
	headers      map[string]string
	auth         Auth
	httpClient   *http.Client
	nextID       atomic.Int64
	disconnectFn atomic.Value // stores func(error)
}

// NewHTTPClient constructs an HTTP-transport MCP client.
// extraHeaders are added to every request before auth (auth may override them).
// timeout is applied per-request; 0 means no timeout.
func NewHTTPClient(url string, extraHeaders map[string]string, auth Auth, timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		url:        url,
		headers:    extraHeaders,
		auth:       auth,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *HTTPClient) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	body, err := json.Marshal(&req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	if c.auth != nil {
		if err := c.auth.Apply(httpReq); err != nil {
			return nil, fmt.Errorf("auth: %w", err)
		}
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.notifyDisconnect(err)
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode rpc response: %w (body: %s)", err, raw)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func (c *HTTPClient) notifyDisconnect(err error) {
	if fn, ok := c.disconnectFn.Load().(func(error)); ok && fn != nil {
		fn(err)
	}
}

// OnDisconnect registers a callback fired when an HTTP request fails (transport
// error). For HTTP clients this is a best-effort signal — a single callback is
// stored; a second registration replaces the first.
func (c *HTTPClient) OnDisconnect(fn func(error)) {
	c.disconnectFn.Store(fn)
}

// Initialize performs the MCP initialize handshake and returns the server's
// declared info and capabilities.
func (c *HTTPClient) Initialize(ctx context.Context) (*InitializeResult, error) {
	params := json.RawMessage(`{
		"protocolVersion":"2024-11-05",
		"capabilities":{"tools":{},"resources":{}},
		"clientInfo":{"name":"oasis","version":"0.x.0"}
	}`)
	raw, err := c.call(ctx, "initialize", params)
	if err != nil {
		return nil, err
	}
	var res InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ListTools fetches the server's tool catalog.
func (c *HTTPClient) ListTools(ctx context.Context) (*ListToolsResult, error) {
	raw, err := c.call(ctx, "tools/list", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	var res ListToolsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// CallTool invokes a tool by name. args may be nil or empty (treated as `{}`).
func (c *HTTPClient) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	params, err := json.Marshal(map[string]interface{}{"name": name, "arguments": json.RawMessage(args)})
	if err != nil {
		return nil, fmt.Errorf("marshal call params: %w", err)
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	var res CallToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Close releases idle connections. HTTP is stateless so no active connections
// to close; this is a best-effort cleanup.
func (c *HTTPClient) Close(_ context.Context) error {
	c.httpClient.CloseIdleConnections()
	return nil
}
