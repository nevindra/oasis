package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePipes returns a duplex pair: client writes to clientWrite, reads from clientRead.
// Server (test fixture) reads from serverRead, writes to serverWrite.
type fakePipes struct {
	clientReads  *io.PipeReader
	clientWrites *io.PipeWriter
	serverReads  *io.PipeReader
	serverWrites *io.PipeWriter
}

func newFakePipes() *fakePipes {
	cr, sw := io.Pipe() // server writes -> client reads
	sr, cw := io.Pipe() // client writes -> server reads
	return &fakePipes{cr, cw, sr, sw}
}

func TestStdioClient_Initialize(t *testing.T) {
	p := newFakePipes()
	var serverIn bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	// Fake server: reads init request, writes init response, then notification handshake done.
	go func() {
		defer wg.Done()
		scanner := json.NewDecoder(p.serverReads)
		var req map[string]interface{}
		scanner.Decode(&req)
		serverIn.Write([]byte(req["method"].(string)))
		// Send response.
		resp := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1.0"}}}` + "\n"
		p.serverWrites.Write([]byte(resp))
		// Read the "notifications/initialized" notification (no response).
		scanner.Decode(&req)
	}()

	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	defer c.Close(context.Background())

	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if info.ServerInfo.Name != "fake" {
		t.Errorf("got %+v", info)
	}
	p.serverWrites.Close()
	wg.Wait()
}

func TestStdioClient_ListTools(t *testing.T) {
	p := newFakePipes()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		dec := json.NewDecoder(p.serverReads)
		// Expect 1: initialize, 2: list_tools (after notification consumed)
		var req map[string]interface{}
		// initialize
		dec.Decode(&req)
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"f","version":"1"}}}` + "\n"))
		// notifications/initialized
		dec.Decode(&req)
		// list_tools
		dec.Decode(&req)
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo","description":"echo input","inputSchema":{"type":"object"}}]}}` + "\n"))
	}()

	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	defer c.Close(context.Background())

	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "echo" {
		t.Errorf("got %+v", res)
	}
	p.serverWrites.Close()
	wg.Wait()
}

func TestStdioClient_CallTool(t *testing.T) {
	p := newFakePipes()
	go func() {
		dec := json.NewDecoder(p.serverReads)
		var req map[string]interface{}
		dec.Decode(&req) // initialize
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"x","capabilities":{},"serverInfo":{"name":"f","version":"1"}}}` + "\n"))
		dec.Decode(&req) // notifications/initialized
		dec.Decode(&req) // tools/call
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"pong"}]}}` + "\n"))
	}()

	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	defer c.Close(context.Background())

	c.Initialize(context.Background())
	res, err := c.CallTool(context.Background(), "ping", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "pong" {
		t.Errorf("got %+v", res)
	}
}

func TestStdioClient_OnDisconnect_OnEOF(t *testing.T) {
	p := newFakePipes()
	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)

	disconnected := make(chan error, 1)
	c.OnDisconnect(func(err error) { disconnected <- err })

	// Close server side -> client read loop sees EOF.
	p.serverWrites.Close()

	select {
	case err := <-disconnected:
		if err == nil {
			t.Errorf("expected non-nil error on EOF")
		}
	case <-time.After(time.Second):
		t.Fatal("OnDisconnect not fired")
	}
}

func TestStdioClient_ForwardsNotification(t *testing.T) {
	p := newFakePipes()
	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	defer c.Close(context.Background())

	got := make(chan string, 1)
	c.setNotificationHandler(func(method string, params json.RawMessage) { got <- method })

	// Server pushes a notification (no id).
	_, _ = p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}` + "\n"))

	select {
	case m := <-got:
		if m != "notifications/message" {
			t.Errorf("method = %q", m)
		}
	case <-time.After(time.Second):
		t.Fatal("notification not forwarded")
	}
}

func TestStdioClient_AnswersPing(t *testing.T) {
	p := newFakePipes()
	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	defer c.Close(context.Background())

	// Server sends a ping request (with id) and reads the client's response.
	_, _ = p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":7,"method":"ping","params":{}}` + "\n"))

	dec := json.NewDecoder(p.serverReads)
	var resp struct {
		ID    float64   `json:"id"`
		Error *struct{} `json:"error"`
	}
	done := make(chan error, 1)
	go func() { done <- dec.Decode(&resp) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no ping response")
	}
	if resp.ID != 7 {
		t.Errorf("id = %v", resp.ID)
	}
}

func TestStdioClient_Roots_AdvertiseAndAnswer(t *testing.T) {
	p := newFakePipes()
	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	c.setRoots([]Root{{URI: "file:///work", Name: "work"}})
	defer c.Close(context.Background())

	var initCaps map[string]json.RawMessage
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		dec := json.NewDecoder(p.serverReads)
		var req struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
			Params struct {
				Capabilities map[string]json.RawMessage `json:"capabilities"`
			} `json:"params"`
		}
		dec.Decode(&req) // initialize
		initCaps = req.Params.Capabilities
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"x","capabilities":{},"serverInfo":{"name":"f","version":"1"}}}` + "\n"))
		dec.Decode(&req) // notifications/initialized
	}()

	if _, err := c.Initialize(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	wg.Wait()
	if _, ok := initCaps["roots"]; !ok {
		t.Fatalf("roots capability not advertised: %v", initCaps)
	}

	// Server now asks for roots/list; client must answer from config.
	p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":50,"method":"roots/list","params":{}}` + "\n"))
	dec := json.NewDecoder(p.serverReads)
	var resp struct {
		ID     float64 `json:"id"`
		Result struct {
			Roots []struct {
				URI  string `json:"uri"`
				Name string `json:"name"`
			} `json:"roots"`
		} `json:"result"`
	}
	done := make(chan error, 1)
	go func() { done <- dec.Decode(&resp) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("decode roots resp: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no roots/list response")
	}
	if len(resp.Result.Roots) != 1 || resp.Result.Roots[0].URI != "file:///work" {
		t.Fatalf("roots = %+v", resp.Result.Roots)
	}
}

func TestStdioClient_Progress_OffByDefault(t *testing.T) {
	p := newFakePipes()
	got := make(chan json.RawMessage, 1)
	go func() {
		dec := json.NewDecoder(p.serverReads)
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		dec.Decode(&req) // initialize
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"x","capabilities":{},"serverInfo":{"name":"f","version":"1"}}}` + "\n"))
		dec.Decode(&req) // notifications/initialized
		dec.Decode(&req) // tools/call
		got <- req.Params
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[]}}` + "\n"))
	}()
	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	defer c.Close(context.Background())
	c.Initialize(context.Background())
	c.CallTool(context.Background(), "fetch", json.RawMessage(`{}`))

	params := <-got
	if strings.Contains(string(params), "progressToken") {
		t.Errorf("default CallTool must not send progressToken, got %s", params)
	}
}

func TestStdioClient_Progress_OnInjectsToken(t *testing.T) {
	p := newFakePipes()
	got := make(chan json.RawMessage, 1)
	go func() {
		dec := json.NewDecoder(p.serverReads)
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		dec.Decode(&req)
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"x","capabilities":{},"serverInfo":{"name":"f","version":"1"}}}` + "\n"))
		dec.Decode(&req)
		dec.Decode(&req) // tools/call
		got <- req.Params
		p.serverWrites.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[]}}` + "\n"))
	}()
	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	c.setProgressEnabled(true)
	defer c.Close(context.Background())
	c.Initialize(context.Background())
	c.CallTool(context.Background(), "fetch", json.RawMessage(`{}`))

	var parsed struct {
		Meta struct {
			ProgressToken string `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(<-got, &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(parsed.Meta.ProgressToken, "fetch#") {
		t.Errorf("progressToken = %q, want fetch#<n>", parsed.Meta.ProgressToken)
	}
}

func TestStdioClient_RejectAfterInit_BadMethod(t *testing.T) {
	p := newFakePipes()
	go func() {
		dec := json.NewDecoder(p.serverReads)
		var req map[string]interface{}
		dec.Decode(&req)
		// Bad init: malformed JSON.
		p.serverWrites.Write([]byte(`not-json` + "\n"))
	}()

	c := NewStdioClientFromPipes(p.clientReads, p.clientWrites)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Initialize(ctx)
	if err == nil {
		t.Fatal("expected init error")
	}
	if !strings.Contains(err.Error(), "context deadline") && !strings.Contains(err.Error(), "transport closed") {
		t.Errorf("expected timeout or transport error, got: %v", err)
	}
}
