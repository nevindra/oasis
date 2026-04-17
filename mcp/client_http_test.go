package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newFakeHTTPServer(t *testing.T, handler func(method string, params json.RawMessage) (interface{}, error)) (*httptest.Server, *atomic.Int32) {
	callCount := &atomic.Int32{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("bad request: %v", err)
			return
		}
		result, err := handler(req.Method, req.Params)
		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID
		if err != nil {
			resp.Error = &rpcError{Code: -1, Message: err.Error()}
		} else {
			raw, _ := json.Marshal(result)
			resp.Result = raw
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&resp)
	}))
	t.Cleanup(s.Close)
	return s, callCount
}

func TestHTTPClient_Initialize(t *testing.T) {
	srv, _ := newFakeHTTPServer(t, func(method string, _ json.RawMessage) (interface{}, error) {
		if method != "initialize" {
			return nil, fmt.Errorf("unexpected method: %s", method)
		}
		return map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "http-test", "version": "1.0"},
		}, nil
	})

	c := NewHTTPClient(srv.URL, nil, nil, 5*time.Second)
	info, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if info.ServerInfo.Name != "http-test" {
		t.Errorf("got %+v", info)
	}
}

func TestHTTPClient_AuthHeader(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"protocolVersion":"x","capabilities":{},"serverInfo":{"name":"s","version":"1"}}`)})
	}))
	defer srv.Close()

	auth := BearerAuth{Token: "secret"}
	c := NewHTTPClient(srv.URL, nil, auth, 5*time.Second)
	c.Initialize(context.Background())

	if !strings.HasPrefix(sawAuth, "Bearer secret") {
		t.Errorf("auth header: %q", sawAuth)
	}
}

func TestHTTPClient_CallTool_Error(t *testing.T) {
	srv, _ := newFakeHTTPServer(t, func(method string, _ json.RawMessage) (interface{}, error) {
		switch method {
		case "initialize":
			return map[string]interface{}{"protocolVersion": "x", "capabilities": map[string]interface{}{}, "serverInfo": map[string]interface{}{"name": "s", "version": "1"}}, nil
		case "tools/call":
			return nil, fmt.Errorf("tool failed")
		}
		return nil, fmt.Errorf("?")
	})

	c := NewHTTPClient(srv.URL, nil, nil, 5*time.Second)
	c.Initialize(context.Background())
	_, err := c.CallTool(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "tool failed") {
		t.Errorf("err: %v", err)
	}
}

func TestHTTPClient_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil, nil, 50*time.Millisecond)
	_, err := c.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected timeout")
	}
}

func TestHTTPClient_ExtraHeaders(t *testing.T) {
	var apiVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiVer = r.Header.Get("X-Api-Version")
		body, _ := io.ReadAll(r.Body)
		var req rpcRequest
		json.Unmarshal(body, &req)
		json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"protocolVersion":"x","capabilities":{},"serverInfo":{"name":"s","version":"1"}}`)})
	}))
	defer srv.Close()

	headers := map[string]string{"X-Api-Version": "v2"}
	c := NewHTTPClient(srv.URL, headers, nil, 5*time.Second)
	c.Initialize(context.Background())

	if apiVer != "v2" {
		t.Errorf("X-Api-Version: %q", apiVer)
	}
}
