// Package gemini implements the Google Gemini LLM and embedding providers.
package gemini

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nevindra/oasis"
)

var baseURL = "https://generativelanguage.googleapis.com/v1beta"

// Gemini implements oasis.Provider for Google Gemini models.
type Gemini struct {
	apiKey     string
	model      string
	httpClient *http.Client

	temperature        float64
	topP               float64
	mediaResolution    string
	responseModalities []string
	thinkingEnabled    bool
	structuredOutput   bool
	codeExecution      bool
	functionCalling    bool
	googleSearch       bool
	urlContext         bool
}

// New creates a new Gemini chat provider with functional options.
func New(apiKey, model string, opts ...Option) *Gemini {
	g := &Gemini{
		apiKey:           apiKey,
		model:            model,
		httpClient:       &http.Client{},
		temperature:      0.1,
		topP:             0.9,
		structuredOutput: true,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Name returns "gemini".
func (g *Gemini) Name() string { return "gemini" }

// Chat sends a non-streaming chat request and returns the complete response.
func (g *Gemini) Chat(ctx context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
	body, err := g.buildBody(req.Messages, nil, req.ResponseSchema)
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("build body: " + err.Error())
	}
	return g.doGenerate(ctx, body)
}

// ChatWithTools sends a chat request with tool definitions.
func (g *Gemini) ChatWithTools(ctx context.Context, req oasis.ChatRequest, tools []oasis.ToolDefinition) (oasis.ChatResponse, error) {
	body, err := g.buildBody(req.Messages, tools, req.ResponseSchema)
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("build body: " + err.Error())
	}
	return g.doGenerate(ctx, body)
}

// ChatStream streams text-delta events into ch, then returns the final accumulated response.
// The channel is closed when streaming completes.
func (g *Gemini) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- oasis.StreamEvent) (oasis.ChatResponse, error) {
	defer close(ch)

	body, err := g.buildBody(req.Messages, nil, req.ResponseSchema)
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("build body: " + err.Error())
	}

	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", baseURL, g.model, g.apiKey)

	payload, err := json.Marshal(body)
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("marshal body: " + err.Error())
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("create request: " + err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("stream request failed: " + err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return oasis.ChatResponse{}, httpErr(resp, string(b))
	}

	var fullContent strings.Builder
	var usage oasis.Usage
	var attachments []oasis.Attachment

	scanner := bufio.NewScanner(resp.Body)
	// Large buffer for SSE payloads: image generation returns base64-encoded
	// image data as a single chunk, which can easily reach 5-10 MB.
	scanner.Buffer(make([]byte, 0, 16*1024*1024), 16*1024*1024)

	var jsonBuf strings.Builder

	for scanner.Scan() {
		line := scanner.Text()

		// SSE lines start with "data: ".
		if !strings.HasPrefix(line, "data: ") {
			// If we're accumulating a partial JSON, append the line.
			if jsonBuf.Len() > 0 {
				jsonBuf.WriteString(line)
				if isCompleteJSON(jsonBuf.String()) {
					g.processStreamChunk(jsonBuf.String(), &fullContent, &usage, &attachments, ch)
					jsonBuf.Reset()
				}
			}
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			continue
		}

		// Check if JSON is complete in a single line.
		if isCompleteJSON(data) {
			g.processStreamChunk(data, &fullContent, &usage, &attachments, ch)
		} else {
			jsonBuf.Reset()
			jsonBuf.WriteString(data)
		}
	}

	// Process any remaining buffered JSON.
	if jsonBuf.Len() > 0 && isCompleteJSON(jsonBuf.String()) {
		g.processStreamChunk(jsonBuf.String(), &fullContent, &usage, &attachments, ch)
	}

	return oasis.ChatResponse{
		Content:     fullContent.String(),
		Attachments: attachments,
		Usage:       usage,
	}, nil
}

// processStreamChunk parses a single JSON chunk from the SSE stream,
// extracts text deltas and usage, and sends text to the channel.
func (g *Gemini) processStreamChunk(jsonStr string, fullContent *strings.Builder, usage *oasis.Usage, attachments *[]oasis.Attachment, ch chan<- oasis.StreamEvent) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return
	}

	// Extract text from candidates[0].content.parts[].text
	text := extractTextFromParsed(parsed)
	if text != "" {
		fullContent.WriteString(text)
		ch <- oasis.StreamEvent{Type: oasis.EventTextDelta, Content: text}
	}

	// Extract attachments from inlineData parts.
	if atts := extractAttachmentsFromParsed(parsed); len(atts) > 0 {
		*attachments = append(*attachments, atts...)
	}

	// Extract usage metadata (overwrite each time; last chunk wins).
	extractUsageFromParsed(parsed, usage)
}

// doGenerate performs a non-streaming generateContent call and parses the response.
func (g *Gemini) doGenerate(ctx context.Context, body map[string]any) (oasis.ChatResponse, error) {
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", baseURL, g.model, g.apiKey)

	payload, err := json.Marshal(body)
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("marshal body: " + err.Error())
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("create request: " + err.Error())
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(httpReq)
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("request failed: " + err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return oasis.ChatResponse{}, g.wrapErr("failed to read response body: " + err.Error())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oasis.ChatResponse{}, httpErr(resp, string(respBody))
	}

	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return oasis.ChatResponse{}, g.wrapErr("failed to parse response JSON: " + err.Error())
	}

	var content strings.Builder
	var toolCalls []oasis.ToolCall
	var attachments []oasis.Attachment

	if len(parsed.Candidates) > 0 {
		for _, part := range parsed.Candidates[0].Content.Parts {
			// Skip thinking parts (thought: true) but preserve their thoughtSignature.
			if part.Thought {
				continue
			}
			if part.Text != nil {
				content.WriteString(*part.Text)
			}
			if part.FunctionCall != nil {
				tc := oasis.ToolCall{
					ID:   part.FunctionCall.Name,
					Name: part.FunctionCall.Name,
					Args: part.FunctionCall.Args,
				}
				// Preserve thoughtSignature for multi-turn thinking models.
				if part.ThoughtSignature != "" {
					meta, _ := json.Marshal(map[string]string{
						"thoughtSignature": part.ThoughtSignature,
					})
					tc.Metadata = meta
				}
				toolCalls = append(toolCalls, tc)
			}
			if part.InlineData != nil {
				raw, _ := base64.StdEncoding.DecodeString(part.InlineData.Data)
				attachments = append(attachments, oasis.Attachment{
					MimeType: part.InlineData.MimeType,
					Data:     raw,
				})
			}
		}
	}

	var usage oasis.Usage
	if parsed.UsageMetadata != nil {
		usage.InputTokens = parsed.UsageMetadata.PromptTokenCount
		usage.OutputTokens = parsed.UsageMetadata.CandidatesTokenCount
	}

	return oasis.ChatResponse{
		Content:     content.String(),
		Attachments: attachments,
		ToolCalls:   toolCalls,
		Usage:       usage,
	}, nil
}

func (g *Gemini) wrapErr(msg string) error {
	return &oasis.ErrLLM{Provider: "gemini", Message: msg}
}

// httpErr creates an ErrHTTP from an HTTP response, extracting the retry delay
// from the Retry-After header or from the Gemini-specific google.rpc.RetryInfo
// detail in the JSON error body.
func httpErr(resp *http.Response, body string) *oasis.ErrHTTP {
	ra := oasis.ParseRetryAfter(resp.Header.Get("Retry-After"))
	if ra == 0 {
		ra = parseRetryInfo(body)
	}
	return &oasis.ErrHTTP{
		Status:     resp.StatusCode,
		Body:       body,
		RetryAfter: ra,
	}
}

// parseRetryInfo extracts the retryDelay from a Gemini error body containing
// a google.rpc.RetryInfo detail. Returns 0 if not found or unparseable.
func parseRetryInfo(body string) time.Duration {
	var envelope struct {
		Error struct {
			Details []json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &envelope) != nil {
		return 0
	}
	for _, raw := range envelope.Error.Details {
		var detail struct {
			Type       string `json:"@type"`
			RetryDelay string `json:"retryDelay"`
		}
		if json.Unmarshal(raw, &detail) != nil {
			continue
		}
		if detail.Type == "type.googleapis.com/google.rpc.RetryInfo" && detail.RetryDelay != "" {
			if d, err := time.ParseDuration(detail.RetryDelay); err == nil {
				return d
			}
		}
	}
	return 0
}

// ---- Embedding provider ----

// GeminiEmbedding implements oasis.EmbeddingProvider for Gemini embedding models.
type GeminiEmbedding struct {
	apiKey     string
	model      string
	dims       int
	httpClient *http.Client
}

// NewEmbedding creates a new Gemini embedding provider.
func NewEmbedding(apiKey, model string, dims int) *GeminiEmbedding {
	return &GeminiEmbedding{
		apiKey:     apiKey,
		model:      model,
		dims:       dims,
		httpClient: &http.Client{},
	}
}

// Name returns "gemini".
func (e *GeminiEmbedding) Name() string { return "gemini" }

// Dimensions returns the configured embedding dimensionality.
func (e *GeminiEmbedding) Dimensions() int { return e.dims }

// Embed embeds each text sequentially and returns the embedding vectors.
func (e *GeminiEmbedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	url := fmt.Sprintf("%s/models/%s:embedContent?key=%s", baseURL, e.model, e.apiKey)

	embeddings := make([][]float32, 0, len(texts))

	for _, text := range texts {
		body := map[string]any{
			"content": map[string]any{
				"parts": []map[string]any{
					{"text": text},
				},
			},
			"outputDimensionality": e.dims,
		}

		payload, err := json.Marshal(body)
		if err != nil {
			return nil, &oasis.ErrLLM{Provider: "gemini", Message: "marshal embed body: " + err.Error()}
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
		if err != nil {
			return nil, &oasis.ErrLLM{Provider: "gemini", Message: "create embed request: " + err.Error()}
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := e.httpClient.Do(httpReq)
		if err != nil {
			return nil, &oasis.ErrLLM{Provider: "gemini", Message: "embed request failed: " + err.Error()}
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, &oasis.ErrLLM{Provider: "gemini", Message: "failed to read embed response: " + err.Error()}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, httpErr(resp, string(respBody))
		}

		var parsed embedResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			return nil, &oasis.ErrLLM{Provider: "gemini", Message: "failed to parse embed response: " + err.Error()}
		}

		if parsed.Embedding == nil {
			return nil, &oasis.ErrLLM{Provider: "gemini", Message: "missing embedding.values in response"}
		}

		vec := make([]float32, len(parsed.Embedding.Values))
		for i, v := range parsed.Embedding.Values {
			vec[i] = float32(v)
		}
		embeddings = append(embeddings, vec)
	}

	return embeddings, nil
}

// ---- Body builder ----

// buildBody constructs the Gemini API request body from chat messages and optional tool definitions.
func (g *Gemini) buildBody(messages []oasis.ChatMessage, tools []oasis.ToolDefinition, schema *oasis.ResponseSchema) (map[string]any, error) {
	var systemParts []string
	var contents []map[string]any

	for _, m := range messages {
		switch {
		case m.Role == "system":
			systemParts = append(systemParts, m.Content)

		case len(m.ToolCalls) > 0:
			// Assistant message with tool calls -> model role with functionCall parts.
			parts := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				// Parse args from json.RawMessage into a generic map so Gemini gets an object.
				var args any
				if len(tc.Args) > 0 {
					if err := json.Unmarshal(tc.Args, &args); err != nil {
						args = map[string]any{}
					}
				} else {
					args = map[string]any{}
				}

				part := map[string]any{
					"functionCall": map[string]any{
						"name": tc.Name,
						"args": args,
					},
				}

				// Preserve thoughtSignature from metadata.
				if len(tc.Metadata) > 0 {
					var meta map[string]any
					if err := json.Unmarshal(tc.Metadata, &meta); err == nil {
						if sig, ok := meta["thoughtSignature"]; ok {
							part["thoughtSignature"] = sig
						}
					}
				}

				parts = append(parts, part)
			}
			contents = append(contents, map[string]any{
				"role":  "model",
				"parts": parts,
			})

		case m.Role == "tool":
			// Tool result message -> user role with functionResponse part.
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []map[string]any{
					{
						"functionResponse": map[string]any{
							"name": m.ToolCallID,
							"response": map[string]any{
								"result": m.Content,
							},
						},
					},
				},
			})

		default:
			// Regular user or assistant message.
			var parts []map[string]any

			if m.Content != "" {
				parts = append(parts, map[string]any{"text": m.Content})
			}

			for _, att := range m.Attachments {
				if att.URL != "" {
					parts = append(parts, map[string]any{
						"fileData": map[string]any{
							"mimeType": att.MimeType,
							"fileUri":  att.URL,
						},
					})
				} else if data := att.InlineData(); len(data) > 0 {
					parts = append(parts, map[string]any{
						"inlineData": map[string]any{
							"mimeType": att.MimeType,
							"data":     base64.StdEncoding.EncodeToString(data),
						},
					})
				}
			}

			// Gemini requires at least one part.
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"text": ""})
			}

			entry := map[string]any{
				"role":  mapRole(m.Role),
				"parts": parts,
			}

			contents = append(contents, entry)
		}
	}

	body := map[string]any{
		"contents": contents,
	}

	// System instruction from accumulated system messages.
	if len(systemParts) > 0 {
		combined := strings.Join(systemParts, "\n\n")
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{
				{"text": combined},
			},
		}
	}

	// Tool entries: function declarations, code execution, grounding, URL context.
	var toolEntries []map[string]any

	if len(tools) > 0 {
		declarations := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			var params any
			if len(t.Parameters) > 0 {
				if err := json.Unmarshal(t.Parameters, &params); err != nil {
					params = map[string]any{}
				}
			} else {
				params = map[string]any{}
			}
			declarations = append(declarations, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  params,
			})
		}
		toolEntries = append(toolEntries, map[string]any{
			"functionDeclarations": declarations,
		})
	}

	if g.codeExecution {
		toolEntries = append(toolEntries, map[string]any{
			"codeExecution": map[string]any{},
		})
	}
	if g.googleSearch {
		toolEntries = append(toolEntries, map[string]any{
			"googleSearch": map[string]any{},
		})
	}
	if g.urlContext {
		toolEntries = append(toolEntries, map[string]any{
			"urlContext": map[string]any{},
		})
	}

	if len(toolEntries) > 0 {
		body["tools"] = toolEntries
	}

	// Disable function calling when no tools are explicitly provided.
	if !g.functionCalling && len(tools) == 0 {
		body["toolConfig"] = map[string]any{
			"functionCallingConfig": map[string]any{
				"mode": "NONE",
			},
		}
	}

	// Generation config.
	genConfig := map[string]any{
		"temperature": g.temperature,
		"topP":        g.topP,
	}

	if g.mediaResolution != "" {
		genConfig["mediaResolution"] = g.mediaResolution
	}

	if len(g.responseModalities) > 0 {
		genConfig["responseModalities"] = g.responseModalities
	}

	if g.thinkingEnabled {
		genConfig["thinkingConfig"] = map[string]any{
			"thinkingBudget": -1,
		}
	}

	// Structured output: enforce JSON response matching the schema.
	if g.structuredOutput && schema != nil && len(schema.Schema) > 0 {
		genConfig["responseMimeType"] = "application/json"
		var schemaObj any
		if err := json.Unmarshal(schema.Schema, &schemaObj); err == nil {
			genConfig["responseSchema"] = schemaObj
		}
	}

	body["generationConfig"] = genConfig

	return body, nil
}

// mapRole converts standard roles to Gemini API roles.
func mapRole(role string) string {
	if role == "assistant" {
		return "model"
	}
	return role
}

// ---- Response parsing types ----

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata *geminiUsage      `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
	Role  string       `json:"role"`
}

type geminiPart struct {
	Text             *string           `json:"text,omitempty"`
	FunctionCall     *geminiFuncCall   `json:"functionCall,omitempty"`
	InlineData       *geminiInlineData `json:"inlineData,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFuncCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
}

type embedResponse struct {
	Embedding *embedValues `json:"embedding"`
}

type embedValues struct {
	Values []float64 `json:"values"`
}

// ---- Stream helpers ----

// extractTextFromParsed extracts concatenated text from candidates[0].content.parts[].text
// in a raw parsed JSON map.
func extractTextFromParsed(parsed map[string]json.RawMessage) string {
	candidatesRaw, ok := parsed["candidates"]
	if !ok {
		return ""
	}

	var candidates []json.RawMessage
	if err := json.Unmarshal(candidatesRaw, &candidates); err != nil || len(candidates) == 0 {
		return ""
	}

	var candidate struct {
		Content struct {
			Parts []struct {
				Text    *string `json:"text,omitempty"`
				Thought bool    `json:"thought,omitempty"`
			} `json:"parts"`
		} `json:"content"`
	}
	if err := json.Unmarshal(candidates[0], &candidate); err != nil {
		return ""
	}

	var sb strings.Builder
	for _, p := range candidate.Content.Parts {
		if p.Thought {
			continue
		}
		if p.Text != nil {
			sb.WriteString(*p.Text)
		}
	}
	return sb.String()
}

// extractAttachmentsFromParsed extracts inlineData parts from candidates[0].content.parts[]
// in a raw parsed JSON map.
func extractAttachmentsFromParsed(parsed map[string]json.RawMessage) []oasis.Attachment {
	candidatesRaw, ok := parsed["candidates"]
	if !ok {
		return nil
	}

	var candidates []json.RawMessage
	if err := json.Unmarshal(candidatesRaw, &candidates); err != nil || len(candidates) == 0 {
		return nil
	}

	var candidate struct {
		Content struct {
			Parts []struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData,omitempty"`
			} `json:"parts"`
		} `json:"content"`
	}
	if err := json.Unmarshal(candidates[0], &candidate); err != nil {
		return nil
	}

	var attachments []oasis.Attachment
	for _, p := range candidate.Content.Parts {
		if p.InlineData != nil {
			raw, _ := base64.StdEncoding.DecodeString(p.InlineData.Data)
			attachments = append(attachments, oasis.Attachment{
				MimeType: p.InlineData.MimeType,
				Data:     raw,
			})
		}
	}
	return attachments
}

// extractUsageFromParsed extracts usage metadata from the parsed response.
func extractUsageFromParsed(parsed map[string]json.RawMessage, usage *oasis.Usage) {
	usageRaw, ok := parsed["usageMetadata"]
	if !ok {
		return
	}

	var u geminiUsage
	if err := json.Unmarshal(usageRaw, &u); err != nil {
		return
	}

	if u.PromptTokenCount > 0 || u.CandidatesTokenCount > 0 {
		usage.InputTokens = u.PromptTokenCount
		usage.OutputTokens = u.CandidatesTokenCount
	}
}

// isCompleteJSON checks whether a string has balanced braces/brackets,
// indicating it is a complete JSON value.
func isCompleteJSON(s string) bool {
	depth := 0
	inString := false
	escape := false

	for _, ch := range s {
		if escape {
			escape = false
			continue
		}
		if ch == '\\' && inString {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		}
	}
	return depth == 0 && !inString
}

// Compile-time interface assertions.
var (
	_ oasis.Provider          = (*Gemini)(nil)
	_ oasis.EmbeddingProvider = (*GeminiEmbedding)(nil)
)
