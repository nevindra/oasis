package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	oasis "github.com/nevindra/oasis"
)

const modelsDevURL = "https://models.dev/api.json"

// modelsDevProvider represents a provider entry from models.dev/api.json.
type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	API    string                    `json:"api"`
	Env    []string                  `json:"env"`
	Doc    string                    `json:"doc"`
	Models map[string]modelsDevModel `json:"models"`
}

// modelsDevModel represents a model entry from models.dev.
type modelsDevModel struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Family           string              `json:"family"`
	ToolCall         bool                `json:"tool_call"`
	Reasoning        bool                `json:"reasoning"`
	StructuredOutput bool                `json:"structured_output"`
	Attachment       bool                `json:"attachment"`
	Temperature      bool                `json:"temperature"`
	Modalities       modelsDevModalities `json:"modalities"`
	Cost             *modelsDevCost      `json:"cost"`
	Limit            modelsDevLimit      `json:"limit"`
	OpenWeights      bool                `json:"open_weights"`
	KnowledgeCutoff  string              `json:"knowledge"`
	ReleaseDate      string              `json:"release_date"`
	Status           string              `json:"status"` // "deprecated" or empty
}

type modelsDevModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type modelsDevCost struct {
	Input      float64 `json:"input"`       // per 1M input tokens
	Output     float64 `json:"output"`      // per 1M output tokens
	CacheRead  float64 `json:"cache_read"`  // per 1M cached read tokens
	CacheWrite float64 `json:"cache_write"`
}

type modelsDevLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
	Input   int `json:"input"` // explicit input limit (rare, usually == context)
}

// fetchModelsDev fetches and parses the models.dev API.
func fetchModelsDev(ctx context.Context, url string) (map[string]modelsDevProvider, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("modelsdev: create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("modelsdev: fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("modelsdev: HTTP %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("modelsdev: read body: %w", err)
	}

	var providers map[string]modelsDevProvider
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, fmt.Errorf("modelsdev: parse: %w", err)
	}

	return providers, nil
}

// toModelInfo converts a models.dev model to an oasis.ModelInfo.
func (m modelsDevModel) toModelInfo(provider string) oasis.ModelInfo {
	info := oasis.ModelInfo{
		ID:               m.ID,
		Provider:         provider,
		DisplayName:      m.Name,
		Family:           m.Family,
		InputContext:     m.Limit.Context,
		OutputContext:    m.Limit.Output,
		InputModalities:  m.Modalities.Input,
		OutputModalities: m.Modalities.Output,
		OpenWeights:      m.OpenWeights,
		KnowledgeCutoff:  m.KnowledgeCutoff,
		ReleaseDate:      m.ReleaseDate,
		Capabilities: oasis.ModelCapabilities{
			Chat:             slices.Contains(m.Modalities.Output, "text"),
			Vision:           slices.Contains(m.Modalities.Input, "image"),
			ToolUse:          m.ToolCall,
			Embedding:        slices.Contains(m.Modalities.Output, "embedding") || strings.Contains(m.ID, "embed"),
			Reasoning:        m.Reasoning,
			StructuredOutput: m.StructuredOutput,
			Attachment:       m.Attachment,
		},
	}

	if m.Status == "deprecated" {
		info.Deprecated = true
	}

	if m.Cost != nil && (m.Cost.Input > 0 || m.Cost.Output > 0) {
		info.Pricing = &oasis.ModelPricing{
			InputPerMillion:      m.Cost.Input,
			OutputPerMillion:     m.Cost.Output,
			CacheReadPerMillion:  m.Cost.CacheRead,
			CacheWritePerMillion: m.Cost.CacheWrite,
		}
	}

	return info
}
