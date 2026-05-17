package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	oasis "github.com/nevindra/oasis"
)

func TestHTTPFetchBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><p>Hello from test server</p></body></html>"))
	}))
	defer srv.Close()

	tool := New()
	out, err := tool.Execute(context.Background(), FetchInput{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Error("expected content")
	}
}

func TestHTTPFetch404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	tool := New()
	_, err := tool.Execute(context.Background(), FetchInput{URL: srv.URL})
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestHTTPFetchTruncation(t *testing.T) {
	bigContent := make([]byte, 10000)
	for i := range bigContent {
		bigContent[i] = 'A'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bigContent)
	}))
	defer srv.Close()

	tool := New()
	out, _ := tool.Execute(context.Background(), FetchInput{URL: srv.URL})
	if len(out) > 8100 {
		t.Errorf("content not truncated: %d", len(out))
	}
}

// TestHTTPFetchErased verifies the tool works after Erase to AnyTool.
func TestHTTPFetchErased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><p>Hello from test server</p></body></html>"))
	}))
	defer srv.Close()

	any := oasis.Erase[FetchInput, string](New())
	if any.Name() != "http_fetch" {
		t.Errorf("Name = %q, want http_fetch", any.Name())
	}
	def := any.Definition()
	if def.Name != "http_fetch" {
		t.Errorf("Definition.Name = %q", def.Name)
	}

	args, _ := json.Marshal(FetchInput{URL: srv.URL})
	res, err := any.ExecuteRaw(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	// Content is a JSON-encoded string after Erase.
	var decoded string
	if err := json.Unmarshal([]byte(res.Content), &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !strings.Contains(decoded, "Hello") {
		t.Errorf("got %q, expected text with 'Hello'", decoded)
	}
}

// TestHTTPFetchErasedBadArgs verifies that bad JSON lands in ToolResult.Error,
// not a Go error.
func TestHTTPFetchErasedBadArgs(t *testing.T) {
	any := oasis.Erase[FetchInput, string](New())
	res, err := any.ExecuteRaw(context.Background(), json.RawMessage(`{not valid json}`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error == "" {
		t.Error("expected ToolResult.Error for bad args")
	}
}
