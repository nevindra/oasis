// Command modelgen fetches model metadata from OpenRouter and generates
// provider/catalog/models_gen.go with a static model registry.
//
// Usage:
//
//	go run ./cmd/modelgen                     # write to provider/catalog/models_gen.go
//	go run ./cmd/modelgen -out models.go      # custom output path
//	go run ./cmd/modelgen -dry-run            # print to stdout
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const openRouterURL = "https://openrouter.ai/api/frontend/models"

// providerMap maps OpenRouter author slugs to Oasis platform identifiers.
var providerMap = map[string]string{
	"openai":    "openai",
	"google":    "gemini",
	"deepseek":  "deepseek",
	"meta":      "together",  // Meta models typically via Together
	"mistralai": "mistral",
	"mistral":   "mistral",
	"qwen":      "qwen",
	"groq":      "groq",
	"anthropic": "anthropic",
	"cohere":    "cohere",
	"x-ai":     "xai",
}

// openRouterResponse is the top-level API response.
type openRouterResponse struct {
	Data []openRouterModel `json:"data"`
}

// openRouterModel represents a single model from OpenRouter's API.
type openRouterModel struct {
	Slug               string         `json:"slug"` // "openai/gpt-4o"
	Name               string         `json:"name"` // "OpenAI: GPT-4o"
	ShortName          string         `json:"short_name"`
	Author             string         `json:"author"`
	ContextLength      int            `json:"context_length"`
	MaxCompletionTokens int           `json:"max_completion_tokens"`
	InputModalities    []string       `json:"input_modalities"`
	OutputModalities   []string       `json:"output_modalities"`
	Pricing            *orPricing     `json:"pricing"`
	SupportsToolParams bool           `json:"supports_tool_parameters"`
	SupportsReasoning  bool           `json:"supports_reasoning"`
	IsFree             bool           `json:"is_free"`
	DeprecationDate    *string        `json:"deprecation_date"`
}

type orPricing struct {
	Prompt     string `json:"prompt"`     // per-token cost as string
	Completion string `json:"completion"` // per-token cost as string
}

// modelEntry is the intermediate representation before code generation.
type modelEntry struct {
	ID            string
	Provider      string
	DisplayName   string
	InputContext  int
	OutputContext int
	Chat          bool
	Vision        bool
	ToolUse       bool
	Embedding     bool
	Deprecated    bool
	InputPricing  float64 // per million
	OutputPricing float64 // per million
	HasPricing    bool
}

func main() {
	outPath := flag.String("out", "provider/catalog/models_gen.go", "output file path")
	dryRun := flag.Bool("dry-run", false, "print to stdout instead of writing file")
	timeout := flag.Duration("timeout", 30*time.Second, "HTTP request timeout")
	flag.Parse()

	models, err := fetchModels(*timeout)
	if err != nil {
		log.Fatalf("fetch models: %v", err)
	}

	entries := transform(models)

	// Sort by provider, then by ID for stable output.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].ID < entries[j].ID
	})

	code := generate(entries)

	if *dryRun {
		fmt.Print(code)
		return
	}

	if err := writeAtomic(*outPath, code); err != nil {
		log.Fatalf("write: %v", err)
	}

	log.Printf("wrote %d models to %s", len(entries), *outPath)
}

func fetchModels(timeout time.Duration) ([]openRouterModel, error) {
	client := &http.Client{Timeout: timeout}

	resp, err := client.Get(openRouterURL)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", openRouterURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Try {"data": [...]} format.
	var wrapped openRouterResponse
	if err := json.Unmarshal(body, &wrapped); err == nil && len(wrapped.Data) > 0 {
		return wrapped.Data, nil
	}

	// Try raw array.
	var models []openRouterModel
	if err := json.Unmarshal(body, &models); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return models, nil
}

func transform(models []openRouterModel) []modelEntry {
	var entries []modelEntry

	for _, m := range models {
		// Skip deprecated models.
		if m.DeprecationDate != nil && *m.DeprecationDate != "" {
			continue
		}

		// Map author to our provider identifier.
		provider, ok := providerMap[m.Author]
		if !ok {
			// Use author slug directly for unknown providers.
			provider = m.Author
		}

		// Extract model ID from slug (strip "author/" prefix).
		modelID := m.Slug
		if idx := strings.Index(modelID, "/"); idx >= 0 {
			modelID = modelID[idx+1:]
		}

		entry := modelEntry{
			ID:            modelID,
			Provider:      provider,
			DisplayName:   m.ShortName,
			InputContext:  m.ContextLength,
			OutputContext: m.MaxCompletionTokens,
			Chat:          contains(m.OutputModalities, "text"),
			Vision:        contains(m.InputModalities, "image"),
			ToolUse:       m.SupportsToolParams,
		}

		// Parse pricing (per-token → per-million).
		if m.Pricing != nil {
			input := parseFloat(m.Pricing.Prompt)
			output := parseFloat(m.Pricing.Completion)
			if input > 0 || output > 0 || m.IsFree {
				entry.HasPricing = true
				entry.InputPricing = input * 1_000_000
				entry.OutputPricing = output * 1_000_000
			}
		}

		entries = append(entries, entry)
	}

	return entries
}

func generate(entries []modelEntry) string {
	var b strings.Builder

	b.WriteString("// Code generated by modelgen; DO NOT EDIT.\n")
	b.WriteString("//\n")
	b.WriteString(fmt.Sprintf("// Generated at: %s\n", time.Now().UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("// Source: %s\n", openRouterURL))
	b.WriteString(fmt.Sprintf("// Models: %d\n", len(entries)))
	b.WriteString("\npackage catalog\n\n")
	b.WriteString("import oasis \"github.com/nevindra/oasis\"\n\n")
	b.WriteString("// staticModels is the pre-compiled model registry.\n")
	b.WriteString("// Updated by: go generate ./provider/catalog/...\n")
	b.WriteString("// Sources: OpenRouter API\n")
	b.WriteString("var staticModels = []oasis.ModelInfo{\n")

	currentProvider := ""
	for _, e := range entries {
		if e.Provider != currentProvider {
			if currentProvider != "" {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("\t// --- %s ---\n", e.Provider))
			currentProvider = e.Provider
		}

		b.WriteString("\t{")
		b.WriteString(fmt.Sprintf("ID: %q, Provider: %q", e.ID, e.Provider))

		if e.DisplayName != "" {
			b.WriteString(fmt.Sprintf(", DisplayName: %q", e.DisplayName))
		}
		if e.InputContext > 0 {
			b.WriteString(fmt.Sprintf(", InputContext: %d", e.InputContext))
		}
		if e.OutputContext > 0 {
			b.WriteString(fmt.Sprintf(", OutputContext: %d", e.OutputContext))
		}

		// Capabilities
		caps := []string{}
		if e.Chat {
			caps = append(caps, "Chat: true")
		}
		if e.Vision {
			caps = append(caps, "Vision: true")
		}
		if e.ToolUse {
			caps = append(caps, "ToolUse: true")
		}
		if e.Embedding {
			caps = append(caps, "Embedding: true")
		}
		if len(caps) > 0 {
			b.WriteString(fmt.Sprintf(", Capabilities: oasis.ModelCapabilities{%s}", strings.Join(caps, ", ")))
		}

		if e.HasPricing {
			b.WriteString(fmt.Sprintf(", Pricing: &oasis.ModelPricing{InputPerMillion: %.2f, OutputPerMillion: %.2f}", e.InputPricing, e.OutputPricing))
		}

		b.WriteString("},\n")
	}

	b.WriteString("}\n")
	return b.String()
}

// writeAtomic writes data to a temporary file and renames it to path.
func writeAtomic(path, data string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "models_gen_*.go.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.WriteString(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, path)
}

func contains(s []string, v string) bool {
	for _, item := range s {
		if item == v {
			return true
		}
	}
	return false
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
