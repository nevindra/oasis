package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	oasis "github.com/nevindra/oasis"
)

// EmbedRequest is the OpenAI-compatible embedding request body.
type EmbedRequest struct {
	Model      string `json:"model"`
	Input      any    `json:"input"`                 // []string for text, []Message for multimodal
	Dimensions int    `json:"dimensions,omitempty"`
}

// EmbedResponse is the OpenAI-compatible embedding response.
type EmbedResponse struct {
	Data []EmbedData `json:"data"`
}

// EmbedData holds a single embedding result.
type EmbedData struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// EmbeddingOption configures an Embedding provider.
type EmbeddingOption func(*Embedding)

// WithEmbeddingName sets the provider name (default "openai").
func WithEmbeddingName(name string) EmbeddingOption {
	return func(e *Embedding) { e.name = name }
}

// WithEmbeddingHTTPClient sets a custom HTTP client.
func WithEmbeddingHTTPClient(c *http.Client) EmbeddingOption {
	return func(e *Embedding) { e.client = c }
}

// Embedding implements oasis.EmbeddingProvider for any OpenAI-compatible
// embedding API (OpenAI, vLLM, Ollama, etc.).
type Embedding struct {
	apiKey  string
	model   string
	baseURL string
	dims    int
	client  *http.Client
	name    string
}

// NewEmbedding creates an OpenAI-compatible embedding provider.
// baseURL is the API base (e.g. "https://api.openai.com/v1",
// "http://localhost:8000/v1" for vLLM).
// The /embeddings path is appended automatically.
func NewEmbedding(apiKey, model, baseURL string, dims int, opts ...EmbeddingOption) *Embedding {
	e := &Embedding{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		dims:    dims,
		client:  &http.Client{},
		name:    "openai",
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Name returns the provider name.
func (e *Embedding) Name() string { return e.name }

// Dimensions returns the configured embedding dimensionality.
func (e *Embedding) Dimensions() int { return e.dims }

// Embed returns embedding vectors for the given texts.
func (e *Embedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	req := EmbedRequest{
		Model: e.model,
		Input: texts,
	}
	if e.dims > 0 {
		req.Dimensions = e.dims
	}
	return e.doEmbed(ctx, req)
}

// EmbedMultimodal embeds multimodal inputs (text, images, or both).
// Uses the vLLM/OpenAI chat message format for the input field:
//
//	{"input": [{"role": "user", "content": [{"type": "text", ...}, {"type": "image_url", ...}]}]}
//
// Text-only inputs are sent as simple chat messages. Image attachments are
// converted to image_url content blocks with data URIs (inline) or URLs.
func (e *Embedding) EmbedMultimodal(ctx context.Context, inputs []oasis.MultimodalInput) ([][]float32, error) {
	msgs := make([]Message, len(inputs))
	for i, input := range inputs {
		var blocks []ContentBlock
		if input.Text != "" {
			blocks = append(blocks, ContentBlock{Type: "text", Text: input.Text})
		}
		for _, att := range input.Attachments {
			url := att.URL
			if url == "" {
				url = fmt.Sprintf("data:%s;base64,%s",
					att.MimeType, base64.StdEncoding.EncodeToString(att.InlineData()))
			}
			blocks = append(blocks, ContentBlock{
				Type:     "image_url",
				ImageURL: &ImageURL{URL: url},
			})
		}
		msgs[i] = Message{Role: "user", Content: blocks}
	}

	req := EmbedRequest{
		Model: e.model,
		Input: msgs,
	}
	if e.dims > 0 {
		req.Dimensions = e.dims
	}
	return e.doEmbed(ctx, req)
}

// doEmbed sends the embedding request and parses the response.
func (e *Embedding) doEmbed(ctx context.Context, req EmbedRequest) ([][]float32, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: e.name, Message: "marshal embed request: " + err.Error()}
	}

	url := e.baseURL + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: e.name, Message: "create embed request: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: e.name, Message: "embed request failed: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, &oasis.ErrHTTP{
			Status:     resp.StatusCode,
			Body:       string(body),
			RetryAfter: oasis.ParseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	var embedResp EmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, &oasis.ErrLLM{Provider: e.name, Message: "decode embed response: " + err.Error()}
	}

	vecs := make([][]float32, len(embedResp.Data))
	for _, d := range embedResp.Data {
		if d.Index < len(vecs) {
			vecs[d.Index] = d.Embedding
		}
	}
	return vecs, nil
}
