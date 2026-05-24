package http

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-shiori/go-readability"

	oasis "github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/ingest"
)

// defaultFetchMaxChars is the default character cap applied to extracted
// readable content. Callers may override via FetchInput.MaxChars.
const defaultFetchMaxChars = 8000

// FetchInput is the input payload for the http_fetch tool.
type FetchInput struct {
	URL      string `json:"url" describe:"URL to fetch"`
	MaxChars int    `json:"max_chars,omitempty" describe:"Max characters to return (default 8000)"`
}

// Tool fetches URLs and extracts readable content. It implements
// oasis.Tool[FetchInput, string] — one tool = one operation. The output is
// kept as a bare string for ergonomic LLM consumption: the model just sees
// the extracted text, and Erase wraps it as JSON automatically.
type Tool struct {
	client *http.Client
}

// New creates an HTTPTool with a 15-second timeout.
func New() *Tool {
	return &Tool{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Definition implements oasis.Tool.
func (t *Tool) Definition() oasis.ToolMeta {
	return oasis.ToolMeta{
		Name:        "http_fetch",
		Description: "Fetch a URL and extract its readable text content. Use for reading web pages, articles, documentation.",
	}
}

// Execute implements oasis.Tool. Returns the extracted, possibly-truncated
// readable text for the given URL. Truncation happens at a UTF-8 rune
// boundary so the returned string is always valid UTF-8.
func (t *Tool) Execute(ctx context.Context, in FetchInput) (string, error) {
	content, err := t.Fetch(ctx, in.URL)
	if err != nil {
		return "", err
	}
	limit := in.MaxChars
	if limit <= 0 {
		limit = defaultFetchMaxChars
	}
	// Walk forward counting runes; stop at the byte offset that holds `limit`
	// runes. Allocation-free: no []rune slice, no scratch buffer.
	byteOff, runes := 0, 0
	for byteOff < len(content) && runes < limit {
		_, size := utf8.DecodeRuneInString(content[byteOff:])
		byteOff += size
		runes++
	}
	if byteOff < len(content) {
		content = content[:byteOff] + "\n... (truncated)"
	}
	return content, nil
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

// compile-time check
var _ oasis.Tool[FetchInput, string] = (*Tool)(nil)
