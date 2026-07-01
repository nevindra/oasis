// Package dashscope provides an oasis Provider for Alibaba DashScope's native
// multimodal-generation API — supporting Qwen-Image text-to-image generation,
// Wan image generation (text-to-image via interleaved mode), and Wan video
// generation (text-to-video, image-to-video, and video editing).
//
// DashScope's image and video models are NOT served over the OpenAI-compatible
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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	oasis "github.com/nevindra/oasis/core"
)

// Provider implements oasis.Provider for DashScope image and video generation.
type Provider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
	name    string

	// downloadVideo, when set, downloads the generated video bytes inline
	// instead of returning a URL reference (see WithDownloadVideo).
	downloadVideo bool
}

// New creates a DashScope image/video provider. baseURL is the API base, e.g.
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

// ChatStream generates images or a video for the prompt carried in req.Messages
// and returns them as Attachments. The channel (if non-nil) is closed before
// returning; no incremental events are emitted.
//
// Three DashScope request styles are dispatched by model family:
//   - Wan video models (t2v/i2v/videoedit): asynchronous video-synthesis task
//     (create → poll), returning a single video URL (or inline bytes).
//   - Wan image models: text-to-image only works via interleaved mode
//     (enable_interleave=true), so those use the asynchronous image-generation
//     task (create → poll) and every image part is collected.
//   - Qwen-Image (and similar single-shot models): plain synchronous request.
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
	// Why: video model ids also start with "wan", so isVideoModel must be
	// checked before isWanModel — order matters.
	switch {
	case isVideoModel(p.model):
		attachments, err = p.generateVideo(ctx, prompt, lastUserAttachments(req.Messages), req.Video)
	case isWanModel(p.model):
		attachments, err = p.generateInterleaved(ctx, prompt)
	default:
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

	createURL := p.baseURL + "/services/aigc/image-generation/generation"
	taskID, err := p.createAsyncTask(ctx, createURL, payload)
	if err != nil {
		return nil, err
	}
	return p.pollTask(ctx, taskID)
}

// generateVideo handles Wan 2.7 video models (t2v / i2v / videoedit) via the
// asynchronous video-synthesis endpoint (create task → poll). The result is a
// single video URL (downloaded inline only when WithDownloadVideo is set).
func (p *Provider) generateVideo(ctx context.Context, prompt string, atts []oasis.Attachment, v *oasis.VideoOptions) ([]oasis.Attachment, error) {
	input, err := p.videoInput(prompt, atts, v)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(map[string]any{
		"model":      p.model,
		"input":      input,
		"parameters": videoParameters(v),
	})
	if err != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "marshal request: " + err.Error()}
	}

	createURL := p.baseURL + "/services/aigc/video-generation/video-synthesis"
	taskID, err := p.createAsyncTask(ctx, createURL, payload)
	if err != nil {
		return nil, err
	}

	out, err := p.pollUntilComplete(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if out.videoURL == "" {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "no video_url in completed task"}
	}

	if !p.downloadVideo {
		return []oasis.Attachment{{MimeType: "video/mp4", URL: out.videoURL}}, nil
	}
	att, derr := p.download(ctx, out.videoURL, "video/", "video/mp4")
	if derr != nil {
		return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "download video: " + derr.Error()}
	}
	return []oasis.Attachment{att}, nil
}

// videoInput builds the input payload for the active video model. All wan2.7
// video models share one media array typed by attachment Role; t2v carries an
// optional audio_url / negative_prompt instead.
func (p *Provider) videoInput(prompt string, atts []oasis.Attachment, v *oasis.VideoOptions) (map[string]any, error) {
	model := strings.ToLower(p.model)
	byRole := func(role string) *oasis.Attachment {
		for i := range atts {
			if atts[i].Role == role {
				return &atts[i]
			}
		}
		return nil
	}
	ref := func(a *oasis.Attachment) string { return attachmentRef(*a) }

	switch {
	case strings.Contains(model, "videoedit"):
		vid := byRole("video")
		if vid == nil {
			vid = firstAttachment(atts, "video/")
		}
		if vid == nil {
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "videoedit requires an input video attachment"}
		}
		media := []map[string]any{{"type": "video", "url": ref(vid)}}
		if img := byRole("reference_image"); img != nil {
			media = append(media, map[string]any{"type": "reference_image", "url": ref(img)})
		}
		return map[string]any{"prompt": prompt, "media": media}, nil
	case strings.Contains(model, "r2v"):
		// Reference-to-video: identity-preserving generation from reference
		// images/videos (+ optional first_frame). The media ORDER defines the
		// prompt's "Image n" / "Video n" identifiers, so preserve attachment order.
		var media []map[string]any
		for i := range atts {
			switch atts[i].Role {
			case "reference_image", "reference_video", "first_frame":
				m := map[string]any{"type": atts[i].Role, "url": ref(&atts[i])}
				if atts[i].ReferenceVoice != "" {
					m["reference_voice"] = atts[i].ReferenceVoice
				}
				media = append(media, m)
			}
		}
		if len(media) == 0 {
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "r2v requires at least one reference image or video"}
		}
		in := map[string]any{"prompt": prompt, "media": media}
		if v != nil && v.NegativePrompt != "" {
			in["negative_prompt"] = v.NegativePrompt
		}
		return in, nil
	case strings.Contains(model, "i2v"):
		// Native continuation: a prior clip seeds the next segment directly.
		if clip := byRole("first_clip"); clip != nil {
			return map[string]any{
				"prompt": prompt,
				"media":  []map[string]any{{"type": "first_clip", "url": ref(clip)}},
			}, nil
		}
		first := byRole("first_frame")
		if first == nil {
			first = firstAttachment(atts, "image/")
		}
		if first == nil {
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "i2v requires an input image attachment"}
		}
		media := []map[string]any{{"type": "first_frame", "url": ref(first)}}
		if last := byRole("last_frame"); last != nil {
			media = append(media, map[string]any{"type": "last_frame", "url": ref(last)})
		}
		if audio := byRole("driving_audio"); audio != nil {
			media = append(media, map[string]any{"type": "driving_audio", "url": ref(audio)})
		}
		return map[string]any{"prompt": prompt, "media": media}, nil
	default: // t2v — text, optional audio + negative prompt.
		in := map[string]any{"prompt": prompt}
		if audio := byRole("audio"); audio != nil {
			in["audio_url"] = ref(audio)
		}
		if v != nil && v.NegativePrompt != "" {
			in["negative_prompt"] = v.NegativePrompt
		}
		return in, nil
	}
}

// videoParameters maps VideoOptions to the request parameters block, omitting
// zero/nil fields so DashScope applies its defaults.
func videoParameters(v *oasis.VideoOptions) map[string]any {
	params := map[string]any{"prompt_extend": true}
	if v == nil {
		return params
	}
	if v.Duration > 0 {
		params["duration"] = v.Duration
	}
	if v.Resolution != "" {
		params["resolution"] = v.Resolution
	}
	if v.Ratio != "" {
		params["ratio"] = v.Ratio
	}
	if v.PromptExtend != nil {
		params["prompt_extend"] = *v.PromptExtend
	}
	if v.Watermark != nil {
		params["watermark"] = *v.Watermark
	}
	if v.Seed != nil {
		params["seed"] = *v.Seed
	}
	return params
}

// createAsyncTask POSTs an async-create request (X-DashScope-Async: enable) and
// returns the task_id. It is shared by the image-generation and video-synthesis
// async paths.
func (p *Provider) createAsyncTask(ctx context.Context, createURL string, payload []byte) (string, error) {
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, createURL, bytes.NewReader(payload))
	if err != nil {
		return "", &oasis.ErrLLM{Provider: p.Name(), Message: "create request: " + err.Error()}
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	createReq.Header.Set("X-DashScope-Async", "enable")

	resp, err := p.client.Do(createReq)
	if err != nil {
		return "", &oasis.ErrLLM{Provider: p.Name(), Message: "request failed: " + err.Error()}
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", p.httpErr(resp, body)
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
		return "", &oasis.ErrLLM{Provider: p.Name(), Message: "decode create response: " + err.Error()}
	}
	if created.Code != "" {
		return "", &oasis.ErrLLM{Provider: p.Name(), Message: created.Code + ": " + created.Message}
	}
	if created.Output.TaskID == "" {
		return "", &oasis.ErrLLM{Provider: p.Name(), Message: "no task_id returned"}
	}
	return created.Output.TaskID, nil
}

// taskOutput holds the parsed result of a completed async task. It carries both
// possible result shapes: image URLs (interleaved image generation) and a single
// video URL (video synthesis).
type taskOutput struct {
	imageURLs []string
	videoURL  string
}

// pollTask polls an async DashScope image task until it succeeds, then downloads
// the generated images. Its signature is preserved for the image path.
func (p *Provider) pollTask(ctx context.Context, taskID string) ([]oasis.Attachment, error) {
	out, err := p.pollUntilComplete(ctx, taskID)
	if err != nil {
		return nil, err
	}
	var attachments []oasis.Attachment
	for _, url := range out.imageURLs {
		att, derr := p.download(ctx, url, "image/", "image/png")
		if derr != nil {
			return nil, &oasis.ErrLLM{Provider: p.Name(), Message: "download image: " + derr.Error()}
		}
		attachments = append(attachments, att)
	}
	return attachments, nil
}

// pollUntilComplete polls an async DashScope task until it reaches a terminal
// state, parsing both result shapes (image choices and video_url). It uses the
// shared 3s interval and honours ctx cancellation.
func (p *Provider) pollUntilComplete(ctx context.Context, taskID string) (taskOutput, error) {
	taskURL := p.baseURL + "/tasks/" + taskID
	// Why: time.NewTimer + defer Stop avoids leaking a goroutine per iteration
	// (time.After leaks until the timer fires if the context is canceled first).
	t := time.NewTimer(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return taskOutput{}, ctx.Err()
		case <-t.C:
			t.Reset(3 * time.Second)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, taskURL, nil)
		if err != nil {
			return taskOutput{}, &oasis.ErrLLM{Provider: p.Name(), Message: "create poll request: " + err.Error()}
		}
		req.Header.Set("Authorization", "Bearer "+p.apiKey)

		resp, err := p.client.Do(req)
		if err != nil {
			return taskOutput{}, &oasis.ErrLLM{Provider: p.Name(), Message: "poll failed: " + err.Error()}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return taskOutput{}, p.httpErr(resp, body)
		}

		var task struct {
			Output struct {
				TaskStatus string `json:"task_status"`
				VideoURL   string `json:"video_url"`
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
			return taskOutput{}, &oasis.ErrLLM{Provider: p.Name(), Message: "decode poll response: " + err.Error()}
		}
		if task.Code != "" {
			return taskOutput{}, &oasis.ErrLLM{Provider: p.Name(), Message: task.Code + ": " + task.Message}
		}
		// Why: some failed async tasks report the error in output.code with an
		// empty or "UNKNOWN" task_status rather than at the top level.
		if task.Output.Code != "" {
			return taskOutput{}, &oasis.ErrLLM{Provider: p.Name(), Message: task.Output.Code + ": " + task.Output.Message}
		}

		switch task.Output.TaskStatus {
		case "SUCCEEDED":
			out := taskOutput{videoURL: task.Output.VideoURL}
			for _, choice := range task.Output.Choices {
				for _, part := range choice.Message.Content {
					if part.Image != "" {
						out.imageURLs = append(out.imageURLs, part.Image)
					}
				}
			}
			return out, nil
		case "FAILED", "CANCELED", "UNKNOWN":
			msg := task.Output.Message
			if msg == "" {
				msg = "task " + task.Output.TaskStatus
			}
			return taskOutput{}, &oasis.ErrLLM{Provider: p.Name(), Message: msg}
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
			att, derr := p.download(ctx, part.Image, "image/", "image/png")
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

// isVideoModel reports whether model is a Wan 2.7 video model (text-to-video,
// image-to-video, or video editing). Video model ids also start with "wan", so
// callers must check this before isWanModel.
func isVideoModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "t2v") || strings.Contains(m, "i2v") ||
		strings.Contains(m, "r2v") || strings.Contains(m, "videoedit")
}

// attachmentRef returns the reference DashScope should use for an attachment:
// its URL if set, otherwise a base64 data URI built from its inline bytes.
// Callers must ensure that either URL or Data is set before calling.
func attachmentRef(a oasis.Attachment) string {
	if a.URL != "" {
		return a.URL
	}
	return "data:" + a.MimeType + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
}

// firstAttachment returns the first attachment whose MimeType has mimePrefix, or
// nil if none match.
func firstAttachment(atts []oasis.Attachment, mimePrefix string) *oasis.Attachment {
	for i := range atts {
		if strings.HasPrefix(atts[i].MimeType, mimePrefix) {
			return &atts[i]
		}
	}
	return nil
}

// download fetches a generated media URL and returns it as an inline Attachment.
// The MIME type is the response Content-Type when it has wantPrefix, otherwise
// fallback (so a video is never mislabelled as an image, and vice versa).
func (p *Provider) download(ctx context.Context, url, wantPrefix, fallback string) (oasis.Attachment, error) {
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return oasis.Attachment{}, err
	}
	if len(data) > maxDownloadBytes {
		return oasis.Attachment{}, fmt.Errorf("downloaded media exceeds %d byte cap", maxDownloadBytes)
	}
	mime := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(mime, wantPrefix) {
		mime = fallback
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

// lastUserAttachments returns the attachments of the last user-role message, or
// nil if there is no such message (or it has none).
func lastUserAttachments(messages []oasis.ChatMessage) []oasis.Attachment {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == oasis.RoleUser {
			return messages[i].Attachments
		}
	}
	return nil
}

// maxDownloadBytes is the cap on downloaded media (images/video): 512 MiB.
const maxDownloadBytes = 512 << 20

// Compile-time interface check.
var _ oasis.Provider = (*Provider)(nil)
