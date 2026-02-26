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

// StoreDocument inserts a document and all its chunks in a single transaction.
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

	for _, chunk := range chunks {
		var embJSON *string
		if len(chunk.Embedding) > 0 {
			v := serializeEmbedding(chunk.Embedding)
			embJSON = &v
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
		_, err = tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, embJSON, metaJSON,
		)
		if err != nil {
			s.logger.Error("sqlite: insert chunk failed", "chunk_id", chunk.ID, "doc_id", doc.ID, "error", err)
			return fmt.Errorf("insert chunk: %w", err)
		}

		// Keep FTS index in sync.
		_, _ = tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE chunk_id = ?`, chunk.ID)
		if _, err2 := tx.ExecContext(ctx, `INSERT INTO chunks_fts(chunk_id, content) VALUES (?, ?)`, chunk.ID, chunk.Content); err2 != nil {
			return fmt.Errorf("insert chunk fts: %w", err2)
		}
	}

	if err := tx.Commit(); err != nil {
		s.logger.Error("sqlite: store document commit failed", "id", doc.ID, "error", err)
		return fmt.Errorf("commit tx: %w", err)
	}
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
	s.logger.Debug("sqlite: delete document ok", "id", id, "duration", time.Since(start))
	return nil
}

// SearchChunks performs brute-force cosine similarity search over chunks.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search chunks", "top_k", topK, "embedding_dim", len(embedding), "filters", len(filters))

	whereExtra, filterArgs, needsDocJoin := buildChunkFilters(filters)

	var query string
	if needsDocJoin {
		query = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.embedding, c.metadata
			FROM chunks c JOIN documents d ON d.id = c.document_id
			WHERE c.embedding IS NOT NULL` + whereExtra
	} else {
		query = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.embedding, c.metadata
			FROM chunks c WHERE c.embedding IS NOT NULL` + whereExtra
	}

	rows, err := s.db.QueryContext(ctx, query, filterArgs...)
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredChunk
	scanned := 0

	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var embJSON string
		var metaJSON sql.NullString
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &embJSON, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		scanned++
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if metaJSON.Valid {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal([]byte(metaJSON.String), c.Metadata)
		}
		stored, err := deserializeEmbedding(embJSON)
		if err != nil {
			continue
		}
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: cosineSimilarity(embedding, stored)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	s.logger.Debug("sqlite: search chunks ok", "scanned", scanned, "returned", len(results), "duration", time.Since(start))
	return results, nil
}

// SearchChunksKeyword performs full-text keyword search over document chunks
// using SQLite FTS5. Results are sorted by relevance (FTS5 rank).
func (s *Store) SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search chunks keyword", "query", query, "top_k", topK, "filters", len(filters))

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
func (s *Store) GetChunksByDocument(ctx context.Context, docID string) ([]oasis.Chunk, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get chunks by document", "doc_id", docID)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, document_id, parent_id, content, chunk_index, embedding, metadata
		 FROM chunks WHERE document_id = ? ORDER BY chunk_index`, docID)
	if err != nil {
		return nil, fmt.Errorf("get chunks by document: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID sql.NullString
		var embJSON sql.NullString
		var metaJSON sql.NullString
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &embJSON, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if parentID.Valid {
			c.ParentID = parentID.String
		}
		if embJSON.Valid {
			c.Embedding, _ = deserializeEmbedding(embJSON.String)
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
