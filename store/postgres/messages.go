package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevindra/oasis"
)

// --- Messages ---

// StoreMessage inserts or replaces a message.
func (s *Store) StoreMessage(ctx context.Context, msg oasis.Message) error {
	start := time.Now()
	s.logger.Debug("postgres: store message", "id", msg.ID, "thread_id", msg.ThreadID, "role", msg.Role, "has_embedding", len(msg.Embedding) > 0)
	var metaJSON *string
	if len(msg.Metadata) > 0 {
		data, _ := json.Marshal(msg.Metadata)
		v := string(data)
		metaJSON = &v
	}

	if len(msg.Embedding) > 0 {
		embStr := serializeEmbedding(msg.Embedding)
		_, err := s.pool.Exec(ctx,
			`INSERT INTO messages (id, thread_id, role, content, embedding, metadata, created_at)
			 VALUES ($1, $2, $3, $4, $5::vector, $6::jsonb, $7)
			 ON CONFLICT (id) DO UPDATE SET
			   thread_id = EXCLUDED.thread_id,
			   role = EXCLUDED.role,
			   content = EXCLUDED.content,
			   embedding = EXCLUDED.embedding,
			   metadata = EXCLUDED.metadata,
			   created_at = EXCLUDED.created_at`,
			msg.ID, msg.ThreadID, msg.Role, msg.Content, embStr, metaJSON, msg.CreatedAt)
		if err != nil {
			s.logger.Error("postgres: store message failed", "id", msg.ID, "error", err, "duration", time.Since(start))
			return fmt.Errorf("postgres: store message: %w", err)
		}
		s.logger.Debug("postgres: store message ok", "id", msg.ID, "duration", time.Since(start))
		return nil
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO messages (id, thread_id, role, content, embedding, metadata, created_at)
		 VALUES ($1, $2, $3, $4, NULL, $5::jsonb, $6)
		 ON CONFLICT (id) DO UPDATE SET
		   thread_id = EXCLUDED.thread_id,
		   role = EXCLUDED.role,
		   content = EXCLUDED.content,
		   embedding = NULL,
		   metadata = EXCLUDED.metadata,
		   created_at = EXCLUDED.created_at`,
		msg.ID, msg.ThreadID, msg.Role, msg.Content, metaJSON, msg.CreatedAt)
	if err != nil {
		s.logger.Error("postgres: store message failed", "id", msg.ID, "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: store message: %w", err)
	}
	s.logger.Debug("postgres: store message ok", "id", msg.ID, "duration", time.Since(start))
	return nil
}

// GetMessages returns the most recent messages for a thread,
// ordered chronologically (oldest first).
func (s *Store) GetMessages(ctx context.Context, threadID string, limit int) ([]oasis.Message, error) {
	start := time.Now()
	s.logger.Debug("postgres: get messages", "thread_id", threadID, "limit", limit)
	rows, err := s.pool.Query(ctx,
		`SELECT id, thread_id, role, content, metadata, created_at
		 FROM messages
		 WHERE thread_id = $1
		 ORDER BY created_at DESC, id DESC
		 LIMIT $2`,
		threadID, limit)
	if err != nil {
		s.logger.Error("postgres: get messages failed", "thread_id", threadID, "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: get messages: %w", err)
	}
	defer rows.Close()

	var messages []oasis.Message
	for rows.Next() {
		var m oasis.Message
		var metaJSON []byte
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &metaJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan message: %w", err)
		}
		if metaJSON != nil {
			_ = json.Unmarshal(metaJSON, &m.Metadata)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate messages: %w", err)
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	s.logger.Debug("postgres: get messages ok", "thread_id", threadID, "count", len(messages), "duration", time.Since(start))
	return messages, nil
}

// SearchMessages performs vector similarity search over messages
// using pgvector's cosine distance operator with HNSW index.
func (s *Store) SearchMessages(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredMessage, error) {
	start := time.Now()
	s.logger.Debug("postgres: search messages", "top_k", topK, "embedding_dim", len(embedding))
	embStr := serializeEmbedding(embedding)
	rows, err := s.pool.Query(ctx,
		`SELECT id, thread_id, role, content, metadata, created_at,
		        1 - (embedding <=> $1::vector) AS score
		 FROM messages
		 WHERE embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		embStr, topK)
	if err != nil {
		s.logger.Error("postgres: search messages failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: search messages: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredMessage
	for rows.Next() {
		var m oasis.Message
		var metaJSON []byte
		var score float32
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &metaJSON, &m.CreatedAt, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan message: %w", err)
		}
		if metaJSON != nil {
			_ = json.Unmarshal(metaJSON, &m.Metadata)
		}
		results = append(results, oasis.ScoredMessage{Message: m, Score: score})
	}
	s.logger.Debug("postgres: search messages ok", "count", len(results), "duration", time.Since(start))
	return results, rows.Err()
}
