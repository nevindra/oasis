package postgres

import (
	"context"
	"encoding/json"
	"fmt"
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

// buildChunkFiltersPg translates ChunkFilter values into Postgres WHERE clauses.
// startParam is the next $N placeholder number.
func buildChunkFiltersPg(filters []oasis.ChunkFilter, startParam int) (string, []any, bool) {
	if len(filters) == 0 {
		return "", nil, false
	}
	var clauses []string
	var args []any
	needsDocJoin := false
	p := startParam

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
					placeholders[i] = fmt.Sprintf("$%d", p)
					p++
					args = append(args, id)
				}
				clauses = append(clauses, "c.document_id IN ("+strings.Join(placeholders, ",")+")")
			} else if f.Op == oasis.OpEq {
				clauses = append(clauses, fmt.Sprintf("c.document_id = $%d", p))
				p++
				args = append(args, f.Value)
			} else if f.Op == oasis.OpNeq {
				clauses = append(clauses, fmt.Sprintf("c.document_id != $%d", p))
				p++
				args = append(args, f.Value)
			}

		case f.Field == "source":
			if f.Op != oasis.OpEq {
				continue
			}
			needsDocJoin = true
			clauses = append(clauses, fmt.Sprintf("d.source = $%d", p))
			p++
			args = append(args, f.Value)

		case f.Field == "created_at":
			needsDocJoin = true
			if f.Op == oasis.OpGt {
				clauses = append(clauses, fmt.Sprintf("d.created_at > $%d", p))
				p++
				args = append(args, f.Value)
			} else if f.Op == oasis.OpLt {
				clauses = append(clauses, fmt.Sprintf("d.created_at < $%d", p))
				p++
				args = append(args, f.Value)
			}

		case strings.HasPrefix(f.Field, "meta."):
			key := strings.TrimPrefix(f.Field, "meta.")
			if !safeMetaKey(key) {
				continue
			}
			clauses = append(clauses, fmt.Sprintf("c.metadata->>'%s' = $%d", key, p))
			p++
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
	s.logger.Debug("postgres: store document", "id", doc.ID, "title", doc.Title, "chunks", len(chunks))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx,
		`INSERT INTO documents (id, title, source, content, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (id) DO UPDATE SET
		   title = EXCLUDED.title,
		   source = EXCLUDED.source,
		   content = EXCLUDED.content,
		   created_at = EXCLUDED.created_at`,
		doc.ID, doc.Title, doc.Source, doc.Content, doc.CreatedAt)
	if err != nil {
		s.logger.Error("postgres: store document failed", "id", doc.ID, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: insert document: %w", err)
	}

	for _, chunk := range chunks {
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

		if len(chunk.Embedding) > 0 {
			embStr := serializeEmbedding(chunk.Embedding)
			_, err = tx.Exec(ctx,
				`INSERT INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata)
				 VALUES ($1, $2, $3, $4, $5, $6::vector, $7::jsonb)
				 ON CONFLICT (id) DO UPDATE SET
				   document_id = EXCLUDED.document_id,
				   parent_id = EXCLUDED.parent_id,
				   content = EXCLUDED.content,
				   chunk_index = EXCLUDED.chunk_index,
				   embedding = EXCLUDED.embedding,
				   metadata = EXCLUDED.metadata`,
				chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, embStr, metaJSON)
		} else {
			_, err = tx.Exec(ctx,
				`INSERT INTO chunks (id, document_id, parent_id, content, chunk_index, embedding, metadata)
				 VALUES ($1, $2, $3, $4, $5, NULL, $6::jsonb)
				 ON CONFLICT (id) DO UPDATE SET
				   document_id = EXCLUDED.document_id,
				   parent_id = EXCLUDED.parent_id,
				   content = EXCLUDED.content,
				   chunk_index = EXCLUDED.chunk_index,
				   embedding = NULL,
				   metadata = EXCLUDED.metadata`,
				chunk.ID, chunk.DocumentID, parentID, chunk.Content, chunk.ChunkIndex, metaJSON)
		}
		if err != nil {
			s.logger.Error("postgres: store document chunk failed", "doc_id", doc.ID, "chunk_id", chunk.ID, "error", err, "duration", time.Since(start))
			return fmt.Errorf("postgres: insert chunk: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit tx: %w", err)
	}
	s.logger.Debug("postgres: store document ok", "id", doc.ID, "chunks", len(chunks), "duration", time.Since(start))
	return nil
}

// ListDocuments returns all documents ordered by most recently created first.
func (s *Store) ListDocuments(ctx context.Context, limit int) ([]oasis.Document, error) {
	start := time.Now()
	s.logger.Debug("postgres: list documents", "limit", limit)
	rows, err := s.pool.Query(ctx,
		`SELECT id, title, source, content, created_at
		 FROM documents
		 ORDER BY created_at DESC
		 LIMIT $1`,
		limit)
	if err != nil {
		s.logger.Error("postgres: list documents failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: list documents: %w", err)
	}
	defer rows.Close()

	var docs []oasis.Document
	for rows.Next() {
		var d oasis.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.Content, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan document: %w", err)
		}
		docs = append(docs, d)
	}
	s.logger.Debug("postgres: list documents ok", "count", len(docs), "duration", time.Since(start))
	return docs, rows.Err()
}

// ListDocumentMeta returns all documents without the Content field, ordered by
// creation time (newest first). Use this instead of ListDocuments when only
// ID, Title, Source, and CreatedAt are needed to avoid loading large document
// bodies into memory.
func (s *Store) ListDocumentMeta(ctx context.Context, limit int) ([]oasis.Document, error) {
	start := time.Now()
	s.logger.Debug("postgres: list document meta", "limit", limit)
	rows, err := s.pool.Query(ctx,
		`SELECT id, title, source, created_at
		 FROM documents
		 ORDER BY created_at DESC
		 LIMIT $1`,
		limit)
	if err != nil {
		s.logger.Error("postgres: list document meta failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: list document meta: %w", err)
	}
	defer rows.Close()

	var docs []oasis.Document
	for rows.Next() {
		var d oasis.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan document meta: %w", err)
		}
		docs = append(docs, d)
	}
	s.logger.Debug("postgres: list document meta ok", "count", len(docs), "duration", time.Since(start))
	return docs, rows.Err()
}

// DeleteDocument removes a document and all its chunks in a single transaction.
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("postgres: delete document", "id", id)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM chunk_edges WHERE source_id IN (SELECT id FROM chunks WHERE document_id = $1) OR target_id IN (SELECT id FROM chunks WHERE document_id = $1)`, id); err != nil {
		s.logger.Error("postgres: delete document edges failed", "id", id, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: delete document edges: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chunks WHERE document_id = $1`, id); err != nil {
		s.logger.Error("postgres: delete document chunks failed", "id", id, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: delete document chunks: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM documents WHERE id = $1`, id); err != nil {
		s.logger.Error("postgres: delete document failed", "id", id, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: delete document: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.logger.Debug("postgres: delete document ok", "id", id, "duration", time.Since(start))
	return nil
}

// SearchChunks performs vector similarity search over document chunks
// using pgvector's cosine distance operator with HNSW index.
func (s *Store) SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	start := time.Now()
	s.logger.Debug("postgres: search chunks", "top_k", topK, "embedding_dim", len(embedding), "filters", len(filters))
	embStr := serializeEmbedding(embedding)
	whereExtra, filterArgs, needsDocJoin := buildChunkFiltersPg(filters, 3) // $1=embedding, $2=topK

	var q string
	if needsDocJoin {
		q = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata,
		        1 - (c.embedding <=> $1::vector) AS score
		 FROM chunks c JOIN documents d ON d.id = c.document_id
		 WHERE c.embedding IS NOT NULL` + whereExtra + `
		 ORDER BY c.embedding <=> $1::vector
		 LIMIT $2`
	} else {
		q = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata,
		        1 - (c.embedding <=> $1::vector) AS score
		 FROM chunks c
		 WHERE c.embedding IS NOT NULL` + whereExtra + `
		 ORDER BY c.embedding <=> $1::vector
		 LIMIT $2`
	}

	allArgs := []any{embStr, topK}
	allArgs = append(allArgs, filterArgs...)

	rows, err := s.pool.Query(ctx, q, allArgs...)
	if err != nil {
		s.logger.Error("postgres: search chunks failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: search chunks: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredChunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID *string
		var metaJSON []byte
		var score float32
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan chunk: %w", err)
		}
		if parentID != nil {
			c.ParentID = *parentID
		}
		if metaJSON != nil {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal(metaJSON, c.Metadata)
		}
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: score})
	}
	s.logger.Debug("postgres: search chunks ok", "count", len(results), "duration", time.Since(start))
	return results, rows.Err()
}

// SearchChunksKeyword performs full-text keyword search over document chunks
// using PostgreSQL tsvector/tsquery with a GIN index.
func (s *Store) SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...oasis.ChunkFilter) ([]oasis.ScoredChunk, error) {
	start := time.Now()
	s.logger.Debug("postgres: search chunks keyword", "query", query, "top_k", topK, "filters", len(filters))
	whereExtra, filterArgs, needsDocJoin := buildChunkFiltersPg(filters, 3) // $1=query, $2=topK

	var q string
	if needsDocJoin {
		q = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata,
		        ts_rank(to_tsvector('english', c.content), plainto_tsquery('english', $1)) AS score
		 FROM chunks c JOIN documents d ON d.id = c.document_id
		 WHERE to_tsvector('english', c.content) @@ plainto_tsquery('english', $1)` + whereExtra + `
		 ORDER BY score DESC
		 LIMIT $2`
	} else {
		q = `SELECT c.id, c.document_id, c.parent_id, c.content, c.chunk_index, c.metadata,
		        ts_rank(to_tsvector('english', c.content), plainto_tsquery('english', $1)) AS score
		 FROM chunks c
		 WHERE to_tsvector('english', c.content) @@ plainto_tsquery('english', $1)` + whereExtra + `
		 ORDER BY score DESC
		 LIMIT $2`
	}

	allArgs := []any{query, topK}
	allArgs = append(allArgs, filterArgs...)

	rows, err := s.pool.Query(ctx, q, allArgs...)
	if err != nil {
		s.logger.Error("postgres: search chunks keyword failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: keyword search: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredChunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID *string
		var metaJSON []byte
		var score float32
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan chunk: %w", err)
		}
		if parentID != nil {
			c.ParentID = *parentID
		}
		if metaJSON != nil {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal(metaJSON, c.Metadata)
		}
		results = append(results, oasis.ScoredChunk{Chunk: c, Score: score})
	}
	s.logger.Debug("postgres: search chunks keyword ok", "count", len(results), "duration", time.Since(start))
	return results, rows.Err()
}

// GetChunksByDocument returns all chunks belonging to a specific document,
// including their embeddings. This implements ingest.DocumentChunkLister.
func (s *Store) GetChunksByDocument(ctx context.Context, docID string) ([]oasis.Chunk, error) {
	start := time.Now()
	s.logger.Debug("postgres: get chunks by document", "doc_id", docID)
	rows, err := s.pool.Query(ctx,
		`SELECT id, document_id, parent_id, content, chunk_index, embedding::text, metadata
		 FROM chunks WHERE document_id = $1 ORDER BY chunk_index`, docID)
	if err != nil {
		s.logger.Error("postgres: get chunks by document failed", "doc_id", docID, "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: get chunks by document: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID *string
		var embStr *string
		var metaJSON []byte
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &embStr, &metaJSON); err != nil {
			return nil, fmt.Errorf("postgres: scan chunk: %w", err)
		}
		if parentID != nil {
			c.ParentID = *parentID
		}
		if embStr != nil {
			c.Embedding = deserializeEmbedding(*embStr)
		}
		if metaJSON != nil {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal(metaJSON, c.Metadata)
		}
		chunks = append(chunks, c)
	}
	s.logger.Debug("postgres: get chunks by document ok", "doc_id", docID, "count", len(chunks), "duration", time.Since(start))
	return chunks, rows.Err()
}

// GetDocumentsByIDs returns documents matching the given IDs.
func (s *Store) GetDocumentsByIDs(ctx context.Context, ids []string) ([]oasis.Document, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("postgres: get documents by ids", "count", len(ids))

	rows, err := s.pool.Query(ctx,
		`SELECT id, title, source, content, created_at FROM documents WHERE id = ANY($1)`, ids)
	if err != nil {
		s.logger.Error("postgres: get documents by ids failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: get documents by ids: %w", err)
	}
	defer rows.Close()

	var docs []oasis.Document
	for rows.Next() {
		var d oasis.Document
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.Content, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan document: %w", err)
		}
		docs = append(docs, d)
	}
	s.logger.Debug("postgres: get documents by ids ok", "count", len(docs), "duration", time.Since(start))
	return docs, rows.Err()
}

// GetChunksByIDs returns chunks matching the given IDs.
func (s *Store) GetChunksByIDs(ctx context.Context, ids []string) ([]oasis.Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("postgres: get chunks by ids", "count", len(ids))

	rows, err := s.pool.Query(ctx,
		`SELECT id, document_id, parent_id, content, chunk_index, metadata
		 FROM chunks WHERE id = ANY($1)`, ids)
	if err != nil {
		s.logger.Error("postgres: get chunks by ids failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: get chunks by ids: %w", err)
	}
	defer rows.Close()

	var chunks []oasis.Chunk
	for rows.Next() {
		var c oasis.Chunk
		var parentID *string
		var metaJSON []byte
		if err := rows.Scan(&c.ID, &c.DocumentID, &parentID, &c.Content, &c.ChunkIndex, &metaJSON); err != nil {
			return nil, fmt.Errorf("postgres: scan chunk: %w", err)
		}
		if parentID != nil {
			c.ParentID = *parentID
		}
		if metaJSON != nil {
			c.Metadata = &oasis.ChunkMeta{}
			_ = json.Unmarshal(metaJSON, c.Metadata)
		}
		chunks = append(chunks, c)
	}
	s.logger.Debug("postgres: get chunks by ids ok", "count", len(chunks), "duration", time.Since(start))
	return chunks, rows.Err()
}
