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

	ids := make([]string, len(edges))
	sourceIDs := make([]string, len(edges))
	targetIDs := make([]string, len(edges))
	relations := make([]string, len(edges))
	weights := make([]float32, len(edges))
	descriptions := make([]string, len(edges))
	for i, e := range edges {
		ids[i] = e.ID
		sourceIDs[i] = e.SourceID
		targetIDs[i] = e.TargetID
		relations[i] = string(e.Relation)
		weights[i] = e.Weight
		descriptions[i] = e.Description
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO chunk_edges (id, source_id, target_id, relation, weight, description)
		 SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::real[], $6::text[])
		 ON CONFLICT (source_id, target_id, relation) DO UPDATE SET weight = EXCLUDED.weight, description = EXCLUDED.description`,
		ids, sourceIDs, targetIDs, relations, weights, descriptions,
	)
	if err != nil {
		s.logger.Error("postgres: store edges failed", "count", len(edges), "error", err, "duration", time.Since(start))
		return fmt.Errorf("postgres: store edges: %w", err)
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

// GetBothEdges returns both outgoing and incoming edges for the given chunk IDs
// in a single query. Implements oasis.BidirectionalGraphStore.
func (s *Store) GetBothEdges(ctx context.Context, chunkIDs []string) ([]oasis.ChunkEdge, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	start := time.Now()
	s.logger.Debug("postgres: get both edges", "chunk_count", len(chunkIDs))
	rows, err := s.pool.Query(ctx,
		`SELECT id, source_id, target_id, relation, weight, description FROM chunk_edges WHERE source_id = ANY($1) OR target_id = ANY($1)`,
		chunkIDs,
	)
	if err != nil {
		s.logger.Error("postgres: get both edges failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: get both edges: %w", err)
	}
	defer rows.Close()
	edges, err := scanEdgesPg(rows)
	s.logger.Debug("postgres: get both edges ok", "count", len(edges), "duration", time.Since(start))
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
