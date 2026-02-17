package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/internal/ingest"
)

// Tool performs web searches via Brave API with semantic re-ranking.
type Tool struct {
	embedding   oasis.EmbeddingProvider
	braveAPIKey string
	httpClient  *http.Client
	chunkerCfg  ingest.ChunkerConfig
}

// New creates a SearchTool. Requires an embedding provider and Brave API key.
func New(embedding oasis.EmbeddingProvider, braveAPIKey string) *Tool {
	return &Tool{
		embedding:   embedding,
		braveAPIKey: braveAPIKey,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		chunkerCfg:  ingest.ChunkerConfig{MaxChars: 500, OverlapChars: 0},
	}
}

type braveResult struct {
	Title   string
	URL     string
	Snippet string
}

type rankedChunk struct {
	Text        string
	SourceIndex int
	SourceTitle string
	Score       float32
}

type resultWithContent struct {
	Result  braveResult
	Content string // extracted text, may be empty
}

func (t *Tool) Definitions() []oasis.ToolDefinition {
	return []oasis.ToolDefinition{{
		Name:        "web_search",
		Description: "Search the web for current/real-time information. Use for recent events, news, prices, weather, or anything that requires up-to-date data.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query optimized for search engines"}},"required":["query"]}`),
	}}
}

func (t *Tool) Execute(ctx context.Context, _ string, args json.RawMessage) (oasis.ToolResult, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}

	content, err := t.Search(ctx, params.Query)
	if err != nil {
		return oasis.ToolResult{Error: err.Error()}, nil
	}

	return oasis.ToolResult{Content: content}, nil
}

// Search performs a web search with semantic re-ranking.
func (t *Tool) Search(ctx context.Context, query string) (string, error) {
	const minGoodScore float32 = 0.35

	results, err := t.braveSearch(ctx, query, 8)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return fmt.Sprintf("No results found for %q.", query), nil
	}

	allResults := t.fetchAndExtract(ctx, results)
	ranked := t.rankResults(ctx, query, allResults)
	topScore := float32(0)
	if len(ranked) > 0 {
		topScore = ranked[0].Score
	}

	if topScore < minGoodScore {
		log.Printf(" [search] top score %.3f < %.3f, retrying with more results...", topScore, minGoodScore)
		more, err := t.braveSearch(ctx, query, 12)
		if err == nil {
			moreWithContent := t.fetchAndExtract(ctx, more)
			// Deduplicate by URL
			existing := make(map[string]bool)
			for _, r := range allResults {
				existing[r.Result.URL] = true
			}
			for _, r := range moreWithContent {
				if !existing[r.Result.URL] {
					allResults = append(allResults, r)
				}
			}
			ranked = t.rankResults(ctx, query, allResults)
		}
	}

	return formatRankedResults(ranked, allResults), nil
}

func (t *Tool) braveSearch(ctx context.Context, query string, count int) ([]braveResult, error) {
	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), count)

	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", t.braveAPIKey)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave search error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("brave API %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("brave parse error: %w", err)
	}

	var results []braveResult
	for _, r := range data.Web.Results {
		results = append(results, braveResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
		})
	}
	return results, nil
}

func (t *Tool) fetchAndExtract(ctx context.Context, results []braveResult) []resultWithContent {
	out := make([]resultWithContent, len(results))
	var wg sync.WaitGroup

	for i, r := range results {
		out[i] = resultWithContent{Result: r}
		wg.Add(1)
		go func(idx int, u string) {
			defer wg.Done()
			fetchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(fetchCtx, "GET", u, nil)
			if err != nil {
				return
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OasisBot/1.0)")

			resp, err := t.httpClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				return
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10)) // 512KB
			if err != nil {
				return
			}

			text := ingest.StripHTML(string(body))
			if len(text) > 8000 {
				text = text[:8000]
			}
			out[idx].Content = text
		}(i, r.URL)
	}
	wg.Wait()

	return out
}

func (t *Tool) rankResults(ctx context.Context, query string, results []resultWithContent) []rankedChunk {
	var tagged []rankedChunk

	for i, r := range results {
		if r.Result.Snippet != "" {
			tagged = append(tagged, rankedChunk{
				Text:        r.Result.Snippet,
				SourceIndex: i,
				SourceTitle: r.Result.Title,
			})
		}
		if r.Content != "" {
			chunks := ingest.ChunkText(r.Content, t.chunkerCfg)
			for _, c := range chunks {
				if len(c) < 50 {
					continue
				}
				tagged = append(tagged, rankedChunk{
					Text:        c,
					SourceIndex: i,
					SourceTitle: r.Result.Title,
				})
			}
		}
	}

	if len(tagged) == 0 {
		return tagged
	}

	log.Printf(" [search] chunked into %d pieces, embedding...", len(tagged))

	texts := make([]string, 0, 1+len(tagged))
	texts = append(texts, query)
	for _, c := range tagged {
		texts = append(texts, c.Text)
	}

	embeddings, err := t.embedding.Embed(ctx, texts)
	if err != nil {
		log.Printf(" [search] embedding failed: %v, falling back to unranked", err)
		if len(tagged) > 8 {
			tagged = tagged[:8]
		}
		return tagged
	}

	queryVec := embeddings[0]
	for i := range tagged {
		tagged[i].Score = cosineSimilarity(queryVec, embeddings[i+1])
	}

	sort.Slice(tagged, func(i, j int) bool {
		return tagged[i].Score > tagged[j].Score
	})

	log.Printf(" [search] top score: %.3f, bottom: %.3f",
		tagged[0].Score, tagged[len(tagged)-1].Score)

	return tagged
}

func formatRankedResults(ranked []rankedChunk, results []resultWithContent) string {
	var out strings.Builder
	seenSources := make(map[int]bool)

	limit := 8
	if len(ranked) < limit {
		limit = len(ranked)
	}

	for i := 0; i < limit; i++ {
		c := ranked[i]
		fmt.Fprintf(&out, "[%d] (score: %.2f) %s\n%s\n\n", i+1, c.Score, c.SourceTitle, c.Text)
		seenSources[c.SourceIndex] = true
	}

	out.WriteString("Sources:\n")
	for idx := range seenSources {
		if idx < len(results) {
			r := results[idx]
			fmt.Fprintf(&out, "- %s (%s)\n", r.Result.Title, r.Result.URL)
		}
	}

	return out.String()
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}
