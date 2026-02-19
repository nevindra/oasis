package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RetrievalResult is a scored piece of content from a knowledge base search.
// Score is in [0, 1]; higher means more relevant.
type RetrievalResult struct {
	Content        string  `json:"content"`
	Score          float32 `json:"score"`
	ChunkID        string  `json:"chunk_id"`
	DocumentID     string  `json:"document_id"`
	DocumentTitle  string  `json:"document_title"`
	DocumentSource string  `json:"document_source"`
}

// Retriever searches a knowledge base and returns ranked results.
// Implementations may combine multiple search strategies (vector, keyword,
// hybrid) and optionally re-rank before returning.
type Retriever interface {
	Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error)
}

// Reranker re-scores retrieval results for improved precision.
// Implementations may use cross-encoders, LLM-based scoring, or custom logic.
// The returned slice must be sorted by Score descending and trimmed to topK.
type Reranker interface {
	Rerank(ctx context.Context, query string, results []RetrievalResult, topK int) ([]RetrievalResult, error)
}

// KeywordSearcher is an optional Store capability for full-text keyword search.
// Store implementations that support FTS can implement this interface;
// callers discover it via type assertion.
type KeywordSearcher interface {
	SearchChunksKeyword(ctx context.Context, query string, topK int) ([]ScoredChunk, error)
}

// RetrieverOption configures a HybridRetriever.
type RetrieverOption func(*retrieverConfig)

type retrieverConfig struct {
	reranker            Reranker
	minScore            float32
	keywordWeight       float32
	overfetchMultiplier int
}

// WithReranker sets an optional re-ranking stage that runs after hybrid merge.
func WithReranker(r Reranker) RetrieverOption {
	return func(c *retrieverConfig) { c.reranker = r }
}

// WithMinRetrievalScore sets the minimum score threshold. Results below this
// score are dropped before returning. Default is 0 (no filtering).
func WithMinRetrievalScore(score float32) RetrieverOption {
	return func(c *retrieverConfig) { c.minScore = score }
}

// WithKeywordWeight sets the relative weight for keyword search results in
// the RRF merge. Must be in [0, 1]. Default is 0.3 (vector gets 0.7).
func WithKeywordWeight(w float32) RetrieverOption {
	return func(c *retrieverConfig) { c.keywordWeight = w }
}

// WithOverfetchMultiplier sets the multiplier for over-fetching candidates
// before re-ranking. Retrieve fetches topK * multiplier candidates, then
// re-ranks and trims to topK. Default is 3.
func WithOverfetchMultiplier(n int) RetrieverOption {
	return func(c *retrieverConfig) { c.overfetchMultiplier = n }
}

// --- ScoreReranker ---

// ScoreReranker filters results below a minimum score and re-sorts by score
// descending. It makes no external calls — useful as a baseline or when no
// API-based reranker is available.
type ScoreReranker struct {
	minScore float32
}

var _ Reranker = (*ScoreReranker)(nil)

// NewScoreReranker creates a ScoreReranker that drops results below minScore.
func NewScoreReranker(minScore float32) *ScoreReranker {
	return &ScoreReranker{minScore: minScore}
}

// Rerank filters results below the minimum score, sorts by score descending,
// and trims to topK.
func (r *ScoreReranker) Rerank(_ context.Context, _ string, results []RetrievalResult, topK int) ([]RetrievalResult, error) {
	var filtered []RetrievalResult
	for _, res := range results {
		if res.Score >= r.minScore {
			filtered = append(filtered, res)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Score > filtered[j].Score
	})
	if len(filtered) > topK {
		filtered = filtered[:topK]
	}
	return filtered, nil
}

// --- Reciprocal Rank Fusion ---

const rrfK = 60

// reciprocalRankFusion merges vector and keyword search results using
// Reciprocal Rank Fusion. keywordWeight is in [0,1]; vectorWeight = 1 - keywordWeight.
// Returns results sorted by fused score descending.
func reciprocalRankFusion(vector, keyword []ScoredChunk, keywordWeight float32) []RetrievalResult {
	vectorWeight := 1 - keywordWeight

	type entry struct {
		chunk Chunk
		score float32
	}
	merged := make(map[string]*entry)

	for rank, sc := range vector {
		e, ok := merged[sc.ID]
		if !ok {
			e = &entry{chunk: sc.Chunk}
			merged[sc.ID] = e
		}
		e.score += vectorWeight * (1.0 / float32(rrfK+rank+1))
	}
	for rank, sc := range keyword {
		e, ok := merged[sc.ID]
		if !ok {
			e = &entry{chunk: sc.Chunk}
			merged[sc.ID] = e
		}
		e.score += keywordWeight * (1.0 / float32(rrfK+rank+1))
	}

	results := make([]RetrievalResult, 0, len(merged))
	for _, e := range merged {
		results = append(results, RetrievalResult{
			Content:    e.chunk.Content,
			Score:      e.score,
			ChunkID:    e.chunk.ID,
			DocumentID: e.chunk.DocumentID,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	return results
}

// --- HybridRetriever ---

// HybridRetriever composes vector search, keyword search (FTS), parent-child
// resolution, and optional re-ranking into a single Retrieve call.
type HybridRetriever struct {
	store     Store
	embedding EmbeddingProvider
	cfg       retrieverConfig
}

var _ Retriever = (*HybridRetriever)(nil)

// NewHybridRetriever creates a Retriever that combines vector and keyword search
// using Reciprocal Rank Fusion, resolves parent-child chunks, and optionally
// re-ranks results. If the Store implements KeywordSearcher, keyword search is
// used automatically.
func NewHybridRetriever(store Store, embedding EmbeddingProvider, opts ...RetrieverOption) *HybridRetriever {
	cfg := retrieverConfig{
		keywordWeight:       0.3,
		overfetchMultiplier: 3,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return &HybridRetriever{store: store, embedding: embedding, cfg: cfg}
}

// Retrieve searches the knowledge base using hybrid vector + keyword search,
// resolves parent-child chunks, optionally re-ranks, and returns the top results.
func (h *HybridRetriever) Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error) {
	embs, err := h.embedding.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(embs) == 0 {
		return nil, fmt.Errorf("embed query: no embedding returned")
	}

	fetchK := max(topK*h.cfg.overfetchMultiplier, topK)

	vectorResults, err := h.store.SearchChunks(ctx, embs[0], fetchK)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	var keywordResults []ScoredChunk
	if ks, ok := h.store.(KeywordSearcher); ok {
		keywordResults, _ = ks.SearchChunksKeyword(ctx, query, fetchK)
	}

	var results []RetrievalResult
	if len(keywordResults) > 0 {
		results = reciprocalRankFusion(vectorResults, keywordResults, h.cfg.keywordWeight)
	} else {
		results = reciprocalRankFusion(vectorResults, nil, 0)
	}

	results, err = h.resolveParents(ctx, results)
	if err != nil {
		return nil, fmt.Errorf("resolve parents: %w", err)
	}

	if h.cfg.reranker != nil {
		results, err = h.cfg.reranker.Rerank(ctx, query, results, topK)
		if err != nil {
			return nil, fmt.Errorf("rerank: %w", err)
		}
	}

	if h.cfg.minScore > 0 {
		filtered := results[:0]
		for _, r := range results {
			if r.Score >= h.cfg.minScore {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	}

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

// resolveParents replaces child chunks with their parent's richer content.
// If multiple children map to the same parent, the highest-scored child wins.
// Errors are non-fatal — on failure, results pass through unmodified.
func (h *HybridRetriever) resolveParents(ctx context.Context, results []RetrievalResult) ([]RetrievalResult, error) {
	if len(results) == 0 {
		return results, nil
	}

	chunkIDs := make([]string, len(results))
	for i, r := range results {
		chunkIDs[i] = r.ChunkID
	}

	chunks, err := h.store.GetChunksByIDs(ctx, chunkIDs)
	if err != nil {
		return results, nil // degrade gracefully
	}

	chunkMap := make(map[string]Chunk, len(chunks))
	for _, c := range chunks {
		chunkMap[c.ID] = c
	}

	parentIDs := make(map[string]bool)
	var pIDs []string
	for _, c := range chunks {
		if c.ParentID != "" && !parentIDs[c.ParentID] {
			parentIDs[c.ParentID] = true
			pIDs = append(pIDs, c.ParentID)
		}
	}

	if len(pIDs) == 0 {
		return results, nil
	}

	parents, err := h.store.GetChunksByIDs(ctx, pIDs)
	if err != nil {
		return results, nil // degrade gracefully
	}

	parentMap := make(map[string]Chunk, len(parents))
	for _, p := range parents {
		parentMap[p.ID] = p
	}

	seen := make(map[string]bool)
	var resolved []RetrievalResult

	for _, r := range results {
		c, ok := chunkMap[r.ChunkID]
		if !ok || c.ParentID == "" {
			resolved = append(resolved, r)
			continue
		}

		if seen[c.ParentID] {
			continue
		}
		seen[c.ParentID] = true

		parent, ok := parentMap[c.ParentID]
		if !ok {
			resolved = append(resolved, r)
			continue
		}

		resolved = append(resolved, RetrievalResult{
			Content:    parent.Content,
			Score:      r.Score,
			ChunkID:    parent.ID,
			DocumentID: parent.DocumentID,
		})
	}

	return resolved, nil
}

// --- LLMReranker ---

// LLMReranker uses an LLM to score query-document relevance.
// It sends a prompt asking the model to rate each result 0-10,
// then normalizes and re-sorts. On LLM failure, results pass through
// unmodified (graceful degradation).
type LLMReranker struct {
	provider Provider
}

var _ Reranker = (*LLMReranker)(nil)

// NewLLMReranker creates a Reranker that uses the given LLM provider to
// score relevance.
func NewLLMReranker(provider Provider) *LLMReranker {
	return &LLMReranker{provider: provider}
}

// Rerank sends results to the LLM for relevance scoring, then re-sorts.
func (r *LLMReranker) Rerank(ctx context.Context, query string, results []RetrievalResult, topK int) ([]RetrievalResult, error) {
	if len(results) == 0 {
		return results, nil
	}

	var docs strings.Builder
	for i, res := range results {
		fmt.Fprintf(&docs, "Document %d:\n%s\n\n", i, res.Content)
	}

	prompt := fmt.Sprintf(
		"Rate the relevance of each document to the query on a scale of 0-10.\n\nQuery: %s\n\n%sRespond with JSON only: {\"scores\":[{\"index\":0,\"score\":N}, ...]}",
		query, docs.String(),
	)

	resp, err := r.provider.Chat(ctx, ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return results, nil // degrade gracefully
	}

	var parsed struct {
		Scores []struct {
			Index int     `json:"index"`
			Score float64 `json:"score"`
		} `json:"scores"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &parsed); err != nil {
		return results, nil // degrade gracefully
	}

	for _, s := range parsed.Scores {
		if s.Index >= 0 && s.Index < len(results) {
			results[s.Index].Score = float32(s.Score / 10.0)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}
