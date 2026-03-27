package ixd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type webSearchRequest struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type webSearchResponse struct {
	Query   string         `json:"query"`
	Results []searchResult `json:"results"`
}

// handleWebSearch performs a web search via Startpage and returns parsed results.
func (s *Server) handleWebSearch(w http.ResponseWriter, r *http.Request) {
	var req webSearchRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 10
	}

	results, err := startpageSearch(r.Context(), req.Query, req.MaxResults)
	if err != nil {
		writeError(w, http.StatusBadGateway, "search error: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, webSearchResponse{
		Query:   req.Query,
		Results: results,
	})
}

// startpageSearch queries Startpage and parses the HTML results.
func startpageSearch(ctx context.Context, query string, maxResults int) ([]searchResult, error) {
	searchURL := "https://www.startpage.com/sp/search?" + url.Values{
		"query": {query},
	}.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("User-Agent", browserUA)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := fetchClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("startpage request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("startpage HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB limit
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	results := parseStartpageResults(string(body), maxResults)
	if len(results) == 0 {
		return nil, fmt.Errorf("no results found (page may have changed structure)")
	}
	return results, nil
}

// Startpage HTML structure (verified 2026-03):
//
//	<a class="result-title result-link css-XXX" href="https://real-url.com" ...>
//	  <h2 class="wgl-title css-XXX">Title text</h2>
//	</a>
//	<p class="... description css-XXX ...">Snippet text</p>
var (
	// URL: <a class="result-title result-link ..." href="URL">
	resultURLRe = regexp.MustCompile(`class="result-title result-link[^"]*"\s+href="(https?://[^"]+)"`)
	// Title: <h2 class="wgl-title ...">Title</h2>
	resultTitleRe = regexp.MustCompile(`<h2\s+class="wgl-title[^"]*"[^>]*>(.*?)</h2>`)
	// Snippet: element with "description" in class (but not Startpage's own nav)
	resultSnippetRe = regexp.MustCompile(`(?s)<p\s+class="[^"]*description[^"]*"[^>]*>(.*?)</p>`)
)

func parseStartpageResults(html string, maxResults int) []searchResult {
	var results []searchResult

	urls := resultURLRe.FindAllStringSubmatch(html, -1)
	titles := resultTitleRe.FindAllStringSubmatch(html, -1)
	snippets := resultSnippetRe.FindAllStringSubmatch(html, -1)

	n := len(urls)
	if len(titles) < n {
		n = len(titles)
	}
	if maxResults < n {
		n = maxResults
	}

	// Skip snippets that are Startpage's own navigation (e.g., "Introducing the Startpage mobile app")
	var filteredSnippets []string
	for _, s := range snippets {
		text := stripHTMLBasic(s[1])
		text = strings.TrimSpace(decodeHTMLEntities(text))
		if text != "" && !strings.Contains(text, "Introducing the Startpage") {
			filteredSnippets = append(filteredSnippets, text)
		}
	}

	for i := 0; i < n; i++ {
		resultURL := urls[i][1]
		title := stripHTMLBasic(titles[i][1])
		title = strings.TrimSpace(decodeHTMLEntities(title))

		var snippet string
		if i < len(filteredSnippets) {
			snippet = filteredSnippets[i]
		}

		if resultURL == "" || title == "" {
			continue
		}

		results = append(results, searchResult{
			Title:   title,
			URL:     resultURL,
			Snippet: snippet,
		})
	}

	return results
}

// decodeHTMLEntities handles common HTML entities.
func decodeHTMLEntities(s string) string {
	r := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&apos;", "'",
		"&#x27;", "'",
		"&nbsp;", " ",
	)
	return r.Replace(s)
}
