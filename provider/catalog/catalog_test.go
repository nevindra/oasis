package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	oasis "github.com/nevindra/oasis"
)

func TestNewModelCatalog(t *testing.T) {
	cat := NewModelCatalog()
	platforms := cat.Platforms()
	if len(platforms) == 0 {
		t.Fatal("expected built-in platforms, got none")
	}

	// Check that all built-in platforms are present.
	names := make(map[string]bool)
	for _, p := range platforms {
		names[p.Name] = true
	}
	for _, want := range []string{"OpenAI", "Gemini", "Groq", "DeepSeek", "Qwen", "Together", "Mistral"} {
		if !names[want] {
			t.Errorf("missing built-in platform %q", want)
		}
	}
}

func TestRegisterPlatform(t *testing.T) {
	cat := NewModelCatalog()
	initial := len(cat.Platforms())

	err := cat.RegisterPlatform(oasis.Platform{
		Name:    "Zai",
		BaseURL: "https://api.zai.ai/v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := len(cat.Platforms()); got != initial+1 {
		t.Errorf("platform count = %d, want %d", got, initial+1)
	}

	// Validation: empty name.
	if err := cat.RegisterPlatform(oasis.Platform{BaseURL: "https://x"}); err == nil {
		t.Error("expected error for empty name")
	}

	// Validation: empty BaseURL.
	if err := cat.RegisterPlatform(oasis.Platform{Name: "X"}); err == nil {
		t.Error("expected error for empty BaseURL")
	}
}

func TestAddAndRemove(t *testing.T) {
	cat := NewModelCatalog()

	// Add known platform.
	if err := cat.Add("openai", "sk-test"); err != nil {
		t.Fatal(err)
	}

	// Case-insensitive.
	if err := cat.Add("OpenAI", "sk-test2"); err != nil {
		t.Fatal("case-insensitive add failed:", err)
	}

	// Unknown platform.
	if err := cat.Add("nonexistent", "key"); err == nil {
		t.Error("expected error for unknown platform")
	}

	// Remove.
	cat.Remove("openai")

	// Can't list after removal.
	_, err := cat.ListProvider(context.Background(), "openai")
	if err == nil {
		t.Error("expected error after Remove")
	}
}

func TestAddCustom(t *testing.T) {
	cat := NewModelCatalog()

	if err := cat.AddCustom("my-ollama", "http://192.168.1.50:11434/v1", ""); err != nil {
		t.Fatal(err)
	}

	// Empty identifier.
	if err := cat.AddCustom("", "http://x", ""); err == nil {
		t.Error("expected error for empty identifier")
	}

	// Empty baseURL.
	if err := cat.AddCustom("x", "", ""); err == nil {
		t.Error("expected error for empty baseURL")
	}
}

func TestMaxProviders(t *testing.T) {
	cat := NewModelCatalog(WithMaxProviders(2))

	if err := cat.AddCustom("p1", "http://a/v1", ""); err != nil {
		t.Fatal(err)
	}
	if err := cat.AddCustom("p2", "http://b/v1", ""); err != nil {
		t.Fatal(err)
	}
	if err := cat.AddCustom("p3", "http://c/v1", ""); err == nil {
		t.Error("expected error when max providers reached")
	}

	// Overwrite existing should not fail.
	if err := cat.AddCustom("p1", "http://a2/v1", ""); err != nil {
		t.Error("overwrite should not count against max:", err)
	}
}

func TestParseModelID(t *testing.T) {
	tests := []struct {
		input    string
		provider string
		model    string
	}{
		{"openai/gpt-4o", "openai", "gpt-4o"},
		{"gemini/gemini-2.5-flash", "gemini", "gemini-2.5-flash"},
		{"together/meta-llama/Llama-3.1-70B", "together", "meta-llama/Llama-3.1-70B"},
		{"invalid", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		p, m := oasis.ParseModelID(tt.input)
		if p != tt.provider || m != tt.model {
			t.Errorf("ParseModelID(%q) = (%q, %q), want (%q, %q)", tt.input, p, m, tt.provider, tt.model)
		}
	}
}

func TestListProviderWithMockServer(t *testing.T) {
	// Mock an OpenAI-compatible /models endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		resp := openaiModelResponse{
			Object: "list",
			Data: []openaiModel{
				{ID: "test-model-1", Object: "model", Created: 1000},
				{ID: "test-model-2", Object: "model", Created: 2000},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cat := NewModelCatalog()
	if err := cat.AddCustom("testprov", srv.URL+"/v1", ""); err != nil {
		t.Fatal(err)
	}

	models, err := cat.ListProvider(context.Background(), "testprov")
	if err != nil {
		t.Fatal(err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "test-model-1" || models[1].ID != "test-model-2" {
		t.Errorf("unexpected model IDs: %v", models)
	}
	if models[0].Provider != "testprov" {
		t.Errorf("expected provider 'testprov', got %q", models[0].Provider)
	}
	if models[0].Status != oasis.ModelStatusAvailable {
		t.Errorf("expected ModelStatusAvailable, got %d", models[0].Status)
	}
}

func TestMergeModels(t *testing.T) {
	static := []oasis.ModelInfo{
		{ID: "model-a", Provider: "test", DisplayName: "Model A", InputContext: 128000, Pricing: &oasis.ModelPricing{InputPerMillion: 1.0, OutputPerMillion: 2.0}},
		{ID: "model-b", Provider: "test", DisplayName: "Model B", InputContext: 64000},
	}
	live := []oasis.ModelInfo{
		{ID: "model-a", Provider: "test", Status: oasis.ModelStatusAvailable},
		{ID: "model-c", Provider: "test", Status: oasis.ModelStatusAvailable}, // new model, not in static
	}

	merged := mergeModels(static, live)

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged models, got %d", len(merged))
	}

	// model-a: live + static metadata
	a := findModel(merged, "model-a")
	if a == nil {
		t.Fatal("model-a not found")
	}
	if a.DisplayName != "Model A" {
		t.Errorf("model-a display name = %q, want 'Model A'", a.DisplayName)
	}
	if a.InputContext != 128000 {
		t.Errorf("model-a context = %d, want 128000", a.InputContext)
	}
	if a.Pricing == nil || a.Pricing.InputPerMillion != 1.0 {
		t.Error("model-a should have static pricing")
	}
	if a.Status != oasis.ModelStatusAvailable {
		t.Errorf("model-a status = %d, want Available", a.Status)
	}

	// model-b: static only, marked unavailable
	b := findModel(merged, "model-b")
	if b == nil {
		t.Fatal("model-b not found")
	}
	if b.Status != oasis.ModelStatusUnavailable {
		t.Errorf("model-b status = %d, want Unavailable", b.Status)
	}

	// model-c: live only, minimal metadata
	c := findModel(merged, "model-c")
	if c == nil {
		t.Fatal("model-c not found")
	}
	if c.Status != oasis.ModelStatusAvailable {
		t.Errorf("model-c status = %d, want Available", c.Status)
	}
}

func TestValidate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaiModelResponse{
			Object: "list",
			Data:   []openaiModel{{ID: "test-model", Object: "model"}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cat := NewModelCatalog()
	cat.AddCustom("tp", srv.URL+"/v1", "")

	// Valid model.
	if err := cat.Validate(context.Background(), "tp/test-model"); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}

	// Unknown model.
	if err := cat.Validate(context.Background(), "tp/nonexistent"); err == nil {
		t.Error("expected error for unknown model")
	}

	// Bad format.
	if err := cat.Validate(context.Background(), "noSlash"); err == nil {
		t.Error("expected error for bad format")
	}
}

func TestCacheTTL(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		resp := openaiModelResponse{
			Object: "list",
			Data:   []openaiModel{{ID: "m1", Object: "model"}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cat := NewModelCatalog(WithCatalogTTL(1 * time.Hour))
	cat.AddCustom("tp", srv.URL+"/v1", "")

	// First call fetches.
	cat.ListProvider(context.Background(), "tp")
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	// Second call uses cache.
	cat.ListProvider(context.Background(), "tp")
	if calls != 1 {
		t.Fatalf("expected cache hit, got %d calls", calls)
	}
}

func TestRefreshNone(t *testing.T) {
	cat := NewModelCatalog(WithRefresh(RefreshNone))
	cat.AddCustom("tp", "http://nonexistent:9999/v1", "")

	// Should not make any HTTP calls — returns static only.
	models, err := cat.ListProvider(context.Background(), "tp")
	if err != nil {
		t.Fatal(err)
	}
	// No static data for "tp", so empty.
	if len(models) != 0 {
		t.Errorf("expected 0 models with RefreshNone, got %d", len(models))
	}
}

func findModel(models []oasis.ModelInfo, id string) *oasis.ModelInfo {
	for i := range models {
		if models[i].ID == id {
			return &models[i]
		}
	}
	return nil
}
