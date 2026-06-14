package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFramer_RequestResponse(t *testing.T) {
	// Pipes simulating server: client writes -> server reads from in,
	// server writes -> client reads from out.
	// For test, we manually inject a response.
	in := &bytes.Buffer{}
	canned := `{"jsonrpc":"2.0","id":1,"result":{"hello":"world"}}` + "\n"
	out := strings.NewReader(canned)

	f := newFramer(out, in)
	ctx := context.Background()

	raw, err := f.Call(ctx, "test/method", json.RawMessage(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if string(raw) != `{"hello":"world"}` {
		t.Errorf("result: %s", raw)
	}
	// Verify outgoing request was written correctly.
	var sent map[string]interface{}
	if err := json.Unmarshal(in.Bytes(), &sent); err != nil {
		t.Fatalf("sent: %v", err)
	}
	if sent["method"] != "test/method" {
		t.Errorf("method: %v", sent)
	}
}

func TestFramer_ErrorResponse(t *testing.T) {
	in := &bytes.Buffer{}
	canned := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}` + "\n"
	out := strings.NewReader(canned)

	f := newFramer(out, in)
	_, err := f.Call(context.Background(), "bad/method", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("err msg: %v", err)
	}
}

func TestFramer_Notification(t *testing.T) {
	in := &bytes.Buffer{}
	out := strings.NewReader("") // no response expected

	f := newFramer(out, in)
	if err := f.Notify(context.Background(), "note", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("notify: %v", err)
	}
	var sent map[string]interface{}
	if err := json.Unmarshal(in.Bytes(), &sent); err != nil {
		t.Fatalf("sent: %v", err)
	}
	if _, hasID := sent["id"]; hasID {
		t.Errorf("notification must not have id field")
	}
}

func TestFramer_ContextCancel(t *testing.T) {
	// Reader that blocks forever simulates hung server.
	pr, _ := io.Pipe()
	in := &bytes.Buffer{}

	f := newFramer(pr, in)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := f.Call(ctx, "x", nil)
	if err == nil {
		t.Fatal("expected context deadline error")
	}
}

func TestFramer_RoutesNotification(t *testing.T) {
	or, ow := io.Pipe() // server -> client (framer reads)
	in := &bytes.Buffer{}
	f := newFramer(or, in)
	defer f.Close()

	got := make(chan string, 1)
	f.onNotify(func(method string, params json.RawMessage) { got <- method })

	// Deterministic: handler is set before the line is delivered.
	_, _ = ow.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":0.5}}` + "\n"))

	select {
	case m := <-got:
		if m != "notifications/progress" {
			t.Errorf("method = %q", m)
		}
	case <-time.After(time.Second):
		t.Fatal("notification handler not called")
	}
}

func TestFramer_AnswersServerRequest(t *testing.T) {
	or, ow := io.Pipe() // server -> client (framer reads)
	ir, iw := io.Pipe() // client -> server (framer writes; test reads ir)
	f := newFramer(or, iw)
	defer f.Close()

	f.onRequest(func(method string, params json.RawMessage) (any, *rpcError) {
		if method != "roots/list" {
			return nil, &rpcError{Code: errCodeMethodNotFound, Message: "nope"}
		}
		return map[string]any{"roots": []any{}}, nil
	})

	_, _ = ow.Write([]byte(`{"jsonrpc":"2.0","id":99,"method":"roots/list","params":{}}` + "\n"))

	dec := json.NewDecoder(ir)
	var resp struct {
		ID     float64        `json:"id"`
		Result map[string]any `json:"result"`
		Error  *rpcError      `json:"error"`
	}
	done := make(chan error, 1)
	go func() { done <- dec.Decode(&resp) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("decode response: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no response written for server request")
	}
	if resp.ID != 99 {
		t.Errorf("id = %v", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
	if _, ok := resp.Result["roots"]; !ok {
		t.Errorf("result missing roots: %+v", resp.Result)
	}
}
