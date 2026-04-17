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
