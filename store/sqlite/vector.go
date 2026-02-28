package sqlite

import (
	"container/heap"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nevindra/oasis"
)

// vecIndexWarnThreshold logs a warning when the in-memory vector index exceeds
// this many entries. Large indices consume significant RAM and make brute-force
// search slow — consider Postgres with pgvector HNSW for corpora this size.
const vecIndexWarnThreshold = 50_000

// --- Vector math ---

// cosineSimilarity computes the cosine similarity between two vectors.
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
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// vecNorm computes the L2 norm of a vector.
func vecNorm(v []float32) float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return float32(math.Sqrt(sum))
}

// --- Min-heap for top-K selection ---

type scoredEntry struct {
	id    string
	score float32
}

// minScoreHeap is a min-heap of scored entries. The root is always the lowest
// score, making it efficient to maintain a top-K set: compare new entries
// against the root and replace only when the new score is higher.
type minScoreHeap []scoredEntry

func (h minScoreHeap) Len() int            { return len(h) }
func (h minScoreHeap) Less(i, j int) bool  { return h[i].score < h[j].score }
func (h minScoreHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *minScoreHeap) Push(x any)         { *h = append(*h, x.(scoredEntry)) }
func (h *minScoreHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// serializeEmbedding converts []float32 to compact binary (little-endian,
// 4 bytes per float). ~5x smaller than JSON for typical embeddings.
func serializeEmbedding(embedding []float32) []byte {
	buf := make([]byte, len(embedding)*4)
	for i, v := range embedding {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// deserializeEmbedding parses binary little-endian float32 data back to []float32.
func deserializeEmbedding(data []byte) ([]float32, error) {
	if len(data) == 0 || len(data)%4 != 0 {
		return nil, nil
	}
	n := len(data) / 4
	out := make([]float32, n)
	for i := range n {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return out, nil
}

// --- In-memory vector index ---

// loadVecIndex populates the in-memory embedding cache from the database.
// Called lazily on first SearchChunks. Subsequent calls are no-ops.
// When maxVecEntries is set, the oldest documents are evicted FIFO to stay
// under the cap.
func (s *Store) loadVecIndex(ctx context.Context) error {
	s.vecMu.RLock()
	if s.vecReady {
		s.vecMu.RUnlock()
		return nil
	}
	s.vecMu.RUnlock()

	s.vecMu.Lock()
	defer s.vecMu.Unlock()

	// Double-check after acquiring write lock.
	if s.vecReady {
		return nil
	}

	start := time.Now()
	s.logger.Debug("sqlite: loading vector index")

	// Order by document created_at so oldest docs are loaded first (and
	// evicted first when the cap is hit).
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.document_id, c.embedding
		 FROM chunks c
		 JOIN documents d ON d.id = c.document_id
		 WHERE c.embedding IS NOT NULL
		 ORDER BY d.created_at ASC`)
	if err != nil {
		return fmt.Errorf("load vec index: %w", err)
	}
	defer rows.Close()

	idx := make(map[string]vecEntry)
	docOrder := make([]string, 0)
	docChunkCount := make(map[string]int)
	docSeen := make(map[string]bool)

	for rows.Next() {
		var id, docID string
		var embBlob []byte
		if err := rows.Scan(&id, &docID, &embBlob); err != nil {
			return fmt.Errorf("scan vec index: %w", err)
		}
		emb, err := deserializeEmbedding(embBlob)
		if err != nil || emb == nil {
			continue
		}
		idx[id] = vecEntry{embedding: emb, documentID: docID, norm: vecNorm(emb)}
		if !docSeen[docID] {
			docSeen[docID] = true
			docOrder = append(docOrder, docID)
		}
		docChunkCount[docID]++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate vec index: %w", err)
	}

	total := len(idx)

	// Evict oldest documents if over the cap.
	evicted := make(map[string]bool)
	if s.maxVecEntries > 0 && len(idx) > s.maxVecEntries {
		s.logger.Warn("sqlite: vector index exceeds cap, evicting old documents",
			"total", total, "cap", s.maxVecEntries)
		for len(idx) > s.maxVecEntries && len(docOrder) > 0 {
			oldDoc := docOrder[0]
			docOrder = docOrder[1:]
			evicted[oldDoc] = true
			for id, entry := range idx {
				if entry.documentID == oldDoc {
					delete(idx, id)
				}
			}
			delete(docChunkCount, oldDoc)
		}
		s.logger.Warn("sqlite: vector index evicted oldest documents",
			"evicted_docs", len(evicted), "remaining", len(idx))
	}

	s.vecIndex = idx
	s.docOrder = docOrder
	s.docChunkCount = docChunkCount
	s.evictedDocs = evicted
	s.vecReady = true
	s.logger.Info("sqlite: vector index loaded",
		"chunks", len(idx), "evicted_docs", len(evicted), "duration", time.Since(start))

	if len(idx) >= vecIndexWarnThreshold {
		s.logger.Warn("sqlite: vector index is large — brute-force search will be slow; consider Postgres with pgvector for corpora this size",
			"chunks", len(idx), "threshold", vecIndexWarnThreshold)
	}

	return nil
}

// vecAdd adds or updates entries in the in-memory vector index.
// When maxVecEntries is set and the cap would be exceeded, the oldest
// document's chunks are evicted first.
func (s *Store) vecAdd(chunks []oasis.Chunk) {
	s.vecMu.Lock()
	defer s.vecMu.Unlock()
	if !s.vecReady {
		return // index not yet loaded; will be populated on first search
	}

	// Count new entries that would be added (not updates).
	var newCount int
	for _, c := range chunks {
		if len(c.Embedding) > 0 {
			if _, exists := s.vecIndex[c.ID]; !exists {
				newCount++
			}
		}
	}

	// Evict oldest documents to make room.
	if s.maxVecEntries > 0 {
		for len(s.vecIndex)+newCount > s.maxVecEntries && len(s.docOrder) > 0 {
			oldDoc := s.docOrder[0]
			s.docOrder = s.docOrder[1:]
			for id, entry := range s.vecIndex {
				if entry.documentID == oldDoc {
					delete(s.vecIndex, id)
				}
			}
			delete(s.docChunkCount, oldDoc)
			if s.evictedDocs == nil {
				s.evictedDocs = make(map[string]bool)
			}
			s.evictedDocs[oldDoc] = true
			s.logger.Warn("sqlite: vector index evicted document to make room",
				"evicted_doc", oldDoc, "index_size", len(s.vecIndex))
		}
	}

	// Track which docIDs are being added.
	addedDocs := make(map[string]bool)
	for _, c := range chunks {
		if len(c.Embedding) > 0 {
			s.vecIndex[c.ID] = vecEntry{embedding: c.Embedding, documentID: c.DocumentID, norm: vecNorm(c.Embedding)}
			addedDocs[c.DocumentID] = true
			s.docChunkCount[c.DocumentID]++
		}
	}

	// Append new docIDs to docOrder.
	for docID := range addedDocs {
		if !slices.Contains(s.docOrder, docID) {
			s.docOrder = append(s.docOrder, docID)
		}
		// If this doc was previously evicted, remove from evicted set.
		delete(s.evictedDocs, docID)
	}
}

// vecRemoveByDocument removes all entries for a document from the vector index
// and cleans up associated tracking state.
func (s *Store) vecRemoveByDocument(docID string) {
	s.vecMu.Lock()
	defer s.vecMu.Unlock()
	if !s.vecReady {
		return
	}
	for id, entry := range s.vecIndex {
		if entry.documentID == docID {
			delete(s.vecIndex, id)
		}
	}
	delete(s.docChunkCount, docID)
	delete(s.evictedDocs, docID)
	s.docOrder = slices.DeleteFunc(s.docOrder, func(d string) bool { return d == docID })
}

// vecSearch performs cosine similarity search against the in-memory index using
// a min-heap for top-K selection. Pre-computed norms avoid redundant work per
// comparison — only the dot product is computed per entry.
// If allowedIDs is non-nil, only those chunk IDs are scored.
// Returns the top-K results sorted by score descending.
func (s *Store) vecSearch(query []float32, topK int, allowedIDs map[string]bool) []oasis.ScoredChunk {
	s.vecMu.RLock()
	defer s.vecMu.RUnlock()

	qNorm := float64(vecNorm(query))
	if qNorm == 0 {
		return nil
	}

	h := make(minScoreHeap, 0, topK+1)

	scoreAndPush := func(id string, entry vecEntry) {
		if entry.norm == 0 {
			return
		}
		var dot float64
		for i := range query {
			dot += float64(query[i]) * float64(entry.embedding[i])
		}
		sim := float32(dot / (qNorm * float64(entry.norm)))

		if h.Len() < topK {
			heap.Push(&h, scoredEntry{id: id, score: sim})
		} else if sim > h[0].score {
			h[0] = scoredEntry{id: id, score: sim}
			heap.Fix(&h, 0)
		}
	}

	if allowedIDs != nil {
		for id := range allowedIDs {
			entry, ok := s.vecIndex[id]
			if !ok {
				continue
			}
			scoreAndPush(id, entry)
		}
	} else {
		for id, entry := range s.vecIndex {
			scoreAndPush(id, entry)
		}
	}

	// Extract from heap in descending score order.
	out := make([]oasis.ScoredChunk, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		e := heap.Pop(&h).(scoredEntry)
		entry := s.vecIndex[e.id]
		out[i] = oasis.ScoredChunk{
			Chunk: oasis.Chunk{ID: e.id, DocumentID: entry.documentID},
			Score: e.score,
		}
	}
	return out
}

// vecSearchBatch performs cosine similarity search for multiple query vectors
// in a single pass over the index. This is more efficient than calling vecSearch
// N times because the index is iterated once and each entry's data is loaded
// into cache once for scoring against all queries.
func (s *Store) vecSearchBatch(queries [][]float32, topK int, allowedIDs map[string]bool) [][]oasis.ScoredChunk {
	s.vecMu.RLock()
	defer s.vecMu.RUnlock()

	nq := len(queries)

	// Pre-compute query norms.
	qNorms := make([]float64, nq)
	for i, q := range queries {
		qNorms[i] = float64(vecNorm(q))
	}

	// One heap per query.
	heaps := make([]minScoreHeap, nq)
	for i := range heaps {
		heaps[i] = make(minScoreHeap, 0, topK+1)
	}

	scoreEntry := func(id string, entry vecEntry) {
		if entry.norm == 0 {
			return
		}
		eNorm := float64(entry.norm)
		emb := entry.embedding

		for qi := range queries {
			if qNorms[qi] == 0 {
				continue
			}
			q := queries[qi]
			var dot float64
			for j := range q {
				dot += float64(q[j]) * float64(emb[j])
			}
			sim := float32(dot / (qNorms[qi] * eNorm))

			h := &heaps[qi]
			if h.Len() < topK {
				heap.Push(h, scoredEntry{id: id, score: sim})
			} else if sim > (*h)[0].score {
				(*h)[0] = scoredEntry{id: id, score: sim}
				heap.Fix(h, 0)
			}
		}
	}

	if allowedIDs != nil {
		for id := range allowedIDs {
			entry, ok := s.vecIndex[id]
			if !ok {
				continue
			}
			scoreEntry(id, entry)
		}
	} else {
		for id, entry := range s.vecIndex {
			scoreEntry(id, entry)
		}
	}

	// Extract results per query in descending score order.
	results := make([][]oasis.ScoredChunk, nq)
	for qi := range heaps {
		h := &heaps[qi]
		r := make([]oasis.ScoredChunk, h.Len())
		for i := len(r) - 1; i >= 0; i-- {
			e := heap.Pop(h).(scoredEntry)
			entry := s.vecIndex[e.id]
			r[i] = oasis.ScoredChunk{
				Chunk: oasis.Chunk{ID: e.id, DocumentID: entry.documentID},
				Score: e.score,
			}
		}
		results[qi] = r
	}
	return results
}

// vecHasEvicted reports whether any documents have been evicted from the
// in-memory index. Caller must hold at least vecMu.RLock.
func (s *Store) vecHasEvicted() bool {
	return len(s.evictedDocs) > 0
}

// vecEvictedDocIDs returns a copy of the evicted document IDs.
// Caller must hold at least vecMu.RLock.
func (s *Store) vecEvictedDocIDs() []string {
	ids := make([]string, 0, len(s.evictedDocs))
	for id := range s.evictedDocs {
		ids = append(ids, id)
	}
	return ids
}

// vecDiskFallback searches chunks from evicted documents by reading embeddings
// from disk and computing cosine similarity. This is slower than the in-memory
// path but ensures evicted documents remain searchable.
func (s *Store) vecDiskFallback(ctx context.Context, query []float32, topK int, evictedDocIDs []string, filterWhere string, filterArgs []any) ([]oasis.ScoredChunk, error) {
	if len(evictedDocIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(evictedDocIDs))
	args := make([]any, len(evictedDocIDs))
	for i, id := range evictedDocIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	q := fmt.Sprintf(
		`SELECT c.id, c.document_id, c.embedding FROM chunks c
		 WHERE c.document_id IN (%s) AND c.embedding IS NOT NULL%s`,
		strings.Join(placeholders, ","), filterWhere)
	args = append(args, filterArgs...)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("disk fallback query: %w", err)
	}
	defer rows.Close()

	type scored struct {
		id    string
		docID string
		score float32
	}
	var results []scored

	for rows.Next() {
		var id, docID string
		var embBlob []byte
		if err := rows.Scan(&id, &docID, &embBlob); err != nil {
			return nil, fmt.Errorf("disk fallback scan: %w", err)
		}
		emb, err := deserializeEmbedding(embBlob)
		if err != nil || emb == nil {
			continue
		}
		results = append(results, scored{
			id:    id,
			docID: docID,
			score: cosineSimilarity(query, emb),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("disk fallback iterate: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})
	if len(results) > topK {
		results = results[:topK]
	}

	out := make([]oasis.ScoredChunk, len(results))
	for i, r := range results {
		out[i] = oasis.ScoredChunk{
			Chunk: oasis.Chunk{ID: r.id, DocumentID: r.docID},
			Score: r.score,
		}
	}
	return out, nil
}
