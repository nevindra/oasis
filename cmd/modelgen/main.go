// Command modelgen fetches model metadata from models.dev and generates
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
	"strings"
	"time"
)

const modelsDevURL = "https://models.dev/api.json"

type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	API    string                    `json:"api"`
	Env    []string                  `json:"env"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Family           string `json:"family"`
	ToolCall         bool   `json:"tool_call"`
	Reasoning        bool   `json:"reasoning"`
	StructuredOutput bool   `json:"structured_output"`
	Attachment       bool   `json:"attachment"`
	Modalities       struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Cost *struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	OpenWeights     bool   `json:"open_weights"`
	KnowledgeCutoff string `json:"knowledge"`
	ReleaseDate     string `json:"release_date"`
	Status          string `json:"status"`
}

type modelEntry struct {
	ID, Provider, DisplayName, Family      string
	InputContext, OutputContext             int
	InputModalities, OutputModalities      []string
	Chat, Vision, ToolUse, Embedding       bool
	Reasoning, StructuredOutput, Attachment bool
	OpenWeights                            bool
	KnowledgeCutoff, ReleaseDate           string
	Deprecated                             bool
	HasPricing                             bool
	InputPricing, OutputPricing            float64
	CacheReadPricing, CacheWritePricing    float64
}

type platformEntry struct {
	Name    string
	BaseURL string
	EnvVars []string
}

func main() {
	outPath := flag.String("out", "provider/catalog/models_gen.go", "output file path")
	platformsPath := flag.String("platforms-out", "provider/catalog/platforms_gen.go", "platforms output path")
	dryRun := flag.Bool("dry-run", false, "print to stdout instead of writing file")
	timeout := flag.Duration("timeout", 60*time.Second, "HTTP request timeout")
	flag.Parse()

	providers, err := fetchProviders(*timeout)
	if err != nil {
		log.Fatalf("fetch models.dev: %v", err)
	}

	entries, platforms := transform(providers)

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Provider != entries[j].Provider {
			return entries[i].Provider < entries[j].Provider
		}
		return entries[i].ID < entries[j].ID
	})
	sort.Slice(platforms, func(i, j int) bool {
		return platforms[i].Name < platforms[j].Name
	})

	modelsCode := generateModels(entries)
	platformsCode := generatePlatforms(platforms)

	if *dryRun {
		fmt.Print(modelsCode)
		fmt.Print("\n---\n")
		fmt.Print(platformsCode)
		return
	}

	if err := writeAtomic(*outPath, modelsCode); err != nil {
		log.Fatalf("write models: %v", err)
	}
	log.Printf("wrote %d models to %s", len(entries), *outPath)

	if err := writeAtomic(*platformsPath, platformsCode); err != nil {
		log.Fatalf("write platforms: %v", err)
	}
	log.Printf("wrote %d platforms to %s", len(platforms), *platformsPath)
}

func fetchProviders(timeout time.Duration) (map[string]modelsDevProvider, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(modelsDevURL)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", modelsDevURL, err)
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

	var providers map[string]modelsDevProvider
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return providers, nil
}

func transform(providers map[string]modelsDevProvider) ([]modelEntry, []platformEntry) {
	var entries []modelEntry
	var platforms []platformEntry

	for provID, prov := range providers {
		if len(prov.Models) == 0 {
			continue
		}
		if prov.API != "" {
			platforms = append(platforms, platformEntry{
				Name: provID, BaseURL: prov.API, EnvVars: prov.Env,
			})
		}

		for modelID, m := range prov.Models {
			if m.Status == "deprecated" {
				continue
			}
			id := modelID
			if idx := strings.Index(id, "/"); idx >= 0 {
				id = id[idx+1:]
			}

			entry := modelEntry{
				ID: id, Provider: provID, DisplayName: m.Name, Family: m.Family,
				InputContext: m.Limit.Context, OutputContext: m.Limit.Output,
				InputModalities: m.Modalities.Input, OutputModalities: m.Modalities.Output,
				Chat: contains(m.Modalities.Output, "text"), Vision: contains(m.Modalities.Input, "image"),
				ToolUse: m.ToolCall, Reasoning: m.Reasoning,
				StructuredOutput: m.StructuredOutput, Attachment: m.Attachment,
				Embedding:   contains(m.Modalities.Output, "embedding") || strings.Contains(id, "embed"),
				OpenWeights: m.OpenWeights, KnowledgeCutoff: m.KnowledgeCutoff, ReleaseDate: m.ReleaseDate,
			}

			if m.Cost != nil && (m.Cost.Input > 0 || m.Cost.Output > 0) {
				entry.HasPricing = true
				entry.InputPricing = m.Cost.Input
				entry.OutputPricing = m.Cost.Output
				entry.CacheReadPricing = m.Cost.CacheRead
				entry.CacheWritePricing = m.Cost.CacheWrite
			}
			entries = append(entries, entry)
		}
	}
	return entries, platforms
}

func generateModels(entries []modelEntry) string {
	var b strings.Builder
	b.WriteString("// Code generated by modelgen; DO NOT EDIT.\n//\n")
	b.WriteString(fmt.Sprintf("// Generated at: %s\n", time.Now().UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("// Source: %s\n", modelsDevURL))
	b.WriteString(fmt.Sprintf("// Models: %d\n", len(entries)))
	b.WriteString("\npackage catalog\n\nimport oasis \"github.com/nevindra/oasis\"\n\n")
	b.WriteString("// staticModels is the pre-compiled model registry.\n")
	b.WriteString("// Updated by: go generate ./provider/catalog/...\n")
	b.WriteString("// Source: models.dev API\n")
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
		if e.Family != "" {
			b.WriteString(fmt.Sprintf(", Family: %q", e.Family))
		}
		if e.InputContext > 0 {
			b.WriteString(fmt.Sprintf(", InputContext: %d", e.InputContext))
		}
		if e.OutputContext > 0 {
			b.WriteString(fmt.Sprintf(", OutputContext: %d", e.OutputContext))
		}
		if len(e.InputModalities) > 0 {
			b.WriteString(fmt.Sprintf(", InputModalities: []string{%s}", quotedSlice(e.InputModalities)))
		}
		if len(e.OutputModalities) > 0 {
			b.WriteString(fmt.Sprintf(", OutputModalities: []string{%s}", quotedSlice(e.OutputModalities)))
		}
		var caps []string
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
		if e.Reasoning {
			caps = append(caps, "Reasoning: true")
		}
		if e.StructuredOutput {
			caps = append(caps, "StructuredOutput: true")
		}
		if e.Attachment {
			caps = append(caps, "Attachment: true")
		}
		if len(caps) > 0 {
			b.WriteString(fmt.Sprintf(", Capabilities: oasis.ModelCapabilities{%s}", strings.Join(caps, ", ")))
		}
		if e.OpenWeights {
			b.WriteString(", OpenWeights: true")
		}
		if e.KnowledgeCutoff != "" {
			b.WriteString(fmt.Sprintf(", KnowledgeCutoff: %q", e.KnowledgeCutoff))
		}
		if e.ReleaseDate != "" {
			b.WriteString(fmt.Sprintf(", ReleaseDate: %q", e.ReleaseDate))
		}
		if e.HasPricing {
			b.WriteString(", Pricing: &oasis.ModelPricing{")
			b.WriteString(fmt.Sprintf("InputPerMillion: %.2f, OutputPerMillion: %.2f", e.InputPricing, e.OutputPricing))
			if e.CacheReadPricing > 0 {
				b.WriteString(fmt.Sprintf(", CacheReadPerMillion: %.4f", e.CacheReadPricing))
			}
			if e.CacheWritePricing > 0 {
				b.WriteString(fmt.Sprintf(", CacheWritePerMillion: %.4f", e.CacheWritePricing))
			}
			b.WriteString("}")
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func generatePlatforms(platforms []platformEntry) string {
	var b strings.Builder
	b.WriteString("// Code generated by modelgen; DO NOT EDIT.\n//\n")
	b.WriteString(fmt.Sprintf("// Generated at: %s\n", time.Now().UTC().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("// Source: %s\n", modelsDevURL))
	b.WriteString(fmt.Sprintf("// Platforms: %d\n", len(platforms)))
	b.WriteString("\npackage catalog\n\nimport oasis \"github.com/nevindra/oasis\"\n\n")
	b.WriteString("func init() {\n")
	b.WriteString("\tgeneratedPlatforms = []oasis.Platform{\n")

	for _, p := range platforms {
		b.WriteString(fmt.Sprintf("\t\t{Name: %q, Protocol: oasis.ProtocolOpenAICompat, BaseURL: %q", p.Name, p.BaseURL))
		if len(p.EnvVars) > 0 {
			b.WriteString(fmt.Sprintf(", EnvVars: []string{%s}", quotedSlice(p.EnvVars)))
		}
		b.WriteString("},\n")
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

func quotedSlice(s []string) string {
	quoted := make([]string, len(s))
	for i, v := range s {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}

func writeAtomic(path, data string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "gen_*.go.tmp")
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
