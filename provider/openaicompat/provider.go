package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	oasis "github.com/nevindra/oasis"
)

// Provider implements oasis.Provider for any OpenAI-compatible API.
// It uses the shared helpers in this package (BuildBody, StreamSSE, ParseResponse)
// to handle body building, streaming, and response parsing.
//
// Works with OpenAI, OpenRouter, Groq, Together, Fireworks, DeepSeek, Mistral,
// Ollama, vLLM, LM Studio, Azure OpenAI, and any other provider that implements
// the OpenAI chat completions API.
type Provider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
	name    string
	opts    []Option
}

// NewProvider creates an OpenAI-compatible chat provider.
//
// baseURL is the API base (e.g. "https://api.openai.com/v1",
// "https://api.groq.com/openai/v1", "http://localhost:11434/v1").
// The /chat/completions path is appended automatically.
//
// Provider-level options (WithProviderTemperature, etc.) are applied to every
// request. Per-request options from BuildBody still work for callers using the
// helpers directly.
func NewProvider(apiKey, model, baseURL string, opts ...ProviderOption) *Provider {
	p := &Provider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{},
		name:    "openai",
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider name (default "openai", configurable via WithName).
func (p *Provider) Name() string { return p.name }

// Chat sends a non-streaming chat request and returns the complete response.
func (p *Provider) Chat(ctx context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
	body := BuildBody(req.Messages, nil, p.model, req.ResponseSchema, p.opts...)
	return p.doRequest(ctx, body)
}

// ChatWithTools sends a chat request with tool definitions.
func (p *Provider) ChatWithTools(ctx context.Context, req oasis.ChatRequest, tools []oasis.ToolDefinition) (oasis.ChatResponse, error) {
	body := BuildBody(req.Messages, tools, p.model, req.ResponseSchema, p.opts...)
	return p.doRequest(ctx, body)
}

// ChatStream streams text-delta events into ch, then returns the final accumulated response.
// The channel is closed when streaming completes (via StreamSSE) or on error.
func (p *Provider) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	body := BuildBody(req.Messages, nil, p.model, req.ResponseSchema, p.opts...)
	body.Stream = true
	body.StreamOptions = &StreamOptions{IncludeUsage: true}

	resp, err := p.sendHTTP(ctx, body)
	if err != nil {
		close(ch)
		return oasis.ChatResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		close(ch)
		return oasis.ChatResponse{}, p.httpErr(resp)
	}

	// StreamSSE closes ch when done.
	return StreamSSE(ctx, resp.Body, ch)
}

// doRequest sends a non-streaming request and parses the response.
func (p *Provider) doRequest(ctx context.Context, body ChatRequest) (oasis.ChatResponse, error) {
	resp, err := p.sendHTTP(ctx, body)
	if err != nil {
		return oasis.ChatResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return oasis.ChatResponse{}, p.httpErr(resp)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return oasis.ChatResponse{}, &oasis.ErrLLM{Provider: p.name, Message: fmt.Sprintf("decode response: %v", err)}
	}

	return ParseResponse(chatResp)
}

// sendHTTP marshals the request body and sends it to the chat completions endpoint.
func (p *Provider) sendHTTP(ctx context.Context, body ChatRequest) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.name, Message: fmt.Sprintf("marshal request: %v", err)}
	}

	url := p.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.name, Message: fmt.Sprintf("create request: %v", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	return p.client.Do(httpReq)
}

// httpErr reads the response body and returns an ErrHTTP for retry middleware.
// Parses the Retry-After header when present (429/503 responses).
func (p *Provider) httpErr(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return &oasis.ErrHTTP{
		Status:     resp.StatusCode,
		Body:       string(body),
		RetryAfter: oasis.ParseRetryAfter(resp.Header.Get("Retry-After")),
	}
}

// Compile-time interface check.
var _ oasis.Provider = (*Provider)(nil)
