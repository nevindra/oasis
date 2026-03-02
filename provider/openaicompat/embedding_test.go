package openaicompat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nevindra/oasis"
)

func TestEmbedding_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("expected path /embeddings, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth: %s", r.Header.Get("Authorization"))
		}

		var req EmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Model != "text-embedding-3-small" {
			t.Errorf("expected model text-embedding-3-small, got %s", req.Model)
		}

		// Input should be array of strings for text-only.
		inputs, ok := req.Input.([]any)
		if !ok {
			t.Fatalf("expected []any input, got %T", req.Input)
		}
		if len(inputs) != 2 {
			t.Fatalf("expected 2 inputs, got %d", len(inputs))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EmbedResponse{
			Data: []EmbedData{
				{Index: 0, Embedding: []float32{0.1, 0.2, 0.3}},
				{Index: 1, Embedding: []float32{0.4, 0.5, 0.6}},
			},
		})
	}))
	defer srv.Close()

	e := NewEmbedding("test-key", "text-embedding-3-small", srv.URL, 3)
	vecs, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	if vecs[0][0] != 0.1 {
		t.Errorf("expected 0.1, got %f", vecs[0][0])
	}
}

func TestEmbedding_Dimensions(t *testing.T) {
	e := NewEmbedding("key", "model", "http://localhost", 768)
	if e.Dimensions() != 768 {
		t.Errorf("expected 768, got %d", e.Dimensions())
	}
}

func TestEmbedding_Name(t *testing.T) {
	e := NewEmbedding("key", "model", "http://localhost", 768)
	if e.Name() != "openai" {
		t.Errorf("expected 'openai', got %q", e.Name())
	}
	e = NewEmbedding("key", "model", "http://localhost", 768, WithEmbeddingName("vllm"))
	if e.Name() != "vllm" {
		t.Errorf("expected 'vllm', got %q", e.Name())
	}
}

func TestEmbedding_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	e := NewEmbedding("key", "model", srv.URL, 768)
	_, err := e.Embed(context.Background(), []string{"test"})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	httpErr, ok := err.(*oasis.ErrHTTP)
	if !ok {
		t.Fatalf("expected *ErrHTTP, got %T", err)
	}
	if httpErr.Status != 429 {
		t.Errorf("expected 429, got %d", httpErr.Status)
	}
}

func TestEmbedding_EmbedMultimodal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req EmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}

		// Multimodal input should be array of message objects.
		inputs, ok := req.Input.([]any)
		if !ok {
			t.Fatalf("expected []any input, got %T", req.Input)
		}
		if len(inputs) != 2 {
			t.Fatalf("expected 2 inputs, got %d", len(inputs))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EmbedResponse{
			Data: []EmbedData{
				{Index: 0, Embedding: []float32{0.1, 0.2}},
				{Index: 1, Embedding: []float32{0.3, 0.4}},
			},
		})
	}))
	defer srv.Close()

	e := NewEmbedding("test-key", "Qwen3-VL-Embedding-8B", srv.URL, 2)

	vecs, err := e.EmbedMultimodal(context.Background(), []oasis.MultimodalInput{
		// Text-only input.
		{Text: "black shirt"},
		// Image input with text instruction.
		{
			Text:        "Represent the image",
			Attachments: []oasis.Attachment{{MimeType: "image/jpeg", Data: []byte{0xFF, 0xD8}}},
		},
	})
	if err != nil {
		t.Fatalf("EmbedMultimodal: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
}

func TestEmbedding_EmbedMultimodal_RequestFormat(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EmbedResponse{
			Data: []EmbedData{{Index: 0, Embedding: []float32{0.1}}},
		})
	}))
	defer srv.Close()

	e := NewEmbedding("key", "model", srv.URL, 1)
	_, _ = e.EmbedMultimodal(context.Background(), []oasis.MultimodalInput{
		{
			Text:        "describe",
			Attachments: []oasis.Attachment{{MimeType: "image/png", URL: "https://example.com/img.png"}},
		},
	})

	// Verify the request uses chat message format for multimodal.
	var parsed struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(capturedBody, &parsed); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}
	if len(parsed.Input) != 1 {
		t.Fatalf("expected 1 input, got %d", len(parsed.Input))
	}
	// Each input should be a chat message with role and content blocks.
	msg := parsed.Input[0]
	if msg["role"] != "user" {
		t.Errorf("expected role 'user', got %v", msg["role"])
	}
}

// Compile-time interface checks.
var _ oasis.EmbeddingProvider = (*Embedding)(nil)
var _ oasis.MultimodalEmbeddingProvider = (*Embedding)(nil)
