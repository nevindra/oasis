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

// --- Skills ---

func (s *Store) CreateSkill(ctx context.Context, skill oasis.Skill) error {
	start := time.Now()
	s.logger.Debug("sqlite: create skill", "id", skill.ID, "name", skill.Name, "has_embedding", len(skill.Embedding) > 0)

	var toolsJSON *string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		v := string(data)
		toolsJSON = &v
	}
	var tagsJSON *string
	if len(skill.Tags) > 0 {
		data, _ := json.Marshal(skill.Tags)
		v := string(data)
		tagsJSON = &v
	}
	var refsJSON *string
	if len(skill.References) > 0 {
		data, _ := json.Marshal(skill.References)
		v := string(data)
		refsJSON = &v
	}
	var embBlob []byte
	if len(skill.Embedding) > 0 {
		embBlob = serializeEmbedding(skill.Embedding)
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO skills (id, name, description, instructions, tools, model, tags, created_by, refs, embedding, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		skill.ID, skill.Name, skill.Description, skill.Instructions,
		toolsJSON, skill.Model, tagsJSON, skill.CreatedBy, refsJSON, embBlob, skill.CreatedAt, skill.UpdatedAt)
	if err != nil {
		s.logger.Error("sqlite: create skill failed", "id", skill.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: create skill ok", "id", skill.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) GetSkill(ctx context.Context, id string) (oasis.Skill, error) {
	start := time.Now()
	s.logger.Debug("sqlite: get skill", "id", id)

	var sk oasis.Skill
	var tools, model, tags, createdBy, refs sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, created_at, updated_at
		 FROM skills WHERE id = ?`, id,
	).Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &createdBy, &refs, &sk.CreatedAt, &sk.UpdatedAt)
	if err != nil {
		s.logger.Error("sqlite: get skill failed", "id", id, "error", err, "duration", time.Since(start))
		return oasis.Skill{}, fmt.Errorf("get skill: %w", err)
	}
	if tools.Valid {
		_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
	}
	if model.Valid {
		sk.Model = model.String
	}
	if tags.Valid {
		_ = json.Unmarshal([]byte(tags.String), &sk.Tags)
	}
	if createdBy.Valid {
		sk.CreatedBy = createdBy.String
	}
	if refs.Valid {
		_ = json.Unmarshal([]byte(refs.String), &sk.References)
	}
	s.logger.Debug("sqlite: get skill ok", "id", id, "name", sk.Name, "duration", time.Since(start))
	return sk, nil
}

func (s *Store) ListSkills(ctx context.Context) ([]oasis.Skill, error) {
	start := time.Now()
	s.logger.Debug("sqlite: list skills")

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, created_at, updated_at
		 FROM skills ORDER BY created_at`)
	if err != nil {
		s.logger.Error("sqlite: list skills failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer rows.Close()

	var skills []oasis.Skill
	for rows.Next() {
		var sk oasis.Skill
		var tools, model, tags, createdBy, refs sql.NullString
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &createdBy, &refs, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		if tools.Valid {
			_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
		}
		if model.Valid {
			sk.Model = model.String
		}
		if tags.Valid {
			_ = json.Unmarshal([]byte(tags.String), &sk.Tags)
		}
		if createdBy.Valid {
			sk.CreatedBy = createdBy.String
		}
		if refs.Valid {
			_ = json.Unmarshal([]byte(refs.String), &sk.References)
		}
		skills = append(skills, sk)
	}
	s.logger.Debug("sqlite: list skills ok", "count", len(skills), "duration", time.Since(start))
	return skills, rows.Err()
}

func (s *Store) UpdateSkill(ctx context.Context, skill oasis.Skill) error {
	start := time.Now()
	s.logger.Debug("sqlite: update skill", "id", skill.ID, "name", skill.Name, "has_embedding", len(skill.Embedding) > 0)

	var toolsJSON *string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		v := string(data)
		toolsJSON = &v
	}
	var tagsJSON *string
	if len(skill.Tags) > 0 {
		data, _ := json.Marshal(skill.Tags)
		v := string(data)
		tagsJSON = &v
	}
	var refsJSON *string
	if len(skill.References) > 0 {
		data, _ := json.Marshal(skill.References)
		v := string(data)
		refsJSON = &v
	}
	var embBlob []byte
	if len(skill.Embedding) > 0 {
		embBlob = serializeEmbedding(skill.Embedding)
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE skills SET name=?, description=?, instructions=?, tools=?, model=?, tags=?, created_by=?, refs=?, embedding=?, updated_at=? WHERE id=?`,
		skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, tagsJSON, skill.CreatedBy, refsJSON, embBlob, skill.UpdatedAt, skill.ID)
	if err != nil {
		s.logger.Error("sqlite: update skill failed", "id", skill.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: update skill ok", "id", skill.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("sqlite: delete skill", "id", id)

	_, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE id=?`, id)
	if err != nil {
		s.logger.Error("sqlite: delete skill failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("sqlite: delete skill ok", "id", id, "duration", time.Since(start))
	return nil
}

func (s *Store) SearchSkills(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredSkill, error) {
	start := time.Now()
	s.logger.Debug("sqlite: search skills", "top_k", topK, "embedding_dim", len(embedding))

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, embedding, created_at, updated_at
		 FROM skills WHERE embedding IS NOT NULL`)
	if err != nil {
		s.logger.Error("sqlite: search skills failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("search skills: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredSkill
	scanned := 0

	for rows.Next() {
		var sk oasis.Skill
		var tools, model, tags, createdBy, refs sql.NullString
		var embBlob []byte
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &createdBy, &refs, &embBlob, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan skill: %w", err)
		}
		scanned++
		if tools.Valid {
			_ = json.Unmarshal([]byte(tools.String), &sk.Tools)
		}
		if model.Valid {
			sk.Model = model.String
		}
		if tags.Valid {
			_ = json.Unmarshal([]byte(tags.String), &sk.Tags)
		}
		if createdBy.Valid {
			sk.CreatedBy = createdBy.String
		}
		if refs.Valid {
			_ = json.Unmarshal([]byte(refs.String), &sk.References)
		}
		stored, err := deserializeEmbedding(embBlob)
		if err != nil {
			continue
		}
		results = append(results, oasis.ScoredSkill{Skill: sk, Score: oasis.CosineSimilarity(embedding, stored)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skills: %w", err)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if len(results) > topK {
		results = results[:topK]
	}
	s.logger.Debug("sqlite: search skills ok", "scanned", scanned, "returned", len(results), "duration", time.Since(start))
	return results, nil
}
