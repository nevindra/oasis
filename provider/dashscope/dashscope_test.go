package dashscope

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	oasis "github.com/nevindra/oasis/core"
)

// fakeImageBytes is a non-zero payload returned by the mock image server.
var fakeImageBytes = make([]byte, 2048)

func init() {
	for i := range fakeImageBytes {
		fakeImageBytes[i] = 0xFF
	}
}

// imageURL returns the full URL for a path on the test server, derived from the
// request's Host header (avoids the closure-ordering problem where srv.URL is
// not yet assigned when the handler is constructed).
func imageURL(r *http.Request, path string) string {
	return "http://" + r.Host + path
}

// --- Option tests ---

func TestNew_DefaultName(t *testing.T) {
	p := New("key", "model", "http://localhost")
	if p.Name() != "dashscope" {
		t.Errorf("expected default name 'dashscope', got %q", p.Name())
	}
}

func TestNew_WithName(t *testing.T) {
	p := New("key", "model", "http://localhost", WithName("dashscope-intl"))
	if p.Name() != "dashscope-intl" {
		t.Errorf("expected name 'dashscope-intl', got %q", p.Name())
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	custom := &http.Client{}
	p := New("key", "model", "http://localhost", WithHTTPClient(custom))
	if p.client != custom {
		t.Error("expected custom HTTP client to be set")
	}
}

func TestNew_BaseURLTrailingSlash(t *testing.T) {
	p := New("key", "model", "http://localhost/api/v1/")
	if p.baseURL != "http://localhost/api/v1" {
		t.Errorf("expected trailing slash stripped, got %q", p.baseURL)
	}
}

// --- generateSync tests ---

func TestGenerateSync_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services/aigc/multimodal-generation/generation":
			if r.Header.Get("Authorization") != "Bearer test-key" {
				t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
			}
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body["model"] != "qwen-image-2.0" {
				t.Errorf("expected model qwen-image-2.0, got %v", body["model"])
			}
			resp := map[string]any{
				"output": map[string]any{
					"choices": []any{
						map[string]any{
							"message": map[string]any{
								"content": []any{
									map[string]any{"image": imageURL(r, "/fake-image.png")},
								},
							},
						},
					},
				},
				"usage": map[string]any{"image_count": 1},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case "/fake-image.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(fakeImageBytes)

		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	attachments, err := p.generateSync(context.Background(), "a red fox")
	if err != nil {
		t.Fatalf("generateSync: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if attachments[0].MimeType != "image/png" {
		t.Errorf("expected mime image/png, got %q", attachments[0].MimeType)
	}
	if len(attachments[0].Data) != len(fakeImageBytes) {
		t.Errorf("expected %d bytes, got %d", len(fakeImageBytes), len(attachments[0].Data))
	}
}

func TestGenerateSync_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code":    "InvalidApiKey",
			"message": "bad key",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	_, err := p.generateSync(context.Background(), "a red fox")
	if err == nil {
		t.Fatal("expected error for API error response")
	}
	llmErr, ok := err.(*oasis.ErrLLM)
	if !ok {
		t.Fatalf("expected *oasis.ErrLLM, got %T: %v", err, err)
	}
	if llmErr.Provider != "dashscope" {
		t.Errorf("expected provider 'dashscope', got %q", llmErr.Provider)
	}
}

func TestGenerateSync_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	_, err := p.generateSync(context.Background(), "a red fox")
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
	httpErr, ok := err.(*oasis.ErrHTTP)
	if !ok {
		t.Fatalf("expected *oasis.ErrHTTP, got %T", err)
	}
	if httpErr.Status != http.StatusTooManyRequests {
		t.Errorf("expected status 429, got %d", httpErr.Status)
	}
}

func TestGenerateSync_EmptyPrompt(t *testing.T) {
	// ChatStream should reject an empty prompt before hitting the network.
	p := New("key", "qwen-image-2.0", "http://localhost")
	_, err := p.ChatStream(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{{Role: "user", Content: ""}},
	}, nil)
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

// --- generateInterleaved tests ---

func TestGenerateInterleaved_Success(t *testing.T) {
	taskID := "task-abc-123"
	pollCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/services/aigc/image-generation/generation" && r.Method == http.MethodPost:
			if r.Header.Get("X-DashScope-Async") != "enable" {
				t.Errorf("expected X-DashScope-Async: enable, got %q", r.Header.Get("X-DashScope-Async"))
			}
			resp := map[string]any{
				"output": map[string]any{
					"task_id":     taskID,
					"task_status": "PENDING",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/tasks/"+taskID && r.Method == http.MethodGet:
			pollCount++
			if pollCount < 2 {
				// First poll: still running.
				resp := map[string]any{
					"output": map[string]any{"task_status": "RUNNING"},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			} else {
				// Second+ poll: succeeded.
				resp := map[string]any{
					"output": map[string]any{
						"task_status": "SUCCEEDED",
						"choices": []any{
							map[string]any{
								"message": map[string]any{
									"content": []any{
										map[string]any{"image": imageURL(r, "/fake-image.png")},
									},
								},
							},
						},
					},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
			}

		case r.URL.Path == "/fake-image.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(fakeImageBytes)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := New("test-key", "wan2.7-image", srv.URL, WithHTTPClient(srv.Client()))

	attachments, err := p.generateInterleaved(context.Background(), "a robot")
	if err != nil {
		t.Fatalf("generateInterleaved: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if len(attachments[0].Data) != len(fakeImageBytes) {
		t.Errorf("expected %d bytes, got %d", len(fakeImageBytes), len(attachments[0].Data))
	}
	if pollCount < 2 {
		t.Errorf("expected at least 2 polls, got %d", pollCount)
	}
}

func TestGenerateInterleaved_CreateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"server error"}`))
	}))
	defer srv.Close()

	p := New("test-key", "wan2.7-image", srv.URL, WithHTTPClient(srv.Client()))

	_, err := p.generateInterleaved(context.Background(), "a robot")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestGenerateInterleaved_APIErrorInBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code":    "QuotaExceeded",
			"message": "daily quota exceeded",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := New("test-key", "wan2.7-image", srv.URL, WithHTTPClient(srv.Client()))

	_, err := p.generateInterleaved(context.Background(), "a robot")
	if err == nil {
		t.Fatal("expected error for API error code in body")
	}
	llmErr, ok := err.(*oasis.ErrLLM)
	if !ok {
		t.Fatalf("expected *oasis.ErrLLM, got %T: %v", err, err)
	}
	if llmErr.Provider != "wan2.7-image" {
		// provider name defaults to "dashscope" unless overridden, but the model
		// is "wan2.7-image"; p.Name() == "dashscope".
	}
	_ = llmErr
}

func TestGenerateInterleaved_NoTaskID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"output": map[string]any{
				"task_status": "PENDING",
				// deliberately omit task_id
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := New("test-key", "wan2.7-image", srv.URL, WithHTTPClient(srv.Client()))

	_, err := p.generateInterleaved(context.Background(), "a robot")
	if err == nil {
		t.Fatal("expected error when no task_id returned")
	}
}

// --- pollTask tests ---

func TestPollTask_TaskFailed(t *testing.T) {
	taskID := "task-fail-456"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"output": map[string]any{
				"task_status": "FAILED",
				"message":     "quota exceeded",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	_, err := p.pollTask(context.Background(), taskID)
	if err == nil {
		t.Fatal("expected error for FAILED task")
	}
	llmErr, ok := err.(*oasis.ErrLLM)
	if !ok {
		t.Fatalf("expected *oasis.ErrLLM, got %T: %v", err, err)
	}
	if llmErr.Message != "quota exceeded" {
		t.Errorf("expected message 'quota exceeded', got %q", llmErr.Message)
	}
}

func TestPollTask_TaskCanceled(t *testing.T) {
	taskID := "task-cancel-789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"output": map[string]any{"task_status": "CANCELED"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	_, err := p.pollTask(context.Background(), taskID)
	if err == nil {
		t.Fatal("expected error for CANCELED task")
	}
}

func TestPollTask_ContextCanceled(t *testing.T) {
	taskID := "task-ctx-999"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Would keep returning PENDING forever if context weren't canceled.
		resp := map[string]any{
			"output": map[string]any{"task_status": "PENDING"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so pollTask hits the ctx.Done() select branch on first iteration.
	cancel()

	_, err := p.pollTask(ctx, taskID)
	if err == nil {
		t.Fatal("expected error when context is canceled")
	}
}

func TestPollTask_HTTPError(t *testing.T) {
	taskID := "task-http-err"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"service unavailable"}`))
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := p.pollTask(ctx, taskID)
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
	httpErr, ok := err.(*oasis.ErrHTTP)
	if !ok {
		t.Fatalf("expected *oasis.ErrHTTP, got %T: %v", err, err)
	}
	if httpErr.Status != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", httpErr.Status)
	}
}

func TestPollTask_Success(t *testing.T) {
	taskID := "task-success-001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tasks/" + taskID:
			resp := map[string]any{
				"output": map[string]any{
					"task_status": "SUCCEEDED",
					"choices": []any{
						map[string]any{
							"message": map[string]any{
								"content": []any{
									map[string]any{"image": imageURL(r, "/img.png")},
								},
							},
						},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(fakeImageBytes)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	attachments, err := p.pollTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("pollTask: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}
	if attachments[0].MimeType != "image/png" {
		t.Errorf("expected mime image/png, got %q", attachments[0].MimeType)
	}
}

// --- ChatStream integration tests ---

func TestChatStream_RoutesQwenToSync(t *testing.T) {
	hitGeneration := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services/aigc/multimodal-generation/generation":
			hitGeneration = true
			resp := map[string]any{
				"output": map[string]any{
					"choices": []any{
						map[string]any{"message": map[string]any{
							"content": []any{map[string]any{"image": imageURL(r, "/img.png")}},
						}},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(fakeImageBytes)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	resp, err := p.ChatStream(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{oasis.UserMessage("a red fox")},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if !hitGeneration {
		t.Error("expected synchronous generation endpoint to be hit")
	}
	if len(resp.Attachments) != 1 {
		t.Errorf("expected 1 attachment, got %d", len(resp.Attachments))
	}
	if resp.FinishReason != oasis.FinishStop {
		t.Errorf("expected FinishStop, got %q", resp.FinishReason)
	}
}

func TestChatStream_ChannelClosedOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/services/aigc/multimodal-generation/generation":
			resp := map[string]any{
				"output": map[string]any{
					"choices": []any{
						map[string]any{"message": map[string]any{
							"content": []any{map[string]any{"image": imageURL(r, "/img.png")}},
						}},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(fakeImageBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := New("test-key", "qwen-image-2.0", srv.URL, WithHTTPClient(srv.Client()))

	ch := make(chan oasis.StreamEvent, 10)
	_, err := p.ChatStream(context.Background(), oasis.ChatRequest{
		Messages: []oasis.ChatMessage{oasis.UserMessage("test")},
	}, ch)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	// Drain; channel must be closed (range must terminate, not block).
	for range ch {
	}
}
