package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/nevindra/oasis"
)

// StoreMessage inserts or replaces a message.
func (s *Store) StoreMessage(ctx context.Context, msg oasis.Message) error {
	start := time.Now()
	s.logger.Debug("sqlite: store message", "id", msg.ID, "thread_id", msg.ThreadID, "role", msg.Role, "has_embedding", len(msg.Embedding) > 0)

	var embJSON *string
	if len(msg.Embedding) > 0 {
		v := serializeEmbedding(msg.Embedding)
		embJSON = &v
	}
	var metaJSON *string
	if len(msg.Metadata) > 0 {
		data, _ := json.Marshal(msg.Metadata)
		v := string(data)
		metaJSON = &v
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO messages (id, thread_id, role, content, embedding, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.ThreadID, msg.Role, msg.Content, embJSON, metaJSON, msg.CreatedAt,
	)
	if err != nil {
		s.logger.Error("sqlite: store message failed", "id", msg.ID, "error", err, "duration", time.Since(start))
		return fmt.Errorf("store message: %w", err)
	}
	s.logger.Debug("sqlite: store message ok", "id", msg.ID, "duration", time.Since(start))
	return nil
}

// GetMessages returns the most recent messages for a thread,
// ordered chronologically (oldest first).
func (s *Store) GetMessages(ctx context.Context, threadID string, limit int) ([]oasis.Message, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get messages", "thread_id", threadID, "limit", limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, thread_id, role, content, metadata, created_at
		 FROM messages
		 WHERE thread_id = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`,
		threadID, limit,
	)
	if err != nil {
		s.logger.Error("sqlite: get messages failed", "thread_id", threadID, "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer rows.Close()

	var messages []oasis.Message
	for rows.Next() {
		var m oasis.Message
		var metaJSON sql.NullString
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &metaJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		if metaJSON.Valid {
			_ = json.Unmarshal([]byte(metaJSON.String), &m.Metadata)
		}
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	// Reverse to chronological order (oldest first).
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	s.logger.Debug("sqlite: get messages ok", "thread_id", threadID, "count", len(messages), "duration", time.Since(start))
	return messages, nil
}

// SearchMessages performs brute-force cosine similarity search over messages.
func (s *Store) SearchMessages(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredMessage, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search messages", "top_k", topK, "embedding_dim", len(embedding))

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, thread_id, role, content, embedding, metadata, created_at
		 FROM messages WHERE embedding IS NOT NULL`,
	)
	if err != nil {
		s.logger.Error("sqlite: search messages failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredMessage
	scanned := 0

	for rows.Next() {
		var m oasis.Message
		var embJSON string
		var metaJSON sql.NullString
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.Role, &m.Content, &embJSON, &metaJSON, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		scanned++
		if metaJSON.Valid {
			_ = json.Unmarshal([]byte(metaJSON.String), &m.Metadata)
		}
		stored, err := deserializeEmbedding(embJSON)
		if err != nil {
			continue
		}
		results = append(results, oasis.ScoredMessage{Message: m, Score: cosineSimilarity(embedding, stored)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	s.logger.Debug("sqlite: search messages ok", "scanned", scanned, "returned", len(results), "duration", time.Since(start))
	return results, nil
}
