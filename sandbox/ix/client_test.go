package ix

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientPost(t *testing.T) {
	type reqBody struct {
		Name string `json:"name"`
	}
	type respBody struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		var req reqBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(respBody{ID: 42, Name: req.Name})
	}))
	defer srv.Close()

	c := newClient(srv.URL, srv.Client())

	var resp respBody
	err := c.post(context.Background(), "/test", reqBody{Name: "hello"}, &resp)
	if err != nil {
		t.Fatalf("post() returned error: %v", err)
	}
	if resp.ID != 42 {
		t.Errorf("expected ID 42, got %d", resp.ID)
	}
	if resp.Name != "hello" {
		t.Errorf("expected Name 'hello', got %q", resp.Name)
	}
}

func TestClientPostError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	c := newClient(srv.URL, srv.Client())

	var dst map[string]any
	err := c.post(context.Background(), "/fail", map[string]string{"k": "v"}, &dst)
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error should mention HTTP 500, got: %v", err)
	}
	if !strings.Contains(err.Error(), "internal server error") {
		t.Errorf("error should include response body, got: %v", err)
	}
}

func TestClientGetRaw(t *testing.T) {
	want := "raw response body content"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Write([]byte(want))
	}))
	defer srv.Close()

	c := newClient(srv.URL, srv.Client())

	rc, err := c.getRaw(context.Background(), "/raw")
	if err != nil {
		t.Fatalf("getRaw() returned error: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != want {
		t.Errorf("expected %q, got %q", want, string(got))
	}
}

func TestClientUpload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("expected multipart/form-data Content-Type, got %s", ct)
		}

		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}

		pathField := r.FormValue("path")
		if pathField != "/workspace/test.txt" {
			t.Errorf("expected path field '/workspace/test.txt', got %q", pathField)
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("get form file: %v", err)
		}
		defer file.Close()

		if header.Filename != "test.txt" {
			t.Errorf("expected filename 'test.txt', got %q", header.Filename)
		}

		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("read file: %v", err)
		}
		if string(data) != "file contents here" {
			t.Errorf("expected 'file contents here', got %q", string(data))
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newClient(srv.URL, srv.Client())

	err := c.upload(
		context.Background(),
		"/upload",
		"/workspace/test.txt",
		strings.NewReader("file contents here"),
	)
	if err != nil {
		t.Fatalf("upload() returned error: %v", err)
	}
}

func TestPostSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if accept := r.Header.Get("Accept"); accept != "text/event-stream" {
			t.Errorf("expected Accept: text/event-stream, got %q", accept)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, _ := w.(http.Flusher)
		w.Write([]byte("event: stdout\ndata: {\"text\": \"hello\\n\"}\n\n"))
		flusher.Flush()
		w.Write([]byte("event: complete\ndata: {\"exit_code\": 0, \"elapsed_ms\": 100}\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	c := newClient(srv.URL, srv.Client())

	reader, err := c.postSSE(context.Background(), "/v1/shell/exec", map[string]string{"command": "echo hello"})
	if err != nil {
		t.Fatalf("postSSE() returned error: %v", err)
	}
	defer reader.Close()

	// First event: stdout
	if !reader.Next() {
		t.Fatal("expected first event, got EOF")
	}
	if reader.Event() != "stdout" {
		t.Errorf("expected event 'stdout', got %q", reader.Event())
	}

	// Second event: complete
	if !reader.Next() {
		t.Fatal("expected second event, got EOF")
	}
	if reader.Event() != "complete" {
		t.Errorf("expected event 'complete', got %q", reader.Event())
	}

	// No more events
	if reader.Next() {
		t.Error("expected EOF after complete event")
	}
	if reader.Err() != nil {
		t.Errorf("unexpected error: %v", reader.Err())
	}
}

func TestPostSSEError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer srv.Close()

	c := newClient(srv.URL, srv.Client())

	_, err := c.postSSE(context.Background(), "/v1/shell/exec", nil)
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("error should mention HTTP 500, got: %v", err)
	}
}

func TestSSEReaderPingSkip(t *testing.T) {
	body := io.NopCloser(strings.NewReader(
		": ping\n\nevent: stdout\ndata: {\"text\": \"hi\"}\n\n: ping\n\nevent: complete\ndata: {\"exit_code\": 0}\n\n",
	))

	reader := newSSEReader(body)

	// First event should be stdout (pings skipped)
	if !reader.Next() {
		t.Fatal("expected first event")
	}
	if reader.Event() != "stdout" {
		t.Errorf("expected 'stdout', got %q", reader.Event())
	}

	// Second event should be complete (ping skipped)
	if !reader.Next() {
		t.Fatal("expected second event")
	}
	if reader.Event() != "complete" {
		t.Errorf("expected 'complete', got %q", reader.Event())
	}

	if reader.Next() {
		t.Error("expected EOF")
	}
}

func TestSSEReaderEmptyStream(t *testing.T) {
	body := io.NopCloser(strings.NewReader(""))
	reader := newSSEReader(body)

	if reader.Next() {
		t.Error("expected false for empty stream")
	}
	if reader.Err() != nil {
		t.Errorf("unexpected error: %v", reader.Err())
	}
}

func TestSSEReaderPingOnly(t *testing.T) {
	body := io.NopCloser(strings.NewReader(": ping\n\n: ping\n\n"))
	reader := newSSEReader(body)

	if reader.Next() {
		t.Error("expected false for ping-only stream")
	}
}
