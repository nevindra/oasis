package ixd

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-shiori/go-readability"
)

type httpFetchRequest struct {
	URL      string `json:"url"`
	Raw      bool   `json:"raw"`
	MaxChars int    `json:"max_chars"`
}

type httpFetchResponse struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// fetchClient forces HTTP/1.1 to avoid HTTP/2 stream errors that many sites trigger.
var fetchClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSNextProto:      make(map[string]func(string, *tls.Conn) http.RoundTripper), // disable HTTP/2
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: false,
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).DialContext,
	},
}

const browserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func (s *Server) handleHTTPFetch(w http.ResponseWriter, r *http.Request) {
	var req httpFetchRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	if req.MaxChars <= 0 {
		req.MaxChars = 8000
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), "GET", req.URL, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid URL: "+err.Error())
		return
	}
	httpReq.Header.Set("User-Agent", browserUA)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := fetchClient.Do(httpReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "fetch error: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("HTTP %d from %s", resp.StatusCode, req.URL))
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		writeError(w, http.StatusBadGateway, "read error: "+err.Error())
		return
	}

	html := string(body)

	if req.Raw {
		if len(html) > req.MaxChars {
			html = html[:req.MaxChars]
		}
		writeJSON(w, http.StatusOK, httpFetchResponse{
			URL:     req.URL,
			Content: html,
		})
		return
	}

	// Extract readable content.
	parsedURL, _ := url.Parse(req.URL)
	var title, content string

	article, err := readability.FromReader(strings.NewReader(html), parsedURL)
	if err == nil && article.TextContent != "" {
		title = article.Title
		content = strings.TrimSpace(article.TextContent)
	} else {
		content = stripHTMLBasic(html)
	}

	if len(content) > req.MaxChars {
		content = content[:req.MaxChars]
	}

	writeJSON(w, http.StatusOK, httpFetchResponse{
		URL:     req.URL,
		Title:   title,
		Content: content,
	})
}

// stripHTMLBasic removes HTML tags as a fallback when readability fails.
func stripHTMLBasic(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
