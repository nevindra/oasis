package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nevindra/oasis"
)

// --- GraphStore ---

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

	for _, e := range edges {
		_, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO chunk_edges (id, source_id, target_id, relation, weight, description)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			e.ID, e.SourceID, e.TargetID, string(e.Relation), e.Weight, e.Description,
		)
		if err != nil {
			s.logger.Error("sqlite: store edge failed", "id", e.ID, "error", err)
			return fmt.Errorf("store edge: %w", err)
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

