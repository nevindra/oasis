package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nevindra/oasis"
)

// CachedContent represents a Gemini cached content resource.
// Create one with Gemini.CreateCachedContent, then reference it in requests
// via WithCachedContent(cc.Name).
type CachedContent struct {
	// Name is the resource identifier (e.g. "cachedContents/abc123").
	// Set by the server on creation.
	Name string `json:"name,omitempty"`

	// Model is the model this cache is bound to (e.g. "models/gemini-2.5-flash").
	// A cache can only be used with the model it was created for.
	Model string `json:"model"`

	// DisplayName is an optional human-readable label (max 128 chars).
	DisplayName string `json:"displayName,omitempty"`

	// Contents is the conversation content to cache. Input only, immutable after creation.
	Contents []CachedContentPart `json:"contents,omitempty"`

	// SystemInstruction is the system prompt to cache. Input only, immutable after creation.
	SystemInstruction *CachedContentPart `json:"systemInstruction,omitempty"`

	// TTL is the time-to-live duration (e.g. "3600s"). Input only — converted to
	// ExpireTime in the response. If neither TTL nor ExpireTime is set, defaults
	// to 1 hour.
	TTL string `json:"ttl,omitempty"`

	// ExpireTime is the absolute expiration time (RFC 3339). Output only on
	// creation; can be set on update.
	ExpireTime string `json:"expireTime,omitempty"`

	// UsageMetadata contains the total cached token count. Output only.
	UsageMetadata *CacheUsageMetadata `json:"usageMetadata,omitempty"`

	// CreateTime is when the cache was created. Output only.
	CreateTime string `json:"createTime,omitempty"`

	// UpdateTime is when the cache was last updated. Output only.
	UpdateTime string `json:"updateTime,omitempty"`
}

// CachedContentPart represents content to cache (text, inline data, or file references).
type CachedContentPart struct {
	Role  string          `json:"role,omitempty"`
	Parts []map[string]any `json:"parts"`
}

// CacheUsageMetadata contains token count information for cached content.
type CacheUsageMetadata struct {
	TotalTokenCount int `json:"totalTokenCount"`
}

// CacheListResponse is the response from listing cached contents.
type CacheListResponse struct {
	CachedContents []CachedContent `json:"cachedContents"`
	NextPageToken  string          `json:"nextPageToken,omitempty"`
}

// --- Convenience constructors ---

// NewTextCachedContent creates a CachedContent with a system instruction to cache.
// The model should include the "models/" prefix (e.g. "models/gemini-2.5-flash").
// TTL is the cache lifetime (minimum 1 minute, default 1 hour if zero).
func NewTextCachedContent(model, systemInstruction string, ttl time.Duration) CachedContent {
	cc := CachedContent{
		Model: model,
		SystemInstruction: &CachedContentPart{
			Parts: []map[string]any{
				{"text": systemInstruction},
			},
		},
	}
	if ttl > 0 {
		cc.TTL = fmt.Sprintf("%ds", int(ttl.Seconds()))
	}
	return cc
}

// --- Cache CRUD methods ---

// CreateCachedContent creates a new cached content resource.
// The cache is immutable after creation — only the expiration can be updated.
// Returns the created resource with Name populated.
func (g *Gemini) CreateCachedContent(ctx context.Context, cc CachedContent) (CachedContent, error) {
	url := fmt.Sprintf("%s/cachedContents?key=%s", baseURL, g.apiKey)
	return cacheRequest[CachedContent](ctx, g.httpClient, http.MethodPost, url, &cc)
}

// GetCachedContent retrieves a cached content resource by name.
// Name should be the full resource name (e.g. "cachedContents/abc123").
func (g *Gemini) GetCachedContent(ctx context.Context, name string) (CachedContent, error) {
	url := fmt.Sprintf("%s/%s?key=%s", baseURL, name, g.apiKey)
	return cacheRequest[CachedContent](ctx, g.httpClient, http.MethodGet, url, nil)
}

// ListCachedContents lists all cached content resources.
func (g *Gemini) ListCachedContents(ctx context.Context) ([]CachedContent, error) {
	url := fmt.Sprintf("%s/cachedContents?key=%s", baseURL, g.apiKey)
	resp, err := cacheRequest[CacheListResponse](ctx, g.httpClient, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return resp.CachedContents, nil
}

// UpdateCachedContent updates the expiration of a cached content resource.
// Only TTL or ExpireTime can be updated — content is immutable.
// Name must be set on the input.
func (g *Gemini) UpdateCachedContent(ctx context.Context, cc CachedContent) (CachedContent, error) {
	// Determine which expiration field to update.
	var updateMask string
	if cc.TTL != "" {
		updateMask = "ttl"
	} else if cc.ExpireTime != "" {
		updateMask = "expireTime"
	}
	url := fmt.Sprintf("%s/%s?key=%s", baseURL, cc.Name, g.apiKey)
	if updateMask != "" {
		url += "&updateMask=" + updateMask
	}
	return cacheRequest[CachedContent](ctx, g.httpClient, http.MethodPatch, url, &cc)
}

// DeleteCachedContent deletes a cached content resource by name.
func (g *Gemini) DeleteCachedContent(ctx context.Context, name string) error {
	url := fmt.Sprintf("%s/%s?key=%s", baseURL, name, g.apiKey)
	_, err := cacheRequest[json.RawMessage](ctx, g.httpClient, http.MethodDelete, url, nil)
	return err
}

// cacheRequest is a generic helper for cache API requests.
func cacheRequest[T any](ctx context.Context, client *http.Client, method, url string, body any) (T, error) {
	var zero T
	var reqBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return zero, &oasis.ErrLLM{Provider: "gemini", Message: "marshal cache request: " + err.Error()}
		}
		reqBody = bytes.NewReader(payload)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return zero, &oasis.ErrLLM{Provider: "gemini", Message: "create cache request: " + err.Error()}
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return zero, &oasis.ErrLLM{Provider: "gemini", Message: "cache request failed: " + err.Error()}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, &oasis.ErrLLM{Provider: "gemini", Message: "read cache response: " + err.Error()}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, httpErr(resp, string(respBody))
	}

	// DELETE returns empty body.
	if len(respBody) == 0 {
		return zero, nil
	}

	var result T
	if err := json.Unmarshal(respBody, &result); err != nil {
		return zero, &oasis.ErrLLM{Provider: "gemini", Message: "parse cache response: " + err.Error()}
	}
	return result, nil
}
