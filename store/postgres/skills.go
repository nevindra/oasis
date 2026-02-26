package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nevindra/oasis"
)

// --- Skills ---

func (s *Store) CreateSkill(ctx context.Context, skill oasis.Skill) error {
	start := time.Now()
	s.logger.Debug("postgres: create skill", "id", skill.ID, "name", skill.Name)
	var toolsJSON string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		toolsJSON = string(data)
	}
	var tagsJSON string
	if len(skill.Tags) > 0 {
		data, _ := json.Marshal(skill.Tags)
		tagsJSON = string(data)
	}
	var refsJSON string
	if len(skill.References) > 0 {
		data, _ := json.Marshal(skill.References)
		refsJSON = string(data)
	}

	if len(skill.Embedding) > 0 {
		embStr := serializeEmbedding(skill.Embedding)
		_, err := s.pool.Exec(ctx,
			`INSERT INTO skills (id, name, description, instructions, tools, model, tags, created_by, refs, embedding, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::vector, $11, $12)`,
			skill.ID, skill.Name, skill.Description, skill.Instructions,
			toolsJSON, skill.Model, tagsJSON, skill.CreatedBy, refsJSON, embStr, skill.CreatedAt, skill.UpdatedAt)
		if err != nil {
			s.logger.Error("postgres: create skill failed", "id", skill.ID, "error", err, "duration", time.Since(start))
			return err
		}
		s.logger.Debug("postgres: create skill ok", "id", skill.ID, "duration", time.Since(start))
		return nil
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO skills (id, name, description, instructions, tools, model, tags, created_by, refs, embedding, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL, $10, $11)`,
		skill.ID, skill.Name, skill.Description, skill.Instructions,
		toolsJSON, skill.Model, tagsJSON, skill.CreatedBy, refsJSON, skill.CreatedAt, skill.UpdatedAt)
	if err != nil {
		s.logger.Error("postgres: create skill failed", "id", skill.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("postgres: create skill ok", "id", skill.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) GetSkill(ctx context.Context, id string) (oasis.Skill, error) {
	start := time.Now()
	s.logger.Debug("postgres: get skill", "id", id)
	var sk oasis.Skill
	var tools, model, tags, refs string
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, created_at, updated_at
		 FROM skills WHERE id = $1`, id,
	).Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &sk.CreatedBy, &refs, &sk.CreatedAt, &sk.UpdatedAt)
	if err != nil {
		s.logger.Error("postgres: get skill failed", "id", id, "error", err, "duration", time.Since(start))
		return oasis.Skill{}, fmt.Errorf("postgres: get skill: %w", err)
	}
	if tools != "" {
		_ = json.Unmarshal([]byte(tools), &sk.Tools)
	}
	if tags != "" {
		_ = json.Unmarshal([]byte(tags), &sk.Tags)
	}
	if refs != "" {
		_ = json.Unmarshal([]byte(refs), &sk.References)
	}
	sk.Model = model
	s.logger.Debug("postgres: get skill ok", "id", id, "duration", time.Since(start))
	return sk, nil
}

func (s *Store) ListSkills(ctx context.Context) ([]oasis.Skill, error) {
	start := time.Now()
	s.logger.Debug("postgres: list skills")
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, created_at, updated_at
		 FROM skills ORDER BY created_at`)
	if err != nil {
		s.logger.Error("postgres: list skills failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: list skills: %w", err)
	}
	defer rows.Close()

	var skills []oasis.Skill
	for rows.Next() {
		var sk oasis.Skill
		var tools, model, tags, refs string
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &sk.CreatedBy, &refs, &sk.CreatedAt, &sk.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan skill: %w", err)
		}
		if tools != "" {
			_ = json.Unmarshal([]byte(tools), &sk.Tools)
		}
		if tags != "" {
			_ = json.Unmarshal([]byte(tags), &sk.Tags)
		}
		if refs != "" {
			_ = json.Unmarshal([]byte(refs), &sk.References)
		}
		sk.Model = model
		skills = append(skills, sk)
	}
	s.logger.Debug("postgres: list skills ok", "count", len(skills), "duration", time.Since(start))
	return skills, rows.Err()
}

func (s *Store) UpdateSkill(ctx context.Context, skill oasis.Skill) error {
	start := time.Now()
	s.logger.Debug("postgres: update skill", "id", skill.ID, "name", skill.Name)
	var toolsJSON string
	if len(skill.Tools) > 0 {
		data, _ := json.Marshal(skill.Tools)
		toolsJSON = string(data)
	}
	var tagsJSON string
	if len(skill.Tags) > 0 {
		data, _ := json.Marshal(skill.Tags)
		tagsJSON = string(data)
	}
	var refsJSON string
	if len(skill.References) > 0 {
		data, _ := json.Marshal(skill.References)
		refsJSON = string(data)
	}

	if len(skill.Embedding) > 0 {
		embStr := serializeEmbedding(skill.Embedding)
		_, err := s.pool.Exec(ctx,
			`UPDATE skills SET name=$1, description=$2, instructions=$3, tools=$4, model=$5, tags=$6, created_by=$7, refs=$8, embedding=$9::vector, updated_at=$10 WHERE id=$11`,
			skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, tagsJSON, skill.CreatedBy, refsJSON, embStr, skill.UpdatedAt, skill.ID)
		if err != nil {
			s.logger.Error("postgres: update skill failed", "id", skill.ID, "error", err, "duration", time.Since(start))
			return err
		}
		s.logger.Debug("postgres: update skill ok", "id", skill.ID, "duration", time.Since(start))
		return nil
	}

	_, err := s.pool.Exec(ctx,
		`UPDATE skills SET name=$1, description=$2, instructions=$3, tools=$4, model=$5, tags=$6, created_by=$7, refs=$8, embedding=NULL, updated_at=$9 WHERE id=$10`,
		skill.Name, skill.Description, skill.Instructions, toolsJSON, skill.Model, tagsJSON, skill.CreatedBy, refsJSON, skill.UpdatedAt, skill.ID)
	if err != nil {
		s.logger.Error("postgres: update skill failed", "id", skill.ID, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("postgres: update skill ok", "id", skill.ID, "duration", time.Since(start))
	return nil
}

func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	start := time.Now()
	s.logger.Debug("postgres: delete skill", "id", id)
	_, err := s.pool.Exec(ctx, `DELETE FROM skills WHERE id=$1`, id)
	if err != nil {
		s.logger.Error("postgres: delete skill failed", "id", id, "error", err, "duration", time.Since(start))
		return err
	}
	s.logger.Debug("postgres: delete skill ok", "id", id, "duration", time.Since(start))
	return nil
}

// SearchSkills performs vector similarity search over stored skills
// using pgvector's cosine distance operator with HNSW index.
func (s *Store) SearchSkills(ctx context.Context, embedding []float32, topK int) ([]oasis.ScoredSkill, error) {
	start := time.Now()
	s.logger.Debug("postgres: search skills", "top_k", topK, "embedding_dim", len(embedding))
	embStr := serializeEmbedding(embedding)
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, description, instructions, tools, model, tags, created_by, refs, created_at, updated_at,
		        1 - (embedding <=> $1::vector) AS score
		 FROM skills
		 WHERE embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		embStr, topK)
	if err != nil {
		s.logger.Error("postgres: search skills failed", "error", err, "duration", time.Since(start))
		return nil, fmt.Errorf("postgres: search skills: %w", err)
	}
	defer rows.Close()

	var results []oasis.ScoredSkill
	for rows.Next() {
		var sk oasis.Skill
		var tools, model, tags, refs string
		var score float32
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Description, &sk.Instructions, &tools, &model, &tags, &sk.CreatedBy, &refs, &sk.CreatedAt, &sk.UpdatedAt, &score); err != nil {
			return nil, fmt.Errorf("postgres: scan skill: %w", err)
		}
		if tools != "" {
			_ = json.Unmarshal([]byte(tools), &sk.Tools)
		}
		if tags != "" {
			_ = json.Unmarshal([]byte(tags), &sk.Tags)
		}
		if refs != "" {
			_ = json.Unmarshal([]byte(refs), &sk.References)
		}
		sk.Model = model
		results = append(results, oasis.ScoredSkill{Skill: sk, Score: score})
	}
	s.logger.Debug("postgres: search skills ok", "count", len(results), "duration", time.Since(start))
	return results, rows.Err()
}
