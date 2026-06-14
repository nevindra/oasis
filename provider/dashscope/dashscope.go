// Package dashscope provides an oasis Provider for Alibaba DashScope's native
// multimodal-generation API — specifically Qwen-Image text-to-image generation.
//
// DashScope's image models are NOT served over the OpenAI-compatible
// chat/completions endpoint; they use a bespoke request/response shape:
//
//	POST {baseURL}/services/aigc/multimodal-generation/generation
//	{ "model": "...", "input": {"messages":[{"role":"user","content":[{"text":"..."}]}]},
//	  "parameters": {"n":1, "watermark":false, "prompt_extend":true} }
//
// The response returns image URLs (valid ~24h). This provider downloads the
// images and returns them as inline Attachments on the ChatResponse so callers
// can persist them to their own storage.
package dashscope

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	oasis "github.com/nevindra/oasis/core"
)

// Provider implements oasis.Provider for DashScope image generation.
type Provider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
	name    string
}

// New creates a DashScope image provider. baseURL is the API base, e.g.
// "https://dashscope-intl.aliyuncs.com/api/v1" (Singapore) or
// "https://dashscope.aliyuncs.com/api/v1" (Beijing).
//
// Use functional options to customise the provider:
//
//	dashscope.New(key, model, baseURL,
//	    dashscope.WithHTTPClient(myClient),
//	    dashscope.WithName("dashscope-intl"),
//	)
func New(apiKey, model, baseURL string, opts ...Option) *Provider {
	p := &Provider{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{},
		name:    "dashscope",
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name returns the provider name (default "dashscope", overridable via WithName).
func (p *Provider) Name() string { return p.name }

// --- request/response shapes ---

type genRequest struct {
	Model      string        `json:"model"`
	Input      genInput      `json:"input"`
	Parameters genParameters `json:"parameters"`
}

type genInput struct {
	Messages []genMessage `json:"messages"`
}

type genMessage struct {
	Role    string           `json:"role"`
	Content []genContentPart `json:"content"`
}

type genContentPart struct {
	Text string `json:"text"`
}

type genParameters struct {
	N            int    `json:"n,omitempty"`
	Size         string `json:"size,omitempty"`
	Watermark    bool   `json:"watermark"`
	PromptExtend bool   `json:"prompt_extend"`
}

type genResponse struct {
	Output struct {
		Choices []struct {
			Message struct {
				Content []struct {
					Image string `json:"image"`
				} `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	} `json:"output"`
	Usage struct {
		ImageCount int `json:"image_count"`
	} `json:"usage"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// ChatStream generates images for the prompt carried in req.Messages and
// returns them as Attachments. The channel (if non-nil) is closed before
// returning; no incremental events are emitted.
//
// Two DashScope request styles are dispatched by model family:
//   - Qwen-Image (and similar single-shot models): plain synchronous request.
//   - Wan image models: text-to-image only works via interleaved mode
//     (enable_interleave=true), which on the sync endpoint requires SSE
//     streaming — so those are streamed and every image part is collected.
func (p *Provider) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	if ch != nil {
		defer close(ch)
	}

	prompt := lastUserText(req.Messages)
	if prompt == "" {
		return oasis.ChatResponse{}, &oasis.ErrLLM{Provider: p.Name(), Message: "no prompt text in request"}
	}

	var (
		attachments []oasis.Attachment
		err         error
	)
	if isWanModel(p.model) {
		attachments, err = p.generateInterleaved(ctx, prompt)
	} else {
		attachments, err = p.generateSync(ctx, prompt)
	}
	if err != nil {
		return oasis.ChatResponse{}, err
	}

	return oasis.ChatResponse{
		Attachments:  attachments,
		FinishReason: oasis.FinishStop,
	}, nil
}

// generateSync handles single-shot models (Qwen-Image) via the synchronous
// multimodal-generation endpoint.
func (p *Provider) generateSync(ctx context.Context, prompt string) ([]oasis.Attachment, error) {
	payload, err := json.Marshal(genRequest{
		Model: p.model,
		Input: genInput{Messages: []genMessage{{
			Role:    "user",
			Content: []genContentPart{{Text: prompt}},
		}}},
		Parameters: genParameters{N: 1, Watermark: false, PromptExtend: true},
	})
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "marshal request: " + err.Error()}
	}

	resp, err := p.post(ctx, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, p.httpErr(resp, respBody)
	}

	var parsed genResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "decode response: " + err.Error()}
	}
	if parsed.Code != "" {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: parsed.Code + ": " + parsed.Message}
	}
	return p.downloadImages(ctx, parsed)
}

// generateInterleaved handles Wan image models. Text-to-image requires
// enable_interleave=true, which on the sync endpoint forces SSE streaming.
// We use the asynchronous endpoint instead (create task → poll), which needs
// no streaming and returns the same image-bearing choices.
func (p *Provider) generateInterleaved(ctx context.Context, prompt string) ([]oasis.Attachment, error) {
	payload, err := json.Marshal(map[string]any{
		"model": p.model,
		"input": map[string]any{
			"messages": []map[string]any{{
				"role":    "user",
				"content": []map[string]any{{"text": prompt}},
			}},
		},
		"parameters": map[string]any{
			"enable_interleave": true,
			"max_images":        1,
			"size":              "1280*1280",
			"watermark":         false,
		},
	})
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "marshal request: " + err.Error()}
	}

	// Create the async task.
	createURL := p.baseURL + "/services/aigc/image-generation/generation"
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(payload))
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "create request: " + err.Error()}
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	createReq.Header.Set("X-DashScope-Async", "enable")

	resp, err := p.client.Do(createReq)
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "request failed: " + err.Error()}
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, p.httpErr(resp, body)
	}

	var created struct {
		Output struct {
			TaskID     string `json:"task_id"`
			TaskStatus string `json:"task_status"`
		} `json:"output"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "decode create response: " + err.Error()}
	}
	if created.Code != "" {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: created.Code + ": " + created.Message}
	}
	if created.Output.TaskID == "" {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "no task_id returned"}
	}

	return p.pollTask(ctx, created.Output.TaskID)
}

// pollTask polls an async DashScope task until it succeeds, then downloads the
// generated images.
func (p *Provider) pollTask(ctx context.Context, taskID string) ([]oasis.Attachment, error) {
	taskURL := p.baseURL + "/tasks/" + taskID
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, taskURL, nil)
		if err != nil {
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "create poll request: " + err.Error()}
		}
		req.Header.Set("Authorization", "Bearer "+p.apiKey)

		resp, err := p.client.Do(req)
		if err != nil {
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "poll failed: " + err.Error()}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, p.httpErr(resp, body)
		}

		var task struct {
			Output struct {
				TaskStatus string `json:"task_status"`
				Choices    []struct {
					Message struct {
						Content []struct {
							Image string `json:"image"`
						} `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"output"`
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &task); err != nil {
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "decode poll response: " + err.Error()}
		}
		if task.Code != "" {
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: task.Code + ": " + task.Message}
		}

		switch task.Output.TaskStatus {
		case "SUCCEEDED":
			var attachments []oasis.Attachment
			for _, choice := range task.Output.Choices {
				for _, part := range choice.Message.Content {
					if part.Image == "" {
						continue
					}
					att, derr := p.download(ctx, part.Image)
					if derr != nil {
						return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "download image: " + derr.Error()}
					}
					attachments = append(attachments, att)
				}
			}
			return attachments, nil
		case "FAILED", "CANCELED", "UNKNOWN":
			msg := task.Output.Message
			if msg == "" {
				msg = "task " + task.Output.TaskStatus
			}
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: msg}
		default:
			// PENDING / RUNNING — keep polling.
		}
	}
}

// post sends a synchronous multimodal-generation request (Qwen-Image path).
func (p *Provider) post(ctx context.Context, payload []byte) (*http.Response, error) {
	url := p.baseURL + "/services/aigc/multimodal-generation/generation"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "create request: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "request failed: " + err.Error()}
	}
	return resp, nil
}

func (p *Provider) httpErr(resp *http.Response, body []byte) error {
	return &oasis.ErrHTTP{
		Status:     resp.StatusCode,
		Body:       string(body),
		RetryAfter: oasis.ParseRetryAfter(resp.Header.Get("Retry-After")),
	}
}

// downloadImages fetches every image URL in a parsed (non-streaming) response.
func (p *Provider) downloadImages(ctx context.Context, parsed genResponse) ([]oasis.Attachment, error) {
	var attachments []oasis.Attachment
	for _, choice := range parsed.Output.Choices {
		for _, part := range choice.Message.Content {
			if part.Image == "" {
				continue
			}
			att, derr := p.download(ctx, part.Image)
			if derr != nil {
				return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "download image: " + derr.Error()}
			}
			attachments = append(attachments, att)
		}
	}
	return attachments, nil
}

// isWanModel reports whether model is a Wan image model (text-to-image only via
// interleaved streaming mode).
func isWanModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "wan")
}

// download fetches a generated image URL and returns it as an inline Attachment.
func (p *Provider) download(ctx context.Context, url string) (oasis.Attachment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return oasis.Attachment{}, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return oasis.Attachment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return oasis.Attachment{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return oasis.Attachment{}, err
	}
	mime := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(mime, "image/") {
		mime = "image/png"
	}
	return oasis.Attachment{MimeType: mime, Data: data}, nil
}

// lastUserText returns the text of the last user message (falling back to any
// non-empty message content).
func lastUserText(messages []oasis.ChatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == oasis.RoleUser && messages[i].Content != "" {
			return messages[i].Content
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Content != "" {
			return messages[i].Content
		}
	}
	return ""
}

// Compile-time interface check.
var _ oasis.Provider = (*Provider)(nil)
