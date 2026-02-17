package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPFetchBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><p>Hello from test server</p></body></html>"))
	}))
	defer srv.Close()

	tool := New()
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, err := tool.Execute(context.Background(), "http_fetch", args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Content == "" {
		t.Error("expected content")
	}
}

func TestHTTPFetch404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	tool := New()
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, _ := tool.Execute(context.Background(), "http_fetch", args)
	if result.Error == "" {
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
	args, _ := json.Marshal(map[string]string{"url": srv.URL})
	result, _ := tool.Execute(context.Background(), "http_fetch", args)
	if len(result.Content) > 8100 {
		t.Errorf("content not truncated: %d", len(result.Content))
	}
}
