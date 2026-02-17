package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-shiori/go-readability"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/ingest"
)

// Tool fetches URLs and extracts readable content.
type Tool struct {
	client *http.Client
}

// New creates an HTTPTool with a 15-second timeout.
func New() *Tool {
	return &Tool{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{{
		Name:        "http_fetch",
		Description: "Fetch a URL and extract its readable text content. Use for reading web pages, articles, documentation.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"URL to fetch"}},"required":["url"]}`),
	}}
}

func (t *Tool) Execute(ctx context.Context, _ string, args json.RawMessage) (oasis.ToolResult, error) {
	var params struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}

	content, err := t.Fetch(ctx, params.URL)
	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}

	if len(content) > 8000 {
		content = content[:8000] + "\n... (truncated)"
	}

	return oasis.ToolResult{Content: content}, nil
}

// Fetch downloads a URL and extracts readable text. Exported for use by other tools.
func (t *Tool) Fetch(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OasisBot/1.0)")

	resp, err := t.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return "", fmt.Errorf("read error: %w", err)
	}

	html := string(body)

	// Try readability extraction
	parsedURL, _ := url.Parse(rawURL)
	article, err := readability.FromReader(strings.NewReader(html), parsedURL)
	if err == nil && article.TextContent != "" {
		return strings.TrimSpace(article.TextContent), nil
	}

	// Fallback: simple HTML stripping
	return ingest.StripHTML(html), nil
}
