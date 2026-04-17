package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// framer manages newline-delimited JSON-RPC framing over a reader/writer pair.
// Handles request/response correlation by ID; serializes writes via mutex.
type framer struct {
	enc      *json.Encoder
	encMu    sync.Mutex   // serialize writes (FIFO)
	nextID   atomic.Int64
	pending  map[int64]chan rpcResponse
	pendMu   sync.Mutex
	readErr  atomic.Value // error
	closed   atomic.Bool
	in       io.Writer // server stdin (we write here)
	out      io.Reader // server stdout (we read here)
	readDone chan struct{}
}

func newFramer(out io.Reader, in io.Writer) *framer {
	f := &framer{
		enc:      json.NewEncoder(in),
		in:       in,
		out:      out,
		pending:  make(map[int64]chan rpcResponse),
		readDone: make(chan struct{}),
	}
	go f.readLoop()
	return f
}

func (f *framer) readLoop() {
	defer close(f.readDone)
	scanner := bufio.NewScanner(f.out)
	// Allow large messages (some MCP responses can be sizeable).
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			// Malformed response: log via readErr and continue (don't block other in-flight).
			f.readErr.Store(fmt.Errorf("malformed response: %w", err))
			continue
		}
		// Match by ID. Notifications from server (no ID) are ignored at this layer
		// (handled separately by client's notification subscriber).
		if resp.ID == nil {
			continue
		}
		var id int64
		switch v := resp.ID.(type) {
		case float64:
			id = int64(v)
		case int64:
			id = v
		default:
			continue
		}
		f.pendMu.Lock()
		ch, ok := f.pending[id]
		if ok {
			delete(f.pending, id)
		}
		f.pendMu.Unlock()
		if ok {
			ch <- resp
		}
	}
	if err := scanner.Err(); err != nil {
		f.readErr.Store(err)
	} else {
		f.readErr.Store(io.EOF)
	}
	// Wake all pending callers with the read error.
	f.pendMu.Lock()
	for id, ch := range f.pending {
		close(ch)
		delete(f.pending, id)
	}
	f.pendMu.Unlock()
}

// Call sends a JSON-RPC request and blocks until response or context canceled.
func (f *framer) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if f.closed.Load() {
		return nil, errors.New("framer: closed")
	}
	id := f.nextID.Add(1)
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}

	ch := make(chan rpcResponse, 1)
	f.pendMu.Lock()
	f.pending[id] = ch
	f.pendMu.Unlock()

	f.encMu.Lock()
	err := f.enc.Encode(&req)
	f.encMu.Unlock()
	if err != nil {
		f.pendMu.Lock()
		delete(f.pending, id)
		f.pendMu.Unlock()
		return nil, fmt.Errorf("encode request: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			// Channel closed via readLoop teardown.
			if e, _ := f.readErr.Load().(error); e != nil {
				return nil, fmt.Errorf("transport closed: %w", e)
			}
			return nil, errors.New("transport closed")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-ctx.Done():
		f.pendMu.Lock()
		delete(f.pending, id)
		f.pendMu.Unlock()
		return nil, ctx.Err()
	}
}

// Notify sends a JSON-RPC notification (no ID, no response expected).
func (f *framer) Notify(ctx context.Context, method string, params json.RawMessage) error {
	if f.closed.Load() {
		return errors.New("framer: closed")
	}
	req := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	f.encMu.Lock()
	defer f.encMu.Unlock()
	return f.enc.Encode(&req)
}

// Close marks the framer closed. Caller is responsible for closing underlying io.
func (f *framer) Close() {
	f.closed.Store(true)
}
