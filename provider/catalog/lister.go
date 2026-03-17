package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	oasis "github.com/nevindra/oasis"
)

// modelLister fetches and normalizes model lists from a provider API.
type modelLister interface {
	listModels(ctx context.Context, baseURL, apiKey string) ([]oasis.ModelInfo, error)
}

// --- OpenAI-compatible lister ---

type openaiLister struct {
	provider string
}

// openaiModelResponse matches the OpenAI /v1/models response.
// Extended fields (context_window, pricing, capabilities) are present
// in some providers (Groq, Together, Mistral) but not others.
type openaiModelResponse struct {
	Data   []openaiModel `json:"data"`
	Object string        `json:"object"`
}

type openaiModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`

	// Groq extensions
	Active              *bool `json:"active,omitempty"`
	ContextWindow       int   `json:"context_window,omitempty"`
	MaxCompletionTokens int   `json:"max_completion_tokens,omitempty"`

	// Together extensions
	DisplayName   string          `json:"display_name,omitempty"`
	Type          string          `json:"type,omitempty"` // "chat", "embedding", "image", etc.
	ContextLength int             `json:"context_length,omitempty"`
	Pricing       *togetherPricing `json:"pricing,omitempty"`

	// Mistral extensions
	Name               string           `json:"name,omitempty"`
	MaxContextLength   int              `json:"max_context_length,omitempty"`
	Capabilities       *mistralCaps     `json:"capabilities,omitempty"`
	Deprecation        *string          `json:"deprecation,omitempty"`
	DefaultModelTemp   *float64         `json:"default_model_temperature,omitempty"`
}

type togetherPricing struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

type mistralCaps struct {
	CompletionChat  bool `json:"completion_chat"`
	FunctionCalling bool `json:"function_calling"`
	Vision          bool `json:"vision"`
	Finetuning      bool `json:"fine_tuning"`
}

func (l *openaiLister) listModels(ctx context.Context, baseURL, apiKey string) ([]oasis.ModelInfo, error) {
	url := strings.TrimSuffix(baseURL, "/") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("catalog: create request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog: fetch models from %s: %w", l.provider, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("catalog: %s returned %d: %s", l.provider, resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("catalog: read response from %s: %w", l.provider, err)
	}

	// Try standard {"data": [...]} format first.
	var modelResp openaiModelResponse
	if err := json.Unmarshal(body, &modelResp); err == nil && len(modelResp.Data) > 0 {
		return l.normalize(modelResp.Data), nil
	}

	// Fallback: try raw array (Together returns this format).
	var models []openaiModel
	if err := json.Unmarshal(body, &models); err == nil && len(models) > 0 {
		return l.normalize(models), nil
	}

	return nil, fmt.Errorf("catalog: could not parse models response from %s", l.provider)
}

func (l *openaiLister) normalize(models []openaiModel) []oasis.ModelInfo {
	out := make([]oasis.ModelInfo, 0, len(models))
	for _, m := range models {
		if m.Active != nil && !*m.Active {
			continue // Groq: skip inactive models
		}

		info := oasis.ModelInfo{
			ID:       m.ID,
			Provider: l.provider,
			Status:   oasis.ModelStatusAvailable,
		}

		// Display name: Together > Mistral > empty
		if m.DisplayName != "" {
			info.DisplayName = m.DisplayName
		} else if m.Name != "" {
			info.DisplayName = m.Name
		}

		// Context window: Groq > Together > Mistral
		if m.ContextWindow > 0 {
			info.InputContext = m.ContextWindow
		} else if m.ContextLength > 0 {
			info.InputContext = m.ContextLength
		} else if m.MaxContextLength > 0 {
			info.InputContext = m.MaxContextLength
		}
		if m.MaxCompletionTokens > 0 {
			info.OutputContext = m.MaxCompletionTokens
		}

		// Capabilities: Mistral provides explicit flags.
		if m.Capabilities != nil {
			info.Capabilities.Chat = m.Capabilities.CompletionChat
			info.Capabilities.ToolUse = m.Capabilities.FunctionCalling
			info.Capabilities.Vision = m.Capabilities.Vision
		} else if m.Type != "" {
			// Together: infer from type field.
			switch m.Type {
			case "chat":
				info.Capabilities.Chat = true
			case "embedding":
				info.Capabilities.Embedding = true
			}
		}

		// Pricing: Together only.
		if m.Pricing != nil {
			info.Pricing = parseTogetherPricing(m.Pricing)
		}

		// Deprecation: Mistral only.
		if m.Deprecation != nil && *m.Deprecation != "" {
			info.Deprecated = true
			info.DeprecationMsg = *m.Deprecation
		}

		out = append(out, info)
	}
	return out
}

// parseTogetherPricing converts Together's per-token string prices to per-million.
func parseTogetherPricing(p *togetherPricing) *oasis.ModelPricing {
	var input, output float64
	fmt.Sscanf(p.Input, "%g", &input)
	fmt.Sscanf(p.Output, "%g", &output)
	if input == 0 && output == 0 {
		return nil
	}
	return &oasis.ModelPricing{
		InputPerMillion:  input * 1_000_000,
		OutputPerMillion: output * 1_000_000,
	}
}

// --- Gemini lister ---

type geminiLister struct{}

type geminiModelsResponse struct {
	Models        []geminiModel `json:"models"`
	NextPageToken string        `json:"nextPageToken"`
}

type geminiModel struct {
	Name                       string   `json:"name"` // "models/gemini-2.5-flash"
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

func (l *geminiLister) listModels(ctx context.Context, baseURL, apiKey string) ([]oasis.ModelInfo, error) {
	var all []geminiModel
	pageToken := ""

	for {
		url := strings.TrimSuffix(baseURL, "/") + "/models?key=" + apiKey + "&pageSize=100"
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("catalog: create gemini request: %w", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("catalog: fetch gemini models: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("catalog: gemini returned %d: %s", resp.StatusCode, truncate(body, 512))
		}
		if err != nil {
			return nil, fmt.Errorf("catalog: read gemini response: %w", err)
		}

		var page geminiModelsResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("catalog: parse gemini response: %w", err)
		}

		all = append(all, page.Models...)

		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}

	return normalizeGemini(all), nil
}

func normalizeGemini(models []geminiModel) []oasis.ModelInfo {
	out := make([]oasis.ModelInfo, 0, len(models))
	for _, m := range models {
		// Strip "models/" prefix from name.
		id := m.Name
		if strings.HasPrefix(id, "models/") {
			id = id[len("models/"):]
		}

		info := oasis.ModelInfo{
			ID:            id,
			Provider:      "gemini",
			DisplayName:   m.DisplayName,
			InputContext:   m.InputTokenLimit,
			OutputContext:  m.OutputTokenLimit,
			Status:        oasis.ModelStatusAvailable,
		}

		// Infer capabilities from supportedGenerationMethods.
		for _, method := range m.SupportedGenerationMethods {
			switch method {
			case "generateContent":
				info.Capabilities.Chat = true
			case "embedContent":
				info.Capabilities.Embedding = true
			}
		}

		out = append(out, info)
	}
	return out
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
