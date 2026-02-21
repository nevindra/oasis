package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...ChunkFilter) ([]ScoredChunk, error)
}

// GraphStore is an optional Store capability for chunk relationship graphs.
// Store implementations that maintain a knowledge graph can implement this
// interface; callers discover it via type assertion.
type GraphStore interface {
	StoreEdges(ctx context.Context, edges []ChunkEdge) error
	GetEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
	GetIncomingEdges(ctx context.Context, chunkIDs []string) ([]ChunkEdge, error)
	PruneOrphanEdges(ctx context.Context) (int, error)
}

// RetrieverOption configures a HybridRetriever.
type RetrieverOption func(*retrieverConfig)

type retrieverConfig struct {
	reranker            Reranker
	minScore            float32
	keywordWeight       float32
	overfetchMultiplier int
	filters             []ChunkFilter
	tracer              Tracer
	logger              *slog.Logger
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

// WithFilters sets metadata filters passed to SearchChunks and SearchChunksKeyword.
func WithFilters(filters ...ChunkFilter) RetrieverOption {
	return func(c *retrieverConfig) { c.filters = filters }
}

// WithRetrieverTracer sets the Tracer for a HybridRetriever.
func WithRetrieverTracer(t Tracer) RetrieverOption {
	return func(c *retrieverConfig) { c.tracer = t }
}

// WithRetrieverLogger sets the structured logger for a HybridRetriever.
func WithRetrieverLogger(l *slog.Logger) RetrieverOption {
	return func(c *retrieverConfig) { c.logger = l }
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
	if h.cfg.tracer != nil {
		var span Span
		ctx, span = h.cfg.tracer.Start(ctx, "retriever.retrieve",
			StringAttr("retriever.type", "hybrid"),
			IntAttr("topK", topK))
		defer func() { span.End() }()

		results, err := h.retrieveInner(ctx, query, topK)
		if err != nil {
			span.Error(err)
		} else {
			span.SetAttr(IntAttr("result_count", len(results)))
		}
		return results, err
	}
	return h.retrieveInner(ctx, query, topK)
}

func (h *HybridRetriever) retrieveInner(ctx context.Context, query string, topK int) ([]RetrievalResult, error) {
	embs, err := h.embedding.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(embs) == 0 {
		return nil, fmt.Errorf("embed query: no embedding returned")
	}

	fetchK := max(topK*h.cfg.overfetchMultiplier, topK)

	vectorResults, err := h.store.SearchChunks(ctx, embs[0], fetchK, h.cfg.filters...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	var keywordResults []ScoredChunk
	if ks, ok := h.store.(KeywordSearcher); ok {
		keywordResults, _ = ks.SearchChunksKeyword(ctx, query, fetchK, h.cfg.filters...)
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

// --- GraphRetriever ---

// GraphRetrieverOption configures a GraphRetriever.
type GraphRetrieverOption func(*graphRetrieverConfig)

type graphRetrieverConfig struct {
	maxHops           int
	graphWeight       float32
	vectorWeight      float32
	hopDecay          []float32
	bidirectional     bool
	relationFilter    map[RelationType]bool
	minTraversalScore float32
	seedTopK          int
	filters           []ChunkFilter
	tracer            Tracer
	logger            *slog.Logger
}

// WithMaxHops sets the maximum number of graph traversal hops (default 2).
func WithMaxHops(n int) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.maxHops = n }
}

// WithGraphWeight sets the weight for graph-derived scores in the final blend (default 0.3).
func WithGraphWeight(w float32) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.graphWeight = w }
}

// WithVectorWeight sets the weight for vector scores in the final blend (default 0.7).
func WithVectorWeight(w float32) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.vectorWeight = w }
}

// WithHopDecay sets the score decay factor per hop level (default {1.0, 0.7, 0.5}).
// Length implicitly caps max hops.
func WithHopDecay(factors []float32) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.hopDecay = factors }
}

// WithBidirectional enables traversal of both outgoing and incoming edges (default false).
func WithBidirectional(b bool) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.bidirectional = b }
}

// WithRelationFilter restricts graph traversal to the specified relationship types.
func WithRelationFilter(types ...RelationType) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) {
		c.relationFilter = make(map[RelationType]bool, len(types))
		for _, t := range types {
			c.relationFilter[t] = true
		}
	}
}

// WithMinTraversalScore sets the minimum edge weight to follow during traversal (default 0).
func WithMinTraversalScore(s float32) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.minTraversalScore = s }
}

// WithSeedTopK sets the number of seed chunks from initial vector search (default 10).
func WithSeedTopK(k int) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.seedTopK = k }
}

// WithGraphFilters sets metadata filters passed to the initial vector search.
func WithGraphFilters(filters ...ChunkFilter) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.filters = filters }
}

// WithGraphRetrieverTracer sets the Tracer for a GraphRetriever.
func WithGraphRetrieverTracer(t Tracer) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.tracer = t }
}

// WithGraphRetrieverLogger sets the structured logger for a GraphRetriever.
func WithGraphRetrieverLogger(l *slog.Logger) GraphRetrieverOption {
	return func(c *graphRetrieverConfig) { c.logger = l }
}

// GraphRetriever combines vector search with knowledge graph traversal.
// It performs an initial vector search to find seed chunks, then traverses
// stored chunk edges to discover contextually related content.
// If the Store does not implement GraphStore, it falls back to vector-only retrieval.
type GraphRetriever struct {
	store     Store
	embedding EmbeddingProvider
	cfg       graphRetrieverConfig
}

var _ Retriever = (*GraphRetriever)(nil)

// NewGraphRetriever creates a Retriever that combines vector search with
// graph traversal for multi-hop contextual retrieval.
func NewGraphRetriever(store Store, embedding EmbeddingProvider, opts ...GraphRetrieverOption) *GraphRetriever {
	cfg := graphRetrieverConfig{
		maxHops:      2,
		graphWeight:  0.3,
		vectorWeight: 0.7,
		hopDecay:     []float32{1.0, 0.7, 0.5},
		seedTopK:     10,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return &GraphRetriever{store: store, embedding: embedding, cfg: cfg}
}

// Retrieve searches the knowledge base using vector search + graph traversal.
func (g *GraphRetriever) Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error) {
	if g.cfg.tracer != nil {
		var span Span
		ctx, span = g.cfg.tracer.Start(ctx, "retriever.retrieve",
			StringAttr("retriever.type", "graph"),
			IntAttr("topK", topK))
		defer func() { span.End() }()

		results, err := g.retrieveInner(ctx, query, topK)
		if err != nil {
			span.Error(err)
		} else {
			span.SetAttr(IntAttr("result_count", len(results)))
		}
		return results, err
	}
	return g.retrieveInner(ctx, query, topK)
}

func (g *GraphRetriever) retrieveInner(ctx context.Context, query string, topK int) ([]RetrievalResult, error) {
	embs, err := g.embedding.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(embs) == 0 {
		return nil, fmt.Errorf("embed query: no embedding returned")
	}

	// 1. Vector search for seed chunks.
	seeds, err := g.store.SearchChunks(ctx, embs[0], g.cfg.seedTopK, g.cfg.filters...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	// Track all discovered chunks with their best scores.
	type scored struct {
		chunkID string
		score   float32
	}
	best := make(map[string]*scored)

	for _, sc := range seeds {
		s := g.cfg.vectorWeight*sc.Score + g.cfg.graphWeight*1.0
		best[sc.ID] = &scored{chunkID: sc.ID, score: s}
	}

	// 2. Graph traversal (if store supports it).
	gs, ok := g.store.(GraphStore)
	if ok && len(seeds) > 0 {
		currentIDs := make([]string, len(seeds))
		for i, sc := range seeds {
			currentIDs[i] = sc.ID
		}

		visited := make(map[string]bool)
		for _, id := range currentIDs {
			visited[id] = true
		}

		for hop := 1; hop <= g.cfg.maxHops; hop++ {
			decay := float32(0.5)
			if hop < len(g.cfg.hopDecay) {
				decay = g.cfg.hopDecay[hop]
			}

			edges, err := gs.GetEdges(ctx, currentIDs)
			if err != nil {
				break // degrade gracefully
			}

			if g.cfg.bidirectional {
				incoming, err := gs.GetIncomingEdges(ctx, currentIDs)
				if err == nil {
					edges = append(edges, incoming...)
				}
			}

			var nextIDs []string
			for _, edge := range edges {
				if g.cfg.relationFilter != nil && !g.cfg.relationFilter[edge.Relation] {
					continue
				}
				if edge.Weight < g.cfg.minTraversalScore {
					continue
				}

				// Determine the neighbor (could be source or target depending on direction).
				neighborID := edge.TargetID
				if visited[neighborID] {
					neighborID = edge.SourceID
					if visited[neighborID] {
						continue
					}
				}
				visited[neighborID] = true

				graphScore := g.cfg.graphWeight * edge.Weight * decay
				if existing, ok := best[neighborID]; ok {
					if graphScore > existing.score {
						existing.score = graphScore
					}
				} else {
					best[neighborID] = &scored{chunkID: neighborID, score: graphScore}
				}
				nextIDs = append(nextIDs, neighborID)
			}

			currentIDs = nextIDs
			if len(currentIDs) == 0 {
				break
			}
		}
	}

	// 3. Fetch chunk content for graph-discovered chunks.
	var needFetch []string
	for id := range best {
		found := false
		for _, sc := range seeds {
			if sc.ID == id {
				found = true
				break
			}
		}
		if !found {
			needFetch = append(needFetch, id)
		}
	}

	chunkContent := make(map[string]Chunk)
	for _, sc := range seeds {
		chunkContent[sc.ID] = sc.Chunk
	}

	if len(needFetch) > 0 {
		fetched, err := g.store.GetChunksByIDs(ctx, needFetch)
		if err == nil {
			for _, c := range fetched {
				chunkContent[c.ID] = c
			}
		}
	}

	// 4. Build results.
	results := make([]RetrievalResult, 0, len(best))
	for id, s := range best {
		c, ok := chunkContent[id]
		if !ok {
			continue
		}
		results = append(results, RetrievalResult{
			Content:    c.Content,
			Score:      s.score,
			ChunkID:    c.ID,
			DocumentID: c.DocumentID,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}
