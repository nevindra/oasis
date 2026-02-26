package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nevindra/oasis"
)

// --- GraphStore ---

// storeEdgesBatchSize is the max rows per multi-value INSERT (SQLite has a
// default SQLITE_MAX_VARIABLE_NUMBER of 999; 6 params per row → 166 rows max).
const storeEdgesBatchSize = 150

func (s *Store) StoreEdges(ctx context.Context, edges []oasis.ChunkEdge) error {
	if len(edges) == 0 {
		return nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: store edges", "count", len(edges))

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for i := 0; i < len(edges); i += storeEdgesBatchSize {
		end := min(i+storeEdgesBatchSize, len(edges))
		batch := edges[i:end]

		var sb strings.Builder
		sb.WriteString(`INSERT INTO chunk_edges (id, source_id, target_id, relation, weight, description) VALUES `)
		args := make([]any, 0, len(batch)*6)
		for j, e := range batch {
			if j > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString("(?,?,?,?,?,?)")
			args = append(args, e.ID, e.SourceID, e.TargetID, string(e.Relation), e.Weight, e.Description)
		}
		sb.WriteString(` ON CONFLICT(source_id, target_id, relation) DO UPDATE SET weight = excluded.weight, description = excluded.description`)

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			s.logger.Error("sqlite: store edges batch failed", "batch_size", len(batch), "error", err)
			return fmt.Errorf("store edges batch: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		s.logger.Error("sqlite: store edges commit failed", "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: store edges ok", "count", len(edges), "duration", time.Since(start))
	return nil
}

func (s *Store) GetEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: get edges", "chunk_count", len(chunkIDs))

	placeholders := make([]string, len(chunkIDs))
	args := make([]any, len(chunkIDs))
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT id, source_id, target_id, relation, weight, description FROM chunk_edges WHERE source_id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	edges, err := s.scanEdges(ctx, query, args)
	if err != nil {
		s.logger.Error("sqlite: get edges failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: get edges ok", "returned", len(edges), "duration", time.Since(start))
	return edges, nil
}

func (s *Store) GetIncomingEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: get incoming edges", "chunk_count", len(chunkIDs))

	placeholders := make([]string, len(chunkIDs))
	args := make([]any, len(chunkIDs))
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := fmt.Sprintf(
		`SELECT id, source_id, target_id, relation, weight, description FROM chunk_edges WHERE target_id IN (%s)`,
		strings.Join(placeholders, ","),
	)
	edges, err := s.scanEdges(ctx, query, args)
	if err != nil {
		s.logger.Error("sqlite: get incoming edges failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: get incoming edges ok", "returned", len(edges), "duration", time.Since(start))
	return edges, nil
}

// GetBothEdges returns both outgoing and incoming edges for the given chunk IDs
// in a single query. Implements oasis.BidirectionalGraphStore.
func (s *Store) GetBothEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("sqlite: get both edges", "chunk_count", len(chunkIDs))

	placeholders := make([]string, len(chunkIDs))
	args := make([]any, 0, len(chunkIDs)*2)
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	ph := strings.Join(placeholders, ",")
	// Duplicate args for the OR clause.
	for _, id := range chunkIDs {
		args = append(args, id)
	}
	query := fmt.Sprintf(
		`SELECT id, source_id, target_id, relation, weight, description FROM chunk_edges WHERE source_id IN (%s) OR target_id IN (%s)`,
		ph, ph,
	)
	edges, err := s.scanEdges(ctx, query, args)
	if err != nil {
		s.logger.Error("sqlite: get both edges failed", "error", err, "duration", time.Since(start))
		return nil, err
	}
	s.logger.Debug("sqlite: get both edges ok", "returned", len(edges), "duration", time.Since(start))
	return edges, nil
}

func (s *Store) PruneOrphanEdges(ctx context.Context) (int, error) {
	start := time.Now()
	s.logger.Debug("sqlite: prune orphan edges")

	result, err := s.db.ExecContext(ctx,
		`DELETE FROM chunk_edges WHERE source_id NOT IN (SELECT id FROM chunks) OR target_id NOT IN (SELECT id FROM chunks)`)
	if err != nil {
		s.logger.Error("sqlite: prune orphan edges failed", "error", err, "duration", time.Since(start))
		return 0, fmt.Errorf("prune orphan edges: %w", err)
	}
	n, _ := result.RowsAffected()
	s.logger.Debug("sqlite: prune orphan edges ok", "deleted", n, "duration", time.Since(start))
	return int(n), nil
}

func (s *Store) scanEdges(ctx context.Context, query string, args []any) ([]oasis.ChunkEdge, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query edges: %w", err)
	}
	defer rows.Close()

	var edges []oasis.ChunkEdge
	for rows.Next() {
		var e oasis.ChunkEdge
		var rel string
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &rel, &e.Weight, &e.Description); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		e.Relation = oasis.RelationType(rel)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}

