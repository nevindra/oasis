package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nevindra/oasis"
)

// --- GraphStore ---

func (s *Store) StoreEdges(ctx context.Context, edges []oasis.ChunkEdge) error {
	if len(edges) == 0 {
		return nil
	}
	start := time.Now()
	s.logger.Debug("postgres: store edges", "count", len(edges))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, e := range edges {
		_, err := tx.Exec(ctx,
			`INSERT INTO chunk_edges (id, source_id, target_id, relation, weight, description)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (source_id, target_id, relation) DO UPDATE SET weight = EXCLUDED.weight, description = EXCLUDED.description`,
			e.ID, e.SourceID, e.TargetID, string(e.Relation), e.Weight, e.Description,
		)
		if err != nil {
			s.logger.Error("postgres: store edge failed", "id", e.ID, "error", err, "duration", time.Since(start))
			return fmt.Errorf("postgres: store edge: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.logger.Debug("postgres: store edges ok", "count", len(edges), "duration", time.Since(start))
	return nil
}

func (s *Store) GetEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("postgres: get edges", "chunk_count", len(chunkIDs))
	rows, err := s.pool.Query(ctx,
		`SELECT id, source_id, target_id, relation, weight, description FROM chunk_edges WHERE source_id = ANY($1)`,
		chunkIDs,
	)
	if err != nil {
		s.logger.Error("postgres: get edges failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: get edges: %w", err)
	}
	defer rows.Close()
	edges, err := scanEdgesPg(rows)
	s.logger.Debug("postgres: get edges ok", "count", len(edges), "duration", time.Since(start))
	return edges, err
}

func (s *Store) GetIncomingEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("postgres: get incoming edges", "chunk_count", len(chunkIDs))
	rows, err := s.pool.Query(ctx,
		`SELECT id, source_id, target_id, relation, weight, description FROM chunk_edges WHERE target_id = ANY($1)`,
		chunkIDs,
	)
	if err != nil {
		s.logger.Error("postgres: get incoming edges failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: get incoming edges: %w", err)
	}
	defer rows.Close()
	edges, err := scanEdgesPg(rows)
	s.logger.Debug("postgres: get incoming edges ok", "count", len(edges), "duration", time.Since(start))
	return edges, err
}

func (s *Store) PruneOrphanEdges(ctx context.Context) (int, error) {
	start := time.Now()
	s.logger.Debug("postgres: prune orphan edges")
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM chunk_edges WHERE source_id NOT IN (SELECT id FROM chunks) OR target_id NOT IN (SELECT id FROM chunks)`)
	if err != nil {
		s.logger.Error("postgres: prune orphan edges failed", "error", err, "duration", time.Since(start))
		return 0, fmt.Errorf("postgres: prune orphan edges: %w", err)
	}
	n := int(tag.RowsAffected())
	s.logger.Debug("postgres: prune orphan edges ok", "deleted", n, "duration", time.Since(start))
	return n, nil
}

func scanEdgesPg(rows pgx.Rows) ([]oasis.ChunkEdge, error) {
	var edges []oasis.ChunkEdge
	for rows.Next() {
		var e oasis.ChunkEdge
		var rel string
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &rel, &e.Weight, &e.Description); err != nil {
			return nil, fmt.Errorf("postgres: scan edge: %w", err)
		}
		e.Relation = oasis.RelationType(rel)
		edges = append(edges, e)
	}
	return edges, rows.Err()
}
