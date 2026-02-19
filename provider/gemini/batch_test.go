package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nevindra/oasis"
)

func TestMapBatchState(t *testing.T) {
	tests := []struct {
		input    string
		expected oasis.BatchState
	}{
		{"BATCH_STATE_PENDING", oasis.BatchPending},
		{"BATCH_STATE_RUNNING", oasis.BatchRunning},
		{"BATCH_STATE_SUCCEEDED", oasis.BatchSucceeded},
		{"BATCH_STATE_FAILED", oasis.BatchFailed},
		{"BATCH_STATE_CANCELLED", oasis.BatchCancelled},
		{"BATCH_STATE_EXPIRED", oasis.BatchExpired},
		{"JOB_STATE_PENDING", oasis.BatchPending},
		{"JOB_STATE_SUCCEEDED", oasis.BatchSucceeded},
		{"UNKNOWN_STATE", oasis.BatchState("UNKNOWN_STATE")},
	}

	for _, tt := range tests {
		got := mapBatchState(tt.input)
		if got != tt.expected {
			t.Errorf("mapBatchState(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestToBatchJob(t *testing.T) {
	br := batchResponse{
		Name: "batches/123",
		Metadata: batchMetadata{
			DisplayName: "test-batch",
			State:       "BATCH_STATE_SUCCEEDED",
			CreateTime:  "2026-01-15T10:00:00Z",
			UpdateTime:  "2026-01-15T11:00:00Z",
			BatchStats: &batchStatsJSON{
				RequestCount:          10,
				SucceededRequestCount: 9,
				FailedRequestCount:    1,
			},
		},
	}

	job := toBatchJob(br)

	if job.ID != "batches/123" {
		t.Errorf("expected ID 'batches/123', got %q", job.ID)
	}
	if job.State != oasis.BatchSucceeded {
		t.Errorf("expected state succeeded, got %q", job.State)
	}
	if job.DisplayName != "test-batch" {
		t.Errorf("expected display name 'test-batch', got %q", job.DisplayName)
	}
	if job.Stats.TotalCount != 10 {
		t.Errorf("expected total count 10, got %d", job.Stats.TotalCount)
	}
	if job.Stats.SucceededCount != 9 {
		t.Errorf("expected succeeded count 9, got %d", job.Stats.SucceededCount)
	}
	if job.Stats.FailedCount != 1 {
		t.Errorf("expected failed count 1, got %d", job.Stats.FailedCount)
	}
	if job.CreateTime.IsZero() {
		t.Error("expected non-zero create time")
	}
	if job.UpdateTime.IsZero() {
		t.Error("expected non-zero update time")
	}
}

func TestToBatchJob_NilStats(t *testing.T) {
	br := batchResponse{
		Name: "batches/456",
		Metadata: batchMetadata{
			State: "BATCH_STATE_PENDING",
		},
	}

	job := toBatchJob(br)

	if job.Stats.TotalCount != 0 {
		t.Errorf("expected zero total count, got %d", job.Stats.TotalCount)
	}
}

func TestToBatchJob_InvalidTime(t *testing.T) {
	br := batchResponse{
		Name: "batches/789",
		Metadata: batchMetadata{
			State:      "BATCH_STATE_RUNNING",
			CreateTime: "not-a-time",
			UpdateTime: "also-not-a-time",
		},
	}

	job := toBatchJob(br)

	if !job.CreateTime.IsZero() {
		t.Error("expected zero create time for invalid input")
	}
	if !job.UpdateTime.IsZero() {
		t.Error("expected zero update time for invalid input")
	}
}

func TestStringInt_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{`"10"`, 10},
		{`5`, 5},
		{`"0"`, 0},
		{`""`, 0},
	}

	for _, tt := range tests {
		var s stringInt
		if err := json.Unmarshal([]byte(tt.input), &s); err != nil {
			t.Errorf("UnmarshalJSON(%s) error: %v", tt.input, err)
			continue
		}
		if int(s) != tt.expected {
			t.Errorf("UnmarshalJSON(%s) = %d, want %d", tt.input, int(s), tt.expected)
		}
	}
}

func TestParseGeminiResponse(t *testing.T) {
	text := "Hello, world!"
	resp := geminiResponse{
		Candidates: []geminiCandidate{
			{
				Content: geminiContent{
					Parts: []geminiPart{
						{Text: &text},
					},
				},
			},
		},
		UsageMetadata: &geminiUsage{
			PromptTokenCount:     10,
			CandidatesTokenCount: 5,
		},
	}

	result := parseGeminiResponse(resp)

	if result.Content != "Hello, world!" {
		t.Errorf("expected content 'Hello, world!', got %q", result.Content)
	}
	if result.Usage.InputTokens != 10 {
		t.Errorf("expected input tokens 10, got %d", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 5 {
		t.Errorf("expected output tokens 5, got %d", result.Usage.OutputTokens)
	}
}

func TestParseGeminiResponse_WithToolCalls(t *testing.T) {
	resp := geminiResponse{
		Candidates: []geminiCandidate{
			{
				Content: geminiContent{
					Parts: []geminiPart{
						{
							FunctionCall: &geminiFuncCall{
								Name: "search",
								Args: json.RawMessage(`{"query":"test"}`),
							},
						},
					},
				},
			},
		},
	}

	result := parseGeminiResponse(resp)

	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "search" {
		t.Errorf("expected tool call name 'search', got %q", result.ToolCalls[0].Name)
	}
}

func TestParseGeminiResponse_SkipsThought(t *testing.T) {
	thought := "thinking..."
	text := "actual response"
	resp := geminiResponse{
		Candidates: []geminiCandidate{
			{
				Content: geminiContent{
					Parts: []geminiPart{
						{Text: &thought, Thought: true},
						{Text: &text},
					},
				},
			},
		},
	}

	result := parseGeminiResponse(resp)

	if result.Content != "actual response" {
		t.Errorf("expected content 'actual response', got %q", result.Content)
	}
}

func TestParseGeminiResponse_Empty(t *testing.T) {
	resp := geminiResponse{}
	result := parseGeminiResponse(resp)

	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
	if len(result.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(result.ToolCalls))
	}
}

// batchMetadataResponse builds a mock Gemini batch API response with data nested in metadata.
func batchMetadataResponse(name, state string, extra map[string]any) map[string]any {
	metadata := map[string]any{
		"@type": "type.googleapis.com/google.ai.generativelanguage.v1main.GenerateContentBatch",
		"state": state,
		"name":  name,
	}
	for k, v := range extra {
		metadata[k] = v
	}
	return map[string]any{
		"name":     name,
		"metadata": metadata,
	}
}

func TestBatchChat_BuildsCorrectPayload(t *testing.T) {
	var receivedPayload map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		receivedPayload = payload

		json.NewEncoder(w).Encode(batchMetadataResponse("batches/test-123", "BATCH_STATE_PENDING", nil))
	}))
	defer server.Close()

	g := &Gemini{
		apiKey:          "test-key",
		model:           "gemini-2.0-flash",
		httpClient:      server.Client(),
		temperature:     0.1,
		topP:            0.9,
		mediaResolution: "MEDIA_RESOLUTION_MEDIUM",
	}

	origBaseURL := baseURL
	defer func() { baseURL = origBaseURL }()
	baseURL = server.URL

	requests := []oasis.ChatRequest{
		{Messages: []oasis.ChatMessage{{Role: "user", Content: "Hello"}}},
		{Messages: []oasis.ChatMessage{{Role: "user", Content: "World"}}},
	}

	job, err := g.BatchChat(context.Background(), requests)
	if err != nil {
		t.Fatalf("BatchChat returned error: %v", err)
	}

	if job.ID != "batches/test-123" {
		t.Errorf("expected job ID 'batches/test-123', got %q", job.ID)
	}
	if job.State != oasis.BatchPending {
		t.Errorf("expected state pending, got %q", job.State)
	}

	// Verify payload structure.
	batch, ok := receivedPayload["batch"].(map[string]any)
	if !ok {
		t.Fatal("expected 'batch' key in payload")
	}
	inputConfig, ok := batch["input_config"].(map[string]any)
	if !ok {
		t.Fatal("expected 'input_config' in batch")
	}
	reqs, ok := inputConfig["requests"].(map[string]any)
	if !ok {
		t.Fatal("expected 'requests' in input_config")
	}
	reqList, ok := reqs["requests"].([]any)
	if !ok {
		t.Fatal("expected 'requests' array")
	}
	if len(reqList) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqList))
	}
}

func TestBatchStatus_ParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(batchMetadataResponse("batches/test-456", "BATCH_STATE_RUNNING", map[string]any{
			"createTime": "2026-01-15T10:00:00Z",
			"updateTime": "2026-01-15T10:05:00Z",
			"batchStats": map[string]any{
				"requestCount":          "5",
				"succeededRequestCount": "2",
				"failedRequestCount":    "0",
			},
		}))
	}))
	defer server.Close()

	g := &Gemini{
		apiKey:     "test-key",
		model:      "test-model",
		httpClient: server.Client(),
	}

	origBaseURL := baseURL
	defer func() { baseURL = origBaseURL }()
	baseURL = server.URL

	job, err := g.BatchStatus(context.Background(), "batches/test-456")
	if err != nil {
		t.Fatalf("BatchStatus returned error: %v", err)
	}

	if job.State != oasis.BatchRunning {
		t.Errorf("expected state running, got %q", job.State)
	}
	if job.Stats.TotalCount != 5 {
		t.Errorf("expected total count 5, got %d", job.Stats.TotalCount)
	}
	if job.Stats.SucceededCount != 2 {
		t.Errorf("expected succeeded count 2, got %d", job.Stats.SucceededCount)
	}
}

func TestBatchChatResults_ReturnsResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(batchMetadataResponse("batches/test-789", "BATCH_STATE_SUCCEEDED", map[string]any{
			"dest": map[string]any{
				"inlinedResponses": []map[string]any{
					{
						"response": map[string]any{
							"candidates": []map[string]any{
								{
									"content": map[string]any{
										"parts": []map[string]any{
											{"text": "Response 1"},
										},
									},
								},
							},
							"usageMetadata": map[string]any{
								"promptTokenCount":     10,
								"candidatesTokenCount": 5,
							},
						},
					},
					{
						"response": map[string]any{
							"candidates": []map[string]any{
								{
									"content": map[string]any{
										"parts": []map[string]any{
											{"text": "Response 2"},
										},
									},
								},
							},
						},
					},
				},
			},
		}))
	}))
	defer server.Close()

	g := &Gemini{
		apiKey:     "test-key",
		model:      "test-model",
		httpClient: server.Client(),
	}

	origBaseURL := baseURL
	defer func() { baseURL = origBaseURL }()
	baseURL = server.URL

	results, err := g.BatchChatResults(context.Background(), "batches/test-789")
	if err != nil {
		t.Fatalf("BatchChatResults returned error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Content != "Response 1" {
		t.Errorf("expected 'Response 1', got %q", results[0].Content)
	}
	if results[1].Content != "Response 2" {
		t.Errorf("expected 'Response 2', got %q", results[1].Content)
	}
	if results[0].Usage.InputTokens != 10 {
		t.Errorf("expected input tokens 10, got %d", results[0].Usage.InputTokens)
	}
}

func TestBatchChatResults_NotCompleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(batchMetadataResponse("batches/running", "BATCH_STATE_RUNNING", nil))
	}))
	defer server.Close()

	g := &Gemini{
		apiKey:     "test-key",
		model:      "test-model",
		httpClient: server.Client(),
	}

	origBaseURL := baseURL
	defer func() { baseURL = origBaseURL }()
	baseURL = server.URL

	_, err := g.BatchChatResults(context.Background(), "batches/running")
	if err == nil {
		t.Fatal("expected error for non-completed batch job")
	}

	llmErr, ok := err.(*oasis.ErrLLM)
	if !ok {
		t.Fatalf("expected ErrLLM, got %T", err)
	}
	if llmErr.Provider != "gemini" {
		t.Errorf("expected provider 'gemini', got %q", llmErr.Provider)
	}
}

func TestBatchCancel(t *testing.T) {
	var receivedMethod string
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	g := &Gemini{
		apiKey:     "test-key",
		model:      "test-model",
		httpClient: server.Client(),
	}

	origBaseURL := baseURL
	defer func() { baseURL = origBaseURL }()
	baseURL = server.URL

	err := g.BatchCancel(context.Background(), "batches/cancel-me")
	if err != nil {
		t.Fatalf("BatchCancel returned error: %v", err)
	}

	if receivedMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", receivedMethod)
	}
	if receivedPath != "/batches/cancel-me:cancel" {
		t.Errorf("expected path '/batches/cancel-me:cancel', got %q", receivedPath)
	}
}

func TestBatchChat_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer server.Close()

	g := &Gemini{
		apiKey:          "test-key",
		model:           "test-model",
		httpClient:      server.Client(),
		temperature:     0.1,
		topP:            0.9,
		mediaResolution: "MEDIA_RESOLUTION_MEDIUM",
	}

	origBaseURL := baseURL
	defer func() { baseURL = origBaseURL }()
	baseURL = server.URL

	_, err := g.BatchChat(context.Background(), []oasis.ChatRequest{
		{Messages: []oasis.ChatMessage{{Role: "user", Content: "test"}}},
	})
	if err == nil {
		t.Fatal("expected error for HTTP 400")
	}

	httpErr, ok := err.(*oasis.ErrHTTP)
	if !ok {
		t.Fatalf("expected ErrHTTP, got %T: %v", err, err)
	}
	if httpErr.Status != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", httpErr.Status)
	}
}
