package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchModelsDevProviders(t *testing.T) {
	data := map[string]modelsDevProvider{
		"openai": {
			ID:   "openai",
			Name: "OpenAI",
			API:  "https://api.openai.com/v1",
			Env:  []string{"OPENAI_API_KEY"},
			Models: map[string]modelsDevModel{
				"gpt-4o": {
					ID: "gpt-4o", Name: "GPT-4o", Family: "gpt-4",
					ToolCall: true, StructuredOutput: true, Attachment: true,
					Modalities: modelsDevModalities{
						Input: []string{"text", "image", "audio"}, Output: []string{"text"},
					},
					Cost:            &modelsDevCost{Input: 2.50, Output: 10.00, CacheRead: 1.25},
					Limit:           modelsDevLimit{Context: 128000, Output: 16384},
					KnowledgeCutoff: "2024-10", ReleaseDate: "2024-05-13",
				},
				"o3": {
					ID: "o3", Name: "o3", Family: "o3",
					Reasoning: true, ToolCall: true,
					Modalities: modelsDevModalities{
						Input: []string{"text", "image"}, Output: []string{"text"},
					},
					Cost:            &modelsDevCost{Input: 2.00, Output: 8.00},
					Limit:           modelsDevLimit{Context: 200000, Output: 100000},
					KnowledgeCutoff: "2025-03", ReleaseDate: "2025-01-31",
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(data)
	}))
	defer srv.Close()

	providers, err := fetchModelsDev(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	openai, ok := providers["openai"]
	if !ok {
		t.Fatal("openai provider not found")
	}
	if openai.Name != "OpenAI" {
		t.Errorf("name = %q, want 'OpenAI'", openai.Name)
	}
	if len(openai.Models) != 2 {
		t.Errorf("models count = %d, want 2", len(openai.Models))
	}
	gpt4o := openai.Models["gpt-4o"]
	if !gpt4o.ToolCall {
		t.Error("gpt-4o should have tool_call")
	}
	if !gpt4o.StructuredOutput {
		t.Error("gpt-4o should have structured_output")
	}
	if gpt4o.Cost.CacheRead != 1.25 {
		t.Errorf("cache_read = %f, want 1.25", gpt4o.Cost.CacheRead)
	}
	o3 := openai.Models["o3"]
	if !o3.Reasoning {
		t.Error("o3 should have reasoning")
	}
}

func TestModelsDevModelToModelInfo(t *testing.T) {
	m := modelsDevModel{
		ID: "gpt-4o", Name: "GPT-4o", Family: "gpt-4",
		ToolCall: true, StructuredOutput: true, Attachment: true,
		Modalities: modelsDevModalities{
			Input: []string{"text", "image"}, Output: []string{"text"},
		},
		Cost:            &modelsDevCost{Input: 2.50, Output: 10.00, CacheRead: 1.25},
		Limit:           modelsDevLimit{Context: 128000, Output: 16384},
		KnowledgeCutoff: "2024-10", ReleaseDate: "2024-05-13",
	}
	info := m.toModelInfo("openai")

	if info.ID != "gpt-4o" {
		t.Errorf("ID = %q", info.ID)
	}
	if info.Provider != "openai" {
		t.Errorf("Provider = %q", info.Provider)
	}
	if info.Family != "gpt-4" {
		t.Errorf("Family = %q", info.Family)
	}
	if !info.Capabilities.ToolUse {
		t.Error("expected ToolUse")
	}
	if !info.Capabilities.StructuredOutput {
		t.Error("expected StructuredOutput")
	}
	if !info.Capabilities.Attachment {
		t.Error("expected Attachment")
	}
	if info.Capabilities.Reasoning {
		t.Error("unexpected Reasoning")
	}
	if !info.Capabilities.Vision {
		t.Error("expected Vision derived from input modalities containing 'image'")
	}
	if info.Pricing == nil {
		t.Fatal("expected pricing")
	}
	if info.Pricing.CacheReadPerMillion != 1.25 {
		t.Errorf("CacheReadPerMillion = %f", info.Pricing.CacheReadPerMillion)
	}
	if info.InputContext != 128000 {
		t.Errorf("InputContext = %d", info.InputContext)
	}
	if info.KnowledgeCutoff != "2024-10" {
		t.Errorf("KnowledgeCutoff = %q", info.KnowledgeCutoff)
	}
}

func TestModelsDevDeprecatedFiltered(t *testing.T) {
	m := modelsDevModel{ID: "old-model", Name: "Old Model", Status: "deprecated"}
	info := m.toModelInfo("test")
	if !info.Deprecated {
		t.Error("expected Deprecated=true for status=deprecated")
	}
}

func TestModelsDevEmbeddingInference(t *testing.T) {
	// Test embedding inferred from model ID containing "embed"
	m := modelsDevModel{
		ID: "text-embedding-3-small", Name: "Text Embedding 3 Small",
		Modalities: modelsDevModalities{Input: []string{"text"}, Output: []string{"text"}},
	}
	info := m.toModelInfo("openai")
	if !info.Capabilities.Embedding {
		t.Error("expected Embedding inferred from model ID containing 'embed'")
	}

	// Test non-embedding model
	m2 := modelsDevModel{
		ID: "gpt-4o", Name: "GPT-4o",
		Modalities: modelsDevModalities{Input: []string{"text"}, Output: []string{"text"}},
	}
	info2 := m2.toModelInfo("openai")
	if info2.Capabilities.Embedding {
		t.Error("unexpected Embedding for gpt-4o")
	}
}
