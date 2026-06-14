package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nevindra/oasis/core"
)

// ItemStore is a PostgreSQL-backed implementation of core.MemoryItemStore.
// Embeddings are stored as JSONB; similarity is computed in-process using
// brute-force cosine similarity (sufficient for sub-100k row counts).
type ItemStore struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

var _ core.MemoryItemStore = (*ItemStore)(nil)

// NewItemStore constructs an ItemStore on the given *pgxpool.Pool.
func NewItemStore(pool *pgxpool.Pool, logger *slog.Logger) *ItemStore {
	if logger == nil {
		logger = nopLogger
	}
	return &ItemStore{pool: pool, logger: logger}
}

// Init creates the memory_items table and indexes if they don't already exist.
// Safe to call multiple times (all statements are idempotent).
func (s *ItemStore) Init(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS memory_items (
			id          TEXT PRIMARY KEY,
			kind        TEXT NOT NULL,
			content     TEXT NOT NULL,
			scope_kind  TEXT NOT NULL,
			scope_ref   TEXT NOT NULL,
			source_kind TEXT,
			source_ref  TEXT,
			source_agent TEXT,
			pinned      BOOLEAN NOT NULL DEFAULT FALSE,
			tags        JSONB,
			embedding   JSONB,
			created_at  BIGINT NOT NULL,
			updated_at  BIGINT NOT NULL,
			expires_at  BIGINT NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_items_scope_kind ON memory_items(scope_kind, scope_ref, kind, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_kind ON memory_items(kind, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_pinned ON memory_items(pinned) WHERE pinned = TRUE`,
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("postgres: memory_items init: %w", err)
		}
	}
	return nil
}

func (s *ItemStore) Upsert(ctx context.Context, it core.MemoryItem) error {
	if it.ID == "" {
		return errors.New("postgres: item ID required")
	}
	now := time.Now().Unix()
	tags, _ := json.Marshal(it.Tags)
	emb, _ := json.Marshal(it.Embedding)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO memory_items
			(id, kind, content, scope_kind, scope_ref, source_kind, source_ref, source_agent,
			 pinned, tags, embedding, created_at, updated_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE SET
			kind         = EXCLUDED.kind,
			content      = EXCLUDED.content,
			scope_kind   = EXCLUDED.scope_kind,
			scope_ref    = EXCLUDED.scope_ref,
			source_kind  = EXCLUDED.source_kind,
			source_ref   = EXCLUDED.source_ref,
			source_agent = EXCLUDED.source_agent,
			pinned       = EXCLUDED.pinned,
			tags         = EXCLUDED.tags,
			embedding    = EXCLUDED.embedding,
			updated_at   = EXCLUDED.updated_at,
			expires_at   = EXCLUDED.expires_at
		`,
		it.ID, string(it.Kind), it.Content, string(it.Scope.Kind), it.Scope.Ref,
		nullableStr(it.Source.Kind), nullableStr(it.Source.Ref), nullableStr(it.Source.AgentID),
		it.Pinned, string(tags), string(emb),
		coalesceUnixPg(it.CreatedAt, now), now, it.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert item: %w", err)
	}
	return nil
}

func (s *ItemStore) UpsertBatch(ctx context.Context, items []core.MemoryItem) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, it := range items {
		if err := s.upsertTx(ctx, tx, it); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *ItemStore) upsertTx(ctx context.Context, tx pgx.Tx, it core.MemoryItem) error {
	if it.ID == "" {
		return errors.New("postgres: item ID required")
	}
	now := time.Now().Unix()
	tags, _ := json.Marshal(it.Tags)
	emb, _ := json.Marshal(it.Embedding)

	_, err := tx.Exec(ctx, `
		INSERT INTO memory_items
			(id, kind, content, scope_kind, scope_ref, source_kind, source_ref, source_agent,
			 pinned, tags, embedding, created_at, updated_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE SET
			kind         = EXCLUDED.kind,
			content      = EXCLUDED.content,
			scope_kind   = EXCLUDED.scope_kind,
			scope_ref    = EXCLUDED.scope_ref,
			source_kind  = EXCLUDED.source_kind,
			source_ref   = EXCLUDED.source_ref,
			source_agent = EXCLUDED.source_agent,
			pinned       = EXCLUDED.pinned,
			tags         = EXCLUDED.tags,
			embedding    = EXCLUDED.embedding,
			updated_at   = EXCLUDED.updated_at,
			expires_at   = EXCLUDED.expires_at
		`,
		it.ID, string(it.Kind), it.Content, string(it.Scope.Kind), it.Scope.Ref,
		nullableStr(it.Source.Kind), nullableStr(it.Source.Ref), nullableStr(it.Source.AgentID),
		it.Pinned, string(tags), string(emb),
		coalesceUnixPg(it.CreatedAt, now), now, it.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert item (tx): %w", err)
	}
	return nil
}

func (s *ItemStore) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM memory_items WHERE id = $1`, id)
	return err
}

func (s *ItemStore) DeleteWhere(ctx context.Context, f core.MemoryFilter) (int, error) {
	if f.IsEmpty() {
		return 0, errors.New("postgres: refuse delete with empty filter")
	}
	q, args := buildWherePg("DELETE FROM memory_items", f, false)
	tag, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *ItemStore) Get(ctx context.Context, id string) (core.MemoryItem, error) {
	row := s.pool.QueryRow(ctx, baseSelectPg()+" WHERE id = $1", id)
	it, err := scanItemPg(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return core.MemoryItem{}, core.ErrNotFound
	}
	return it, err
}

func (s *ItemStore) List(ctx context.Context, f core.MemoryFilter) ([]core.MemoryItem, error) {
	q, args := buildWherePg(baseSelectPg(), f, true)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.MemoryItem
	for rows.Next() {
		it, err := scanItemPg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *ItemStore) SearchSemantic(ctx context.Context, emb []float32, f core.MemoryFilter, topK int) ([]core.ScoredMemoryItem, error) {
	items, err := s.List(ctx, f)
	if err != nil {
		return nil, err
	}
	scored := make([]core.ScoredMemoryItem, 0, len(items))
	for _, it := range items {
		if len(it.Embedding) == 0 {
			continue
		}
		scored = append(scored, core.ScoredMemoryItem{
			Item:  it,
			Score: core.CosineSimilarity(emb, it.Embedding),
		})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}

// --- helpers ---

func baseSelectPg() string {
	return `SELECT id, kind, content, scope_kind, scope_ref, source_kind, source_ref, source_agent,
	  pinned, tags, embedding, created_at, updated_at, expires_at FROM memory_items`
}

type pgRowScanner interface{ Scan(dest ...any) error }

func scanItemPg(r pgRowScanner) (core.MemoryItem, error) {
	var it core.MemoryItem
	var tagsJSON, embJSON []byte
	var srcKind, srcRef, srcAgent *string
	if err := r.Scan(
		&it.ID, &it.Kind, &it.Content, &it.Scope.Kind, &it.Scope.Ref,
		&srcKind, &srcRef, &srcAgent, &it.Pinned,
		&tagsJSON, &embJSON,
		&it.CreatedAt, &it.UpdatedAt, &it.ExpiresAt,
	); err != nil {
		return core.MemoryItem{}, err
	}
	if srcKind != nil {
		it.Source.Kind = *srcKind
	}
	if srcRef != nil {
		it.Source.Ref = *srcRef
	}
	if srcAgent != nil {
		it.Source.AgentID = *srcAgent
	}
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &it.Tags)
	}
	if len(embJSON) > 0 {
		_ = json.Unmarshal(embJSON, &it.Embedding)
	}
	return it, nil
}

func buildWherePg(base string, f core.MemoryFilter, withOrderLimit bool) (string, []any) {
	var sb strings.Builder
	sb.WriteString(base)
	var args []any
	var where []string
	p := 1

	if len(f.Kinds) > 0 {
		placeholders := make([]string, len(f.Kinds))
		for i, k := range f.Kinds {
			placeholders[i] = fmt.Sprintf("$%d", p)
			p++
			args = append(args, string(k))
		}
		where = append(where, "kind IN ("+strings.Join(placeholders, ",")+")")
	}
	if f.Scope != nil {
		where = append(where, fmt.Sprintf("scope_kind = $%d AND scope_ref = $%d", p, p+1))
		args = append(args, string(f.Scope.Kind), f.Scope.Ref)
		p += 2
	}
	if f.Pinned != nil {
		where = append(where, fmt.Sprintf("pinned = $%d", p))
		args = append(args, *f.Pinned)
		p++
	}
	if f.Since > 0 {
		where = append(where, fmt.Sprintf("created_at >= $%d", p))
		args = append(args, f.Since)
		p++
	}
	if f.Until > 0 {
		where = append(where, fmt.Sprintf("created_at <= $%d", p))
		args = append(args, f.Until)
		p++
	}
	if !f.IncludeExp {
		where = append(where, fmt.Sprintf("(expires_at = 0 OR expires_at > $%d)", p))
		args = append(args, time.Now().Unix())
		p++
	}
	_ = p // suppress "p declared and not used" if no conditions added beyond this point
	if len(where) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(where, " AND "))
	}
	if withOrderLimit {
		sb.WriteString(" ORDER BY created_at DESC")
		limit := f.Limit
		if limit <= 0 {
			limit = 50
		}
		sb.WriteString(fmt.Sprintf(" LIMIT %d", limit))
	}
	return sb.String(), args
}

func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func coalesceUnixPg(v, now int64) int64 {
	if v == 0 {
		return now
	}
	return v
}
