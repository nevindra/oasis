package sqlite

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/nevindra/oasis"
)

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

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, document_id, embedding FROM chunks WHERE embedding IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("load vec index: %w", err)
	}
	defer rows.Close()

	idx := make(map[string]vecEntry)
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
		idx[id] = vecEntry{embedding: emb, documentID: docID}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate vec index: %w", err)
	}

	s.vecIndex = idx
	s.vecReady = true
	s.logger.Info("sqlite: vector index loaded", "chunks", len(idx), "duration", time.Since(start))
	return nil
}

// vecAdd adds or updates entries in the in-memory vector index.
func (s *Store) vecAdd(chunks []oasis.Chunk) {
	s.vecMu.Lock()
	defer s.vecMu.Unlock()
	if !s.vecReady {
		return // index not yet loaded; will be populated on first search
	}
	for _, c := range chunks {
		if len(c.Embedding) > 0 {
			s.vecIndex[c.ID] = vecEntry{embedding: c.Embedding, documentID: c.DocumentID}
		}
	}
}

// vecRemoveByDocument removes all entries for a document from the vector index.
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
}

// vecSearch performs cosine similarity search against the in-memory index.
// If allowedIDs is non-nil, only those chunk IDs are scored.
// Returns the top-K results sorted by score descending.
func (s *Store) vecSearch(query []float32, topK int, allowedIDs map[string]bool) []oasis.ScoredChunk {
	s.vecMu.RLock()
	defer s.vecMu.RUnlock()

	type scored struct {
		id    string
		score float32
	}

	var results []scored

	if allowedIDs != nil {
		results = make([]scored, 0, len(allowedIDs))
		for id := range allowedIDs {
			entry, ok := s.vecIndex[id]
			if !ok {
				continue
			}
			results = append(results, scored{id: id, score: cosineSimilarity(query, entry.embedding)})
		}
	} else {
		results = make([]scored, 0, len(s.vecIndex))
		for id, entry := range s.vecIndex {
			results = append(results, scored{id: id, score: cosineSimilarity(query, entry.embedding)})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > topK {
		results = results[:topK]
	}

	// Build ScoredChunk results with minimal data; caller fetches full content.
	out := make([]oasis.ScoredChunk, len(results))
	for i, r := range results {
		entry := s.vecIndex[r.id]
		out[i] = oasis.ScoredChunk{
			Chunk: oasis.Chunk{ID: r.id, DocumentID: entry.documentID},
			Score: r.score,
		}
	}
	return out
}
