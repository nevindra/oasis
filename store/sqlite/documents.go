package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nevindra/oasis"
)

// --- Documents + Chunks ---

// safeMetaKey returns true if the key contains only alphanumeric chars and underscores.
// This prevents SQL injection when the key is interpolated into JSON path expressions.
func safeMetaKey(key string) bool {
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return len(key) > 0
}

// buildChunkFilters translates ChunkFilter values into SQL WHERE clauses.
// Returns (whereClause, args, needsDocJoin). The whereClause includes a leading " AND ..."
// for each filter. needsDocJoin is true when any filter references document-level fields.
func buildChunkFilters(filters []oasis.ChunkFilter) (string, []any, bool) {
	if len(filters) == 0 {
		return "", nil, false
	}
	var clauses []string
	var args []any
	needsDocJoin := false

	for _, f := range filters {
		switch {
		case f.Field == "document_id":
			if f.Op == oasis.OpIn {
				ids, ok := f.Value.([]string)
				if !ok || len(ids) == 0 {
					continue
				}
				placeholders := make([]string, len(ids))
				for i, id := range ids {
					placeholders[i] = "?"
					args = append(args, id)
				}
				clauses = append(clauses, "c.document_id IN ("+strings.Join(placeholders, ",")+")")
			} else if f.Op == oasis.OpEq {
				clauses = append(clauses, "c.document_id = ?")
				args = append(args, f.Value)
			} else if f.Op == oasis.OpNeq {
				clauses = append(clauses, "c.document_id != ?")
				args = append(args, f.Value)
			}

		case f.Field == "source":
			if f.Op != oasis.OpEq {
				continue
			}
			needsDocJoin = true
			clauses = append(clauses, "d.source = ?")
			args = append(args, f.Value)

		case f.Field == "created_at":
			needsDocJoin = true
			if f.Op == oasis.OpGt {
				clauses = append(clauses, "d.created_at > ?")
				args = append(args, f.Value)
			} else if f.Op == oasis.OpLt {
				clauses = append(clauses, "d.created_at < ?")
				args = append(args, f.Value)
			}

		case strings.HasPrefix(f.Field, "meta."):
			key := strings.TrimPrefix(f.Field, "meta.")
			if !safeMetaKey(key) {
				continue
			}
			clauses = append(clauses, "json_extract(c.metadata, '$."+key+"') = ?")
			args = append(args, f.Value)
		}
	}

	if len(clauses) == 0 {
		return "", nil, false
	}
	return " AND " + strings.Join(clauses, " AND "), args, needsDocJoin
}

// storeChunksBatchSize is the max rows per multi-value INSERT for chunks.
// 7 params per row; SQLite default SQLITE_MAX_VARIABLE_NUMBER is 999 → 142 max.
const storeChunksBatchSize = 100

// StoreDocument inserts a document and all its chunks in a single transaction.
// Chunks are inserted in batches using multi-value INSERT statements to reduce
// round-trips from 3N+1 to ~3*(N/batchSize)+1 SQL statements.
func (s *Store) StoreDocument(ctx context.Context, doc oasis.Document, chunks []oasis.Chunk) error {
	start := time.Now()
	s.logger.Debug("sqlite: store document", "id", doc.ID, "title", doc.Title, "source", doc.Source, "chunks", len(chunks))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		`INSERT OR REPLACE INTO documents (id, title, source, content, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		doc.ID, doc.Title, doc.Source, doc.Content, doc.CreatedAt,
	)
	if err != nil {
		s.logger.Error("sqlite: insert document failed", "id", doc.ID, "error", err)
		return fmt.Errorf("insert document: %w", err)
	}

	// Insert chunks in batches.
	for i := 0; i < len(chunks); i += storeChunksBatchSize {
		end := min(i+storeChunksBatchSize, len(chunks))
		batch := chunks[i:end]

		// --- Batch chunk INSERT ---
		var sb strings.Builder
		sb.WriteString(`INSERT OR REPLACE INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata) VALUES `)
		chunkArgs := make([]any, 0, len(batch)*7)
		for j, chunk := range batch {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?,?,?,?,?)")

			var embBlob []byte
			if len(chunk.Embedding) > 0 {
				embBlob = serializeEmbedding(chunk.Embedding)
			}
			var parentID *string
			if chunk.ParentID != "" {
				parentID = &chunk.ParentID
			}
			var metaJSON *string
			if chunk.Metadata != nil {
				data, _ := json.Marshal(chunk.Metadata)
				v := string(data)
				metaJSON = &v
			}
			chunkArgs = append(chunkArgs, chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, embBlob, metaJSON)
		}
		if _, err := tx.ExecContext(ctx, sb.String(), chunkArgs...); err != nil {
			s.logger.Error("sqlite: insert chunks batch failed", "batch_offset", i, "batch_size", len(batch), "doc_id", doc.ID, "error", err)
			return fmt.Errorf("insert chunks batch: %w", err)
		}

		// --- Batch FTS DELETE ---
		var delSB strings.Builder
		delSB.WriteString(`DELETE FROM chunks_fts WHERE chunk_id IN (`)
		delArgs := make([]any, len(batch))
		for j, chunk := range batch {
			if j > 0 {
				delSB.WriteByte(',')
			}
			delSB.WriteByte('?')
			delArgs[j] = chunk.ID
		}
		delSB.WriteByte(')')
		_, _ = tx.ExecContext(ctx, delSB.String(), delArgs...)

		// --- Batch FTS INSERT ---
		var ftsSB strings.Builder
		ftsSB.WriteString(`INSERT INTO chunks_fts(chunk_id, content) VALUES `)
		ftsArgs := make([]any, 0, len(batch)*2)
		for j, chunk := range batch {
			if j > 0 {
				ftsSB.WriteByte(',')
			}
			ftsSB.WriteString("(?,?)")
			ftsArgs = append(ftsArgs, chunk.ID, chunk.Content)
		}
		if _, err := tx.ExecContext(ctx, ftsSB.String(), ftsArgs...); err != nil {
			return fmt.Errorf("insert chunks fts batch: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		s.logger.Error("sqlite: store document commit failed", "id", doc.ID, "error", err)
		return fmt.Errorf("commit tx: %w", err)
	}

	// Keep in-memory vector index in sync.
	s.vecAdd(chunks)

	s.logger.Debug("sqlite: store document ok", "id", doc.ID, "chunks", len(chunks), "duration", time.Since(start))
	return nil
}

// ListDocuments returns all documents ordered by creation time (newest first).
func (s *Store) ListDocuments(ctx context.Context, limit int) ([]oasis.Document, error) {
	start := time.Now()
	s.logger.Debug("sqlite: list documents", "limit", limit)

	query := `SELECT id, title, source, content, created_at FROM documents ORDER BY created_at DESC`
	var args []any
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.Error("sqlite: list documents failed", "error", err)
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	var docs []oasis.Document
	for rows.Next() {
		var d oasis.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.Content, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, d)
	}
	s.logger.Debug("sqlite: list documents ok", "count", len(docs), "duration", time.Since(start))
	return docs, rows.Err()
}

// ListDocumentMeta returns all documents without the Content field, ordered by
// creation time (newest first). Use this instead of ListDocuments when only
// ID, Title, Source, and CreatedAt are needed to avoid loading large document
// bodies into memory.
func (s *Store) ListDocumentMeta(ctx context.Context, limit int) ([]oasis.Document, error) {
	start := time.Now()
	s.logger.Debug("sqlite: list document meta", "limit", limit)

	query := `SELECT id, title, source, created_at FROM documents ORDER BY created_at DESC`
	var args []any
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		s.logger.Error("sqlite: list document meta failed", "error", err)
		return nil, fmt.Errorf("list document meta: %w", err)
	}
	defer rows.Close()

	var docs []oasis.Document
	for rows.Next() {
		var d oasis.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan document meta: %w", err)
		}
		docs = append(docs, d)
	}
	s.logger.Debug("sqlite: list document meta ok", "count", len(docs), "duration", time.Since(start))
	return docs, rows.Err()
}

// DeleteDocument removes a document, its chunks, and associated FTS entries.
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("sqlite: delete document", "id", id)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		`DELETE FROM chunks_fts WHERE chunk_id IN (SELECT id FROM chunks WHERE document_id = ?)`, id)
	if err != nil {
		return fmt.Errorf("delete document fts: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`DELETE FROM chunk_edges WHERE source_id IN (SELECT id FROM chunks WHERE document_id = ?) OR target_id IN (SELECT id FROM chunks WHERE document_id = ?)`, id, id)
	if err != nil {
		return fmt.Errorf("delete document edges: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete document chunks: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.logger.Error("sqlite: delete document commit failed", "id", id, "error", err)
		return err
	}

	// Keep in-memory vector index in sync.
	s.vecRemoveByDocument(id)

	s.logger.Debug("sqlite: delete document ok", "id", id, "duration", time.Since(start))
	return nil
}

// SearchChunks performs cosine similarity search using an in-memory vector index.
// On the first call, embeddings are loaded from disk into memory. Subsequent calls
// score against the cached embeddings without touching SQLite, then fetch full
// chunk content only for the top-K results.
//
// When maxVecEntries is configured and some documents have been evicted from the
// in-memory index, a disk-based fallback searches evicted chunks and merges
// results with the in-memory top-K.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search chunks", "top_k", topK, "embedding_dim", len(embedding), "filters", len(filters))

	// Ensure in-memory vector index is loaded.
	if err := s.loadVecIndex(ctx); err != nil {
		return nil, fmt.Errorf("load vec index: %w", err)
	}

	whereExtra, filterArgs, needsDocJoin := buildChunkFilters(filters)

	// If filters are present, query SQL for matching chunk IDs only,
	// then score those against the in-memory index.
	var allowedIDs map[string]bool
	if whereExtra != "" {
		var q string
		if needsDocJoin {
			q = `SELECT c.id FROM chunks c JOIN documents d ON d.id = c.document_id
				WHERE c.embedding IS NOT NULL` + whereExtra
		} else {
			q = `SELECT c.id FROM chunks c WHERE c.embedding IS NOT NULL` + whereExtra
		}
		rows, err := s.db.QueryContext(ctx, q, filterArgs...)
		if err != nil {
			return nil, fmt.Errorf("search chunks filter: %w", err)
		}
		defer rows.Close()
		allowedIDs = make(map[string]bool)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan filter id: %w", err)
			}
			allowedIDs[id] = true
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate filter ids: %w", err)
		}
	}

	// Score against in-memory embeddings — no blob deserialization per query.
	scored := s.vecSearch(embedding, topK, allowedIDs)

	// Disk fallback: if documents were evicted and we have room for more
	// results, search evicted chunks from disk.
	s.vecMu.RLock()
	hasEvicted := s.vecHasEvicted()
	var evictedDocIDs []string
	if hasEvicted && len(scored) < topK {
		evictedDocIDs = s.vecEvictedDocIDs()
	}
	s.vecMu.RUnlock()

	if len(evictedDocIDs) > 0 {
		s.logger.Debug("sqlite: disk fallback for evicted chunks",
			"evicted_docs", len(evictedDocIDs), "in_memory_results", len(scored))
		diskResults, err := s.vecDiskFallback(ctx, embedding, topK, evictedDocIDs, whereExtra, filterArgs)
		if err != nil {
			s.logger.Error("sqlite: disk fallback failed, using in-memory only", "error", err)
		} else {
			scored = mergeAndDedup(scored, diskResults, topK)
		}
	}

	if len(scored) == 0 {
		s.logger.Debug("sqlite: search chunks ok", "scanned", 0, "returned", 0, "duration", time.Since(start))
		return nil, nil
	}

	// Fetch full chunk data only for top-K results.
	ids := make([]string, len(scored))
	scoreMap := make(map[string]float32, len(scored))
	for i, sc := range scored {
		ids[i] = sc.ID
		scoreMap[sc.ID] = sc.Score
	}

	chunks, err := s.GetChunksByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("fetch top-k chunks: %w", err)
	}

	results := make([]oasis.ScoredChunk, 0, len(chunks))
	for _, c := range chunks {
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: scoreMap[c.ID]})
	}

	// Re-sort since GetChunksByIDs doesn't preserve order.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	s.logger.Debug("sqlite: search chunks ok", "index_size", len(s.vecIndex), "returned", len(results), "duration", time.Since(start))
	return results, nil
}

// mergeAndDedup combines in-memory and disk results, removes duplicates, and
// returns the top-K by score.
func mergeAndDedup(a, b []oasis.ScoredChunk, topK int) []oasis.ScoredChunk {
	seen := make(map[string]bool, len(a))
	merged := make([]oasis.ScoredChunk, 0, len(a)+len(b))
	for _, sc := range a {
		seen[sc.ID] = true
		merged = append(merged, sc)
	}
	for _, sc := range b {
		if seen[sc.ID] {
			continue
		}
		merged = append(merged, sc)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})
	if len(merged) > topK {
		merged = merged[:topK]
	}
	return merged
}

// SearchChunksBatch performs cosine similarity search for multiple embeddings
// in a single pass over the in-memory vector index. Filters (applied once) are
// shared across all queries. This is much more efficient than calling
// SearchChunks N times when searching for neighbors of many chunks in a
// document (e.g. cross-document extraction).
//
// Returns one result slice per input embedding, each sorted by score descending.
func (s *Store) SearchChunksBatch(ctx context.Context, embeddings [][]float32, topK int, filters ...oasis.ChunkFilter) ([][]oasis.ScoredChunk, error) {
	start := time.Now()
	nq := len(embeddings)
	s.logger.Debug("sqlite: search chunks batch", "queries", nq, "top_k", topK, "filters", len(filters))

	if nq == 0 {
		return nil, nil
	}

	// Ensure in-memory vector index is loaded.
	if err := s.loadVecIndex(ctx); err != nil {
		return nil, fmt.Errorf("load vec index: %w", err)
	}

	// Build allowedIDs once — shared across all queries.
	whereExtra, filterArgs, needsDocJoin := buildChunkFilters(filters)
	var allowedIDs map[string]bool
	if whereExtra != "" {
		var q string
		if needsDocJoin {
			q = `SELECT c.id FROM chunks c JOIN documents d ON d.id = c.document_id
				WHERE c.embedding IS NOT NULL` + whereExtra
		} else {
			q = `SELECT c.id FROM chunks c WHERE c.embedding IS NOT NULL` + whereExtra
		}
		rows, err := s.db.QueryContext(ctx, q, filterArgs...)
		if err != nil {
			return nil, fmt.Errorf("search chunks batch filter: %w", err)
		}
		defer rows.Close()
		allowedIDs = make(map[string]bool)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, fmt.Errorf("scan batch filter id: %w", err)
			}
			allowedIDs[id] = true
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate batch filter ids: %w", err)
		}
	}

	// Single-pass batch scoring.
	batchScored := s.vecSearchBatch(embeddings, topK, allowedIDs)

	// Collect all unique chunk IDs from all results for a single GetChunksByIDs call.
	idSet := make(map[string]bool)
	for _, scored := range batchScored {
		for _, sc := range scored {
			idSet[sc.ID] = true
		}
	}

	if len(idSet) == 0 {
		s.logger.Debug("sqlite: search chunks batch ok", "returned", 0, "duration", time.Since(start))
		return make([][]oasis.ScoredChunk, nq), nil
	}

	allIDs := make([]string, 0, len(idSet))
	for id := range idSet {
		allIDs = append(allIDs, id)
	}
	chunkMap, err := s.getChunksByIDsMap(ctx, allIDs)
	if err != nil {
		return nil, fmt.Errorf("fetch batch chunks: %w", err)
	}

	// Build final results per query, enriching with full chunk data.
	results := make([][]oasis.ScoredChunk, nq)
	for qi, scored := range batchScored {
		r := make([]oasis.ScoredChunk, 0, len(scored))
		for _, sc := range scored {
			if chunk, ok := chunkMap[sc.ID]; ok {
				r = append(r, oasis.ScoredChunk{Chunk: chunk, Score: sc.Score})
			}
		}
		results[qi] = r
	}

	s.logger.Debug("sqlite: search chunks batch ok", "queries", nq, "unique_chunks", len(idSet), "duration", time.Since(start))
	return results, nil
}

// getChunksByIDsMap fetches chunks by ID and returns them as a map for O(1) lookup.
func (s *Store) getChunksByIDsMap(ctx context.Context, ids []string) (map[string]oasis.Chunk, error) {
	chunks, err := s.GetChunksByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	m := make(map[string]oasis.Chunk, len(chunks))
	for _, c := range chunks {
		m[c.ID] = c
	}
	return m, nil
}

// sanitizeFTS5Query escapes FTS5 metacharacters so the query is treated as
// literal search terms. FTS5 special characters (", *, +, -, ^, (, )) are
// replaced with spaces. The result is trimmed; if empty, returns "".
func sanitizeFTS5Query(query string) string {
	var b strings.Builder
	b.Grow(len(query))
	for _, r := range query {
		switch r {
		case '"', '*', '+', '-', '^', '(', ')', '{', '}':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// SearchChunksKeyword performs full-text keyword search over document chunks
// using SQLite FTS5. Results are sorted by relevance (FTS5 rank).
func (s *Store) SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search chunks keyword", "query", query, "top_k", topK, "filters", len(filters))

	query = sanitizeFTS5Query(query)
	if query == "" {
		return nil, nil
	}

	whereExtra, filterArgs, needsDocJoin := buildChunkFilters(filters)

	var q string
	baseArgs := []any{query}
	if needsDocJoin {
		q = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata, f.rank
			FROM chunks_fts f
			JOIN chunks c ON c.id = f.chunk_id
			JOIN documents d ON d.id = c.document_id
			WHERE chunks_fts MATCH ?` + whereExtra + `
			ORDER BY f.rank LIMIT ?`
	} else {
		q = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata, f.rank
			FROM chunks_fts f
			JOIN chunks c ON c.id = f.chunk_id
			WHERE chunks_fts MATCH ?` + whereExtra + `
			ORDER BY f.rank LIMIT ?`
	}
	allArgs := append(baseArgs, filterArgs...)
	allArgs = append(allArgs, topK)

	rows, err := s.db.QueryContext(ctx, q, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredChunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var metaJSON sql.NullString
		var rank float64
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON, &rank); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		// FTS5 rank is negative (closer to 0 = better). Use -rank as score.
		score := float32(-rank)
		if score < 0 {
			score = 0
		}
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: score})
	}
	s.logger.Debug("sqlite: search chunks keyword ok", "returned", len(results), "duration", time.Since(start))
	return results, rows.Err()
}

// GetChunksByDocument returns all chunks belonging to a specific document,
// including their embeddings. This implements ingest.DocumentChunkLister.
// When the in-memory vector index is loaded, embeddings are sourced from
// memory instead of deserializing blobs — significantly faster for cross-doc extraction.
func (s *Store) GetChunksByDocument(ctx context.Context, docID string) ([]oasis.Chunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get chunks by document", "doc_id", docID)

	// Check if in-memory index is available for fast embedding lookup.
	// When available, skip the embedding column entirely to avoid copying
	// large blobs from SQLite only to discard them.
	s.vecMu.RLock()
	useVecIndex := s.vecReady
	s.vecMu.RUnlock()

	var q string
	if useVecIndex {
		q = `SELECT id, document_id, parent_id, content, chunk_index, metadata
		     FROM chunks WHERE document_id = ? ORDER BY chunk_index`
	} else {
		q = `SELECT id, document_id, parent_id, content, chunk_index, embedding, metadata
		     FROM chunks WHERE document_id = ? ORDER BY chunk_index`
	}

	rows, err := s.db.QueryContext(ctx, q, docID)
	if err != nil {
		return nil, fmt.Errorf("get chunks by document: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var metaJSON sql.NullString

		if useVecIndex {
			if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON); err != nil {
				return nil, fmt.Errorf("scan chunk: %w", err)
			}
			s.vecMu.RLock()
			if entry, ok := s.vecIndex[c.ID]; ok {
				c.Embedding = entry.embedding
			}
			s.vecMu.RUnlock()
		} else {
			var embBlob []byte
			if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &embBlob, &metaJSON); err != nil {
				return nil, fmt.Errorf("scan chunk: %w", err)
			}
			if embBlob != nil {
				c.Embedding, _ = deserializeEmbedding(embBlob)
			}
		}

		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		chunks = append(chunks, c)
	}
	s.logger.Debug("sqlite: get chunks by document ok", "doc_id", docID, "count", len(chunks), "duration", time.Since(start))
	return chunks, rows.Err()
}

// GetDocumentsByIDs returns documents matching the given IDs.
func (s *Store) GetDocumentsByIDs(ctx context.Context, ids []string) ([]oasis.Document, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: get documents by ids", "count", len(ids))

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT id, title, source, content, created_at FROM documents WHERE id IN (%s)`,
		strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get documents by ids: %w", err)
	}
	defer rows.Close()

	var docs []oasis.Document
	for rows.Next() {
		var d oasis.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.Content, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		docs = append(docs, d)
	}
	s.logger.Debug("sqlite: get documents by ids ok", "requested", len(ids), "returned", len(docs), "duration", time.Since(start))
	return docs, rows.Err()
}

// GetChunksByIDs returns chunks matching the given IDs.
func (s *Store) GetChunksByIDs(ctx context.Context, ids []string) ([]oasis.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: get chunks by ids", "count", len(ids))

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(`SELECT id, document_id, parent_id, content, chunk_index, metadata FROM chunks WHERE id IN (%s)`,
		strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get chunks by ids: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var metaJSON sql.NullString
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		chunks = append(chunks, c)
	}
	s.logger.Debug("sqlite: get chunks by ids ok", "requested", len(ids), "returned", len(chunks), "duration", time.Since(start))
	return chunks, rows.Err()
}
