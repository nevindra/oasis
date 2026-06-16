package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
)

// RetrievalResult is a scored piece of content from a knowledge base search.
// Score is in [0, 1]; higher means more relevant.
type RetrievalResult struct {
	Content        string        `json:"content"`
	Score          float32       `json:"score"`
	ChunkID        string        `json:"chunk_id"`
	ParentID       string        `json:"parent_id,omitempty"`
	DocumentID     string        `json:"document_id"`
	DocumentTitle  string        `json:"document_title"`
	DocumentSource string        `json:"document_source"`
	GraphContext   []EdgeContext `json:"graph_context,omitempty"`
}

// EdgeContext describes a graph edge that led to a chunk's discovery.
// Populated by GraphRetriever for graph-discovered (non-seed) chunks.
type EdgeContext struct {
	FromChunkID string            `json:"from_chunk_id"`
	Relation    core.RelationType `json:"relation"`
	Description string            `json:"description"`
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

// RetrieverOption configures a HybridRetriever.
type RetrieverOption func(*retrieverConfig)

type retrieverConfig struct {
	reranker            Reranker
	minScore            float32
	keywordWeight       float32
	overfetchMultiplier int
	filters             []core.ChunkFilter
	tracer              core.Tracer
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
func WithFilters(filters ...core.ChunkFilter) RetrieverOption {
	return func(c *retrieverConfig) { c.filters = filters }
}

// WithRetrieverTracer sets the core.Tracer for a HybridRetriever.
func WithRetrieverTracer(t core.Tracer) RetrieverOption {
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
// Scores are normalized to [0, 1] so that WithMinRetrievalScore and ScoreReranker
// thresholds work correctly.
// Returns results sorted by fused score descending.
func reciprocalRankFusion(vector, keyword []core.ScoredChunk, keywordWeight float32) []RetrievalResult {
	vectorWeight := 1 - keywordWeight

	// The maximum possible RRF contribution from a single list is 1/(rrfK+1)
	// (rank 0). Since weights sum to 1, the maximum fused score is 1/(rrfK+1).
	// Normalize by this factor so scores are in [0, 1].
	normalizer := float32(rrfK + 1)

	type entry struct {
		chunk core.Chunk
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
			Score:      e.score * normalizer,
			ChunkID:    e.chunk.ID,
			ParentID:   e.chunk.ParentID,
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
	store     core.Store
	embedding core.EmbeddingProvider
	cfg       retrieverConfig

	// mu guards lastSources.
	mu          sync.RWMutex
	lastSources []core.Source
}

var _ Retriever = (*HybridRetriever)(nil)

// Compile-time assertion: HybridRetriever implements core.Sourced.
var _ core.Sourced = (*HybridRetriever)(nil)

// NewHybridRetriever creates a Retriever that combines vector and keyword search
// using Reciprocal Rank Fusion, resolves parent-child chunks, and optionally
// re-ranks results. If the core.Store implements core.KeywordSearcher, keyword search is
// used automatically.
func NewHybridRetriever(store core.Store, embedding core.EmbeddingProvider, opts ...RetrieverOption) *HybridRetriever {
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
// On success the retrieved chunks are stored as []core.Source accessible via Sources().
func (h *HybridRetriever) Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error) {
	var (
		results []RetrievalResult
		err     error
	)
	if h.cfg.tracer != nil {
		var span core.Span
		ctx, span = h.cfg.tracer.Start(ctx, "retriever.retrieve",
			core.StringAttr("retriever.type", "hybrid"),
			core.IntAttr("topK", topK))
		defer func() { span.End() }()

		results, err = h.retrieveInner(ctx, query, topK)
		if err != nil {
			span.Error(err)
		} else {
			span.SetAttr(core.IntAttr("result_count", len(results)))
		}
	} else {
		results, err = h.retrieveInner(ctx, query, topK)
	}
	if err == nil {
		h.mu.Lock()
		h.lastSources = resultsToSources(results)
		h.mu.Unlock()
	}
	return results, err
}

// Sources returns the cited chunks from the most recent Retrieve call as
// []core.Source, satisfying the core.Sourced interface. Returns nil when
// no successful Retrieve call has been made.
func (h *HybridRetriever) Sources() []core.Source {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastSources
}

func (h *HybridRetriever) retrieveInner(ctx context.Context, query string, topK int) ([]RetrievalResult, error) {
	embs, err := h.embedding.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(embs) == 0 {
		return nil, fmt.Errorf("embed query: no embedding returned")
	}
	return h.retrieveWithEmbedding(ctx, embs[0], query, topK)
}

// RetrieveWithEmbedding is like Retrieve but accepts a pre-computed query
// embedding, avoiding a redundant Embed call. Useful when the caller has
// already embedded the query for other purposes (e.g., message search).
func (h *HybridRetriever) RetrieveWithEmbedding(ctx context.Context, queryEmbedding []float32, query string, topK int) ([]RetrievalResult, error) {
	if h.cfg.tracer != nil {
		var span core.Span
		ctx, span = h.cfg.tracer.Start(ctx, "retriever.retrieve",
			core.StringAttr("retriever.type", "hybrid"),
			core.IntAttr("topK", topK))
		defer func() { span.End() }()

		results, err := h.retrieveWithEmbedding(ctx, queryEmbedding, query, topK)
		if err != nil {
			span.Error(err)
		} else {
			span.SetAttr(core.IntAttr("result_count", len(results)))
		}
		return results, err
	}
	return h.retrieveWithEmbedding(ctx, queryEmbedding, query, topK)
}

func (h *HybridRetriever) retrieveWithEmbedding(ctx context.Context, queryEmbedding []float32, query string, topK int) ([]RetrievalResult, error) {
	fetchK := max(topK*h.cfg.overfetchMultiplier, topK)

	var (
		vectorResults  []core.ScoredChunk
		keywordResults []core.ScoredChunk
		vectorErr      error
	)
	ks, hasKeyword := h.store.(core.KeywordSearcher)

	if hasKeyword {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			vectorResults, vectorErr = h.store.SearchChunks(ctx, queryEmbedding, fetchK, h.cfg.filters...)
		}()
		go func() {
			defer wg.Done()
			var kwErr error
			keywordResults, kwErr = ks.SearchChunksKeyword(ctx, query, fetchK, h.cfg.filters...)
			if kwErr != nil && h.cfg.logger != nil {
				h.cfg.logger.Warn("keyword search failed, falling back to vector-only", "err", kwErr)
			}
		}()
		wg.Wait()
	} else {
		vectorResults, vectorErr = h.store.SearchChunks(ctx, queryEmbedding, fetchK, h.cfg.filters...)
	}
	if vectorErr != nil {
		return nil, fmt.Errorf("vector search: %w", vectorErr)
	}

	var results []RetrievalResult
	if len(keywordResults) > 0 {
		results = reciprocalRankFusion(vectorResults, keywordResults, h.cfg.keywordWeight)
	} else {
		results = reciprocalRankFusion(vectorResults, nil, 0)
	}

	results = resolveParentChunks(ctx, h.store, results)
	populateDocumentMeta(ctx, h.store, results)

	if h.cfg.reranker != nil {
		var err error
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

// --- Shared retrieval helpers ---

// resolveParentChunks replaces child chunks with their parent's richer content.
// If multiple children map to the same parent, the highest-scored child wins.
// Uses ParentID already present on RetrievalResult (populated by SearchChunks),
// avoiding an extra GetChunksByIDs round-trip.
// Errors are non-fatal — on failure, results pass through unmodified.
func resolveParentChunks(ctx context.Context, store core.Store, results []RetrievalResult) []RetrievalResult {
	if len(results) == 0 {
		return results
	}

	// Collect unique parent IDs directly from results.
	parentSet := make(map[string]bool)
	var pIDs []string
	for _, r := range results {
		if r.ParentID != "" && !parentSet[r.ParentID] {
			parentSet[r.ParentID] = true
			pIDs = append(pIDs, r.ParentID)
		}
	}

	if len(pIDs) == 0 {
		return results
	}

	parents, err := store.GetChunksByIDs(ctx, pIDs)
	if err != nil {
		return results // degrade gracefully
	}

	parentMap := make(map[string]core.Chunk, len(parents))
	for _, p := range parents {
		parentMap[p.ID] = p
	}

	// Sort by score descending so the highest-scored child wins when
	// multiple children share the same parent.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	seen := make(map[string]bool)
	var resolved []RetrievalResult

	for _, r := range results {
		if r.ParentID == "" {
			resolved = append(resolved, r)
			continue
		}

		if seen[r.ParentID] {
			continue
		}
		seen[r.ParentID] = true

		parent, ok := parentMap[r.ParentID]
		if !ok {
			resolved = append(resolved, r)
			continue
		}

		resolved = append(resolved, RetrievalResult{
			Content:      parent.Content,
			Score:        r.Score,
			ChunkID:      parent.ID,
			ParentID:     parent.ParentID,
			DocumentID:   parent.DocumentID,
			GraphContext: r.GraphContext,
		})
	}

	return resolved
}

// populateDocumentMeta fills DocumentTitle and DocumentSource on results
// by batch-fetching document metadata. If the core.Store does not implement
// core.DocumentGetter, fields stay empty (same behavior as before).
func populateDocumentMeta(ctx context.Context, store core.Store, results []RetrievalResult) {
	dg, ok := store.(core.DocumentGetter)
	if !ok || len(results) == 0 {
		return
	}

	idSet := make(map[string]bool)
	var ids []string
	for _, r := range results {
		if r.DocumentID != "" && !idSet[r.DocumentID] {
			idSet[r.DocumentID] = true
			ids = append(ids, r.DocumentID)
		}
	}
	if len(ids) == 0 {
		return
	}

	docs, err := dg.GetDocumentsByIDs(ctx, ids)
	if err != nil {
		return // degrade gracefully
	}

	docMap := make(map[string]core.Document, len(docs))
	for _, d := range docs {
		docMap[d.ID] = d
	}

	for i := range results {
		if d, ok := docMap[results[i].DocumentID]; ok {
			results[i].DocumentTitle = d.Title
			results[i].DocumentSource = d.Source
		}
	}
}

// extractJSON extracts a JSON object from text that may be wrapped in markdown
// code fences (```json ... ```). Returns the original string if no fences found.
func extractJSON(s string) string {
	// Try to find content between code fences.
	if start := strings.Index(s, "```"); start >= 0 {
		// Skip the opening fence line.
		inner := s[start+3:]
		if nl := strings.IndexByte(inner, '\n'); nl >= 0 {
			inner = inner[nl+1:]
		}
		if end := strings.Index(inner, "```"); end >= 0 {
			return strings.TrimSpace(inner[:end])
		}
	}
	// Try to find a bare JSON object.
	if start := strings.IndexByte(s, '{'); start >= 0 {
		if end := strings.LastIndexByte(s, '}'); end > start {
			return s[start : end+1]
		}
	}
	return s
}

// resultsToSources converts retrieval results to []core.Source for the
// core.Sourced interface. Each result maps to one Source: DocumentSource →
// URL, DocumentTitle → Title, Content → Quote (first 500 chars), Origin="rag",
// and Meta carrying chunk_id and score.
func resultsToSources(results []RetrievalResult) []core.Source {
	if len(results) == 0 {
		return nil
	}
	srcs := make([]core.Source, 0, len(results))
	for _, r := range results {
		quote := r.Content
		if len(quote) > 500 {
			quote = quote[:500]
		}
		meta, _ := json.Marshal(map[string]any{
			"chunk_id": r.ChunkID,
			"score":    r.Score,
		})
		srcs = append(srcs, core.Source{
			URL:    r.DocumentSource,
			Title:  r.DocumentTitle,
			Quote:  quote,
			Origin: "rag",
			Meta:   meta,
		})
	}
	return srcs
}

// --- LLMReranker ---

// LLMReranker uses an LLM to score query-document relevance.
// It sends a prompt asking the model to rate each result 0-10,
// then normalizes and re-sorts. On LLM failure, results pass through
// unmodified (graceful degradation).
type LLMReranker struct {
	provider core.Provider
	timeout  time.Duration
}

var _ Reranker = (*LLMReranker)(nil)

// NewLLMReranker creates a Reranker that uses the given LLM provider to
// score relevance. Default timeout is 2 minutes per LLM call.
func NewLLMReranker(provider core.Provider) *LLMReranker {
	return &LLMReranker{provider: provider, timeout: 2 * time.Minute}
}

// WithRerankerTimeout sets the maximum duration for the LLM reranking call.
func WithRerankerTimeout(d time.Duration) func(*LLMReranker) {
	return func(r *LLMReranker) { r.timeout = d }
}

// Rerank sends results to the LLM for relevance scoring, then re-sorts.
func (r *LLMReranker) Rerank(ctx context.Context, query string, results []RetrievalResult, topK int) ([]RetrievalResult, error) {
	if len(results) == 0 {
		return results, nil
	}

	var docs strings.Builder
	for i, res := range results {
		fmt.Fprintf(&docs, "core.Document %d:\n%s\n\n", i, res.Content)
	}

	prompt := fmt.Sprintf(
		"Rate the relevance of each document to the query on a scale of 0-10.\n\nQuery: %s\n\n%sRespond with JSON only: {\"scores\":[{\"index\":0,\"score\":N}, ...]}",
		query, docs.String(),
	)

	callCtx := ctx
	if r.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	resp, err := core.Chat(callCtx, r.provider, core.ChatRequest{
		Messages: []core.ChatMessage{
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
	raw := extractJSON(resp.Content)
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
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

// GraphRetrieverConfig configures a GraphRetriever.
// The zero value reproduces all defaults (MaxHops=2, GraphWeight=0.3,
// VectorWeight=0.7, HopDecay={1.0,0.7,0.5}, SeedTopK=10).
// All other fields default to their zero/nil value (disabled).
//
// Weight normalization: if GraphWeight+VectorWeight is not within 0.01 of 1.0,
// NewGraphRetriever normalizes both proportionally.
//
// Hop-decay length: if len(HopDecay) < MaxHops, the last element of HopDecay
// is repeated to pad it to MaxHops length. If HopDecay is empty after defaults
// are applied, a fallback decay of 0.5 is used for all hops beyond the table.
type GraphRetrieverConfig struct {
	// MaxHops is the maximum number of BFS traversal hops from seed chunks (default 2).
	MaxHops int
	// GraphWeight is the weight for graph-derived scores in the final blend (default 0.3).
	// Why: together with VectorWeight forms the blending equation; both are normalized to sum to 1.0.
	GraphWeight float32
	// VectorWeight is the weight for vector scores in the final blend (default 0.7).
	VectorWeight float32
	// HopDecay is the per-hop score decay multiplier (default {1.0, 0.7, 0.5}).
	// Why: must have len >= MaxHops after constructor padding; last value is repeated if shorter.
	HopDecay []float32
	// Bidirectional enables traversal of both outgoing and incoming edges (default false).
	Bidirectional bool
	// RelationFilter restricts graph traversal to specified relationship types (nil = all types).
	// Why: stored as a slice for ergonomic struct-literal use; converted to map internally.
	RelationFilter []core.RelationType
	// MinTraversalScore skips edges with weight below this value (default 0).
	MinTraversalScore float32
	// SeedTopK is the number of seed chunks from the initial vector search (default 10).
	SeedTopK int
	// SeedKeywordWeight is the keyword search weight in the seed RRF merge (default 0, disabled).
	// When > 0 and the core.Store implements core.KeywordSearcher, keyword results are merged
	// with vector results to produce a more diverse seed set for graph traversal.
	SeedKeywordWeight float32
	// GraphTopK is the number of guaranteed graph-discovered slots in the final results.
	// When > 0, results are partitioned into a seed pool and a graph pool, each sorted
	// independently, then merged. Default is 0 (single-pool, backward compatible).
	GraphTopK int
	// MaxFrontierSize caps the BFS frontier per hop. When > 0, only the top n candidates
	// by tentative graph score are kept per hop. Default is 0 (unlimited).
	MaxFrontierSize int
	// Reranker sets an optional re-ranking stage that runs after graph score blending
	// and parent resolution, before the final topK trim.
	Reranker Reranker
	// Filters sets metadata filters passed to the initial vector search.
	Filters []core.ChunkFilter
	// Tracer sets the core.Tracer for distributed tracing.
	Tracer core.Tracer
	// Logger sets the structured logger.
	Logger *slog.Logger
}

// graphRetrieverConfig is the internal representation after defaults and validation.
// Why: keeps exported GraphRetrieverConfig ergonomic (slice for RelationFilter) while
// allowing efficient map-based lookups internally.
type graphRetrieverConfig struct {
	MaxHops           int
	GraphWeight       float32
	VectorWeight      float32
	HopDecay          []float32
	Bidirectional     bool
	relationFilter    map[core.RelationType]bool // Why: O(1) lookup during edge traversal.
	MinTraversalScore float32
	SeedTopK          int
	SeedKeywordWeight float32
	GraphTopK         int
	MaxFrontierSize   int
	reranker          Reranker
	filters           []core.ChunkFilter
	tracer            core.Tracer
	logger            *slog.Logger
}

// GraphRetriever combines vector search with knowledge graph traversal.
// It performs an initial vector search to find seed chunks, then traverses
// stored chunk edges to discover contextually related content.
// If the core.Store does not implement core.GraphStore, it falls back to vector-only retrieval.
type GraphRetriever struct {
	store     core.Store
	embedding core.EmbeddingProvider
	cfg       graphRetrieverConfig

	// mu guards lastSources.
	mu          sync.RWMutex
	lastSources []core.Source
}

var _ Retriever = (*GraphRetriever)(nil)

// Compile-time assertion: GraphRetriever implements core.Sourced.
var _ core.Sourced = (*GraphRetriever)(nil)

// NewGraphRetriever creates a Retriever that combines vector search with
// graph traversal for multi-hop contextual retrieval.
//
// Zero-value GraphRetrieverConfig reproduces all defaults:
// MaxHops=2, GraphWeight=0.3, VectorWeight=0.7, HopDecay={1.0,0.7,0.5}, SeedTopK=10.
//
// Construction-time normalization:
//   - If GraphWeight+VectorWeight is not within 0.01 of 1.0, both are normalized proportionally.
//   - If len(HopDecay) < MaxHops, the last element is repeated to pad to MaxHops length.
func NewGraphRetriever(store core.Store, embedding core.EmbeddingProvider, in GraphRetrieverConfig) *GraphRetriever {
	// Apply defaults for zero values.
	if in.MaxHops == 0 {
		in.MaxHops = 2
	}
	if in.GraphWeight == 0 && in.VectorWeight == 0 {
		in.GraphWeight = 0.3
		in.VectorWeight = 0.7
	}
	if len(in.HopDecay) == 0 {
		in.HopDecay = []float32{1.0, 0.7, 0.5}
	}
	if in.SeedTopK == 0 {
		in.SeedTopK = 10
	}

	// Normalize weights if they don't sum to ~1.0.
	// Why: blending equation assumes sum=1; mismatched weights silently deflate scores.
	total := in.GraphWeight + in.VectorWeight
	if total > 0 && (total < 0.99 || total > 1.01) {
		in.GraphWeight = in.GraphWeight / total
		in.VectorWeight = in.VectorWeight / total
	}

	// Pad HopDecay to at least MaxHops length using the last element.
	// Why: avoids silent out-of-bounds use of fallback 0.5 for user-provided decay tables.
	if len(in.HopDecay) < in.MaxHops {
		last := in.HopDecay[len(in.HopDecay)-1]
		for len(in.HopDecay) < in.MaxHops {
			in.HopDecay = append(in.HopDecay, last)
		}
	}

	// Convert RelationFilter slice to map for O(1) edge-traversal lookups.
	var rf map[core.RelationType]bool
	if len(in.RelationFilter) > 0 {
		rf = make(map[core.RelationType]bool, len(in.RelationFilter))
		for _, t := range in.RelationFilter {
			rf[t] = true
		}
	}

	cfg := graphRetrieverConfig{
		MaxHops:           in.MaxHops,
		GraphWeight:       in.GraphWeight,
		VectorWeight:      in.VectorWeight,
		HopDecay:          in.HopDecay,
		Bidirectional:     in.Bidirectional,
		relationFilter:    rf,
		MinTraversalScore: in.MinTraversalScore,
		SeedTopK:          in.SeedTopK,
		SeedKeywordWeight: in.SeedKeywordWeight,
		GraphTopK:         in.GraphTopK,
		MaxFrontierSize:   in.MaxFrontierSize,
		reranker:          in.Reranker,
		filters:           in.Filters,
		tracer:            in.Tracer,
		logger:            in.Logger,
	}
	return &GraphRetriever{store: store, embedding: embedding, cfg: cfg}
}

// Retrieve searches the knowledge base using vector search + graph traversal.
// On success the retrieved chunks are stored as []core.Source accessible via Sources().
func (g *GraphRetriever) Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error) {
	var (
		results []RetrievalResult
		err     error
	)
	if g.cfg.tracer != nil {
		var span core.Span
		ctx, span = g.cfg.tracer.Start(ctx, "retriever.retrieve",
			core.StringAttr("retriever.type", "graph"),
			core.IntAttr("topK", topK))
		defer func() { span.End() }()

		results, err = g.retrieveInner(ctx, query, topK)
		if err != nil {
			span.Error(err)
		} else {
			span.SetAttr(core.IntAttr("result_count", len(results)))
		}
	} else {
		results, err = g.retrieveInner(ctx, query, topK)
	}
	if err == nil {
		g.mu.Lock()
		g.lastSources = resultsToSources(results)
		g.mu.Unlock()
	}
	return results, err
}

// Sources returns the cited chunks from the most recent Retrieve call as
// []core.Source, satisfying the core.Sourced interface. Returns nil when
// no successful Retrieve call has been made.
func (g *GraphRetriever) Sources() []core.Source {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.lastSources
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
	seeds, err := g.store.SearchChunks(ctx, embs[0], g.cfg.SeedTopK, g.cfg.filters...)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	// Keyword search for seed diversity (if store supports it and weight > 0).
	if g.cfg.SeedKeywordWeight > 0 {
		if ks, ok := g.store.(core.KeywordSearcher); ok {
			kwResults, kwErr := ks.SearchChunksKeyword(ctx, query, g.cfg.SeedTopK, g.cfg.filters...)
			if kwErr != nil && g.cfg.logger != nil {
				g.cfg.logger.Warn("seed keyword search failed, using vector-only seeds", "err", kwErr)
			}
			if len(kwResults) > 0 {
				rrfResults := reciprocalRankFusion(seeds, kwResults, g.cfg.SeedKeywordWeight)
				mergedSeeds := make([]core.ScoredChunk, 0, len(rrfResults))
				chunkLookup := make(map[string]core.Chunk)
				for _, sc := range seeds {
					chunkLookup[sc.ID] = sc.Chunk
				}
				for _, sc := range kwResults {
					chunkLookup[sc.ID] = sc.Chunk
				}
				for _, rr := range rrfResults {
					if c, ok := chunkLookup[rr.ChunkID]; ok {
						mergedSeeds = append(mergedSeeds, core.ScoredChunk{Chunk: c, Score: rr.Score})
					}
				}
				seeds = mergedSeeds
			}
		}
	}

	// Track all discovered chunks with their best scores.
	type scored struct {
		chunkID      string
		score        float32
		isSeed       bool
		graphContext []EdgeContext
	}
	best := make(map[string]*scored)

	seedSet := make(map[string]bool, len(seeds))
	for _, sc := range seeds {
		seedSet[sc.ID] = true
		best[sc.ID] = &scored{chunkID: sc.ID, score: g.cfg.VectorWeight * sc.Score, isSeed: true}
	}

	// 2. Graph traversal (if store supports it).
	gs, ok := g.store.(core.GraphStore)
	if ok && len(seeds) > 0 {
		currentIDs := make([]string, len(seeds))
		for i, sc := range seeds {
			currentIDs[i] = sc.ID
		}

		visited := make(map[string]bool)
		for _, id := range currentIDs {
			visited[id] = true
		}

		for hop := 1; hop <= g.cfg.MaxHops; hop++ {
			decay := float32(0.5)
			if hop < len(g.cfg.HopDecay) {
				decay = g.cfg.HopDecay[hop]
			}

			var edges []core.ChunkEdge
			if g.cfg.Bidirectional {
				// Use single-query core.BidirectionalGraphStore when available.
				if bgs, ok := gs.(core.BidirectionalGraphStore); ok {
					edges, err = bgs.GetBothEdges(ctx, currentIDs)
				} else {
					edges, err = gs.GetEdges(ctx, currentIDs)
					if err == nil {
						incoming, ierr := gs.GetIncomingEdges(ctx, currentIDs)
						if ierr == nil {
							edges = append(edges, incoming...)
						}
					}
				}
			} else {
				edges, err = gs.GetEdges(ctx, currentIDs)
			}
			if err != nil {
				break // degrade gracefully
			}

			var nextIDs []string
			for _, edge := range edges {
				if g.cfg.relationFilter != nil && !g.cfg.relationFilter[edge.Relation] {
					continue
				}
				if edge.Weight < g.cfg.MinTraversalScore {
					continue
				}

				// Determine the neighbor (could be source or target depending on direction).
				neighborID := edge.TargetID
				fromID := edge.SourceID
				if visited[neighborID] {
					neighborID = edge.SourceID
					fromID = edge.TargetID
					if visited[neighborID] {
						continue
					}
				}
				visited[neighborID] = true

				ec := EdgeContext{
					FromChunkID: fromID,
					Relation:    edge.Relation,
					Description: edge.Description,
				}

				graphScore := g.cfg.GraphWeight * edge.Weight * decay
				if existing, ok := best[neighborID]; ok {
					if graphScore > existing.score {
						existing.score = graphScore
					}
					if len(existing.graphContext) < 3 {
						existing.graphContext = append(existing.graphContext, ec)
					}
				} else {
					best[neighborID] = &scored{chunkID: neighborID, score: graphScore, graphContext: []EdgeContext{ec}}
				}
				nextIDs = append(nextIDs, neighborID)
			}

			// Cap frontier size if configured.
			if g.cfg.MaxFrontierSize > 0 && len(nextIDs) > g.cfg.MaxFrontierSize {
				sort.Slice(nextIDs, func(i, j int) bool {
					si, sj := best[nextIDs[i]], best[nextIDs[j]]
					if si == nil {
						return false
					}
					if sj == nil {
						return true
					}
					return si.score > sj.score
				})
				nextIDs = nextIDs[:g.cfg.MaxFrontierSize]
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
		if !seedSet[id] {
			needFetch = append(needFetch, id)
		}
	}

	chunkContent := make(map[string]core.Chunk)
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
			Content:      c.Content,
			Score:        s.score,
			ChunkID:      c.ID,
			ParentID:     c.ParentID,
			DocumentID:   c.DocumentID,
			GraphContext: s.graphContext,
		})
	}

	// 5. Parent-child resolution.
	results = resolveParentChunks(ctx, g.store, results)

	// 6. Populate document metadata.
	populateDocumentMeta(ctx, g.store, results)

	// 7. Reranker (if configured).
	if g.cfg.reranker != nil {
		results, err = g.cfg.reranker.Rerank(ctx, query, results, topK)
		if err != nil {
			return nil, fmt.Errorf("rerank: %w", err)
		}
	}

	// 8. Two-pool merge or single-pool sort.
	if g.cfg.GraphTopK > 0 {
		var seedPool, graphPool []RetrievalResult
		for _, r := range results {
			if seedSet[r.ChunkID] {
				seedPool = append(seedPool, r)
			} else {
				graphPool = append(graphPool, r)
			}
		}
		sort.Slice(seedPool, func(i, j int) bool { return seedPool[i].Score > seedPool[j].Score })
		sort.Slice(graphPool, func(i, j int) bool { return graphPool[i].Score > graphPool[j].Score })

		seedSlots := topK - g.cfg.GraphTopK
		if seedSlots < 0 {
			seedSlots = 0
		}
		if len(seedPool) > seedSlots {
			seedPool = seedPool[:seedSlots]
		}
		if len(graphPool) > g.cfg.GraphTopK {
			graphPool = graphPool[:g.cfg.GraphTopK]
		}
		results = append(seedPool, graphPool...)
	} else {
		sort.Slice(results, func(i, j int) bool {
			return results[i].Score > results[j].Score
		})
	}

	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}
