// store/sqlite/memory.go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nevindra/oasis/core"
)

// ItemStore is a SQLite-backed implementation of core.MemoryItemStore.
// Embeddings are stored as JSON text; similarity is computed in-process
// using brute-force cosine similarity (sufficient for sub-100k row counts).
type ItemStore struct {
	db     *sql.DB
	logger *slog.Logger
}

var _ core.MemoryItemStore = (*ItemStore)(nil)

// NewItemStore constructs an ItemStore on the given *sql.DB. Use
// Store.DB() so it shares the serialized connection.
func NewItemStore(db *sql.DB, logger *slog.Logger) *ItemStore {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &ItemStore{db: db, logger: logger}
}

// Init creates the memory_items table if missing.
func (s *ItemStore) Init(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS memory_items (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		content TEXT NOT NULL,
		scope_kind TEXT NOT NULL,
		scope_ref TEXT NOT NULL,
		source_kind TEXT,
		source_ref TEXT,
		source_agent TEXT,
		pinned INTEGER NOT NULL DEFAULT 0,
		tags TEXT,
		embedding TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return fmt.Errorf("create memory_items: %w", err)
	}

	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS idx_items_scope_kind ON memory_items(scope_kind, scope_ref, kind, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_kind ON memory_items(kind, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_items_pinned ON memory_items(pinned) WHERE pinned = 1`,
	} {
		if _, err := s.db.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}
	return nil
}

// execer is satisfied by both *sql.DB and *sql.Tx, allowing upsertTx to
// operate on either the connection pool or an in-progress transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *ItemStore) upsertTx(ctx context.Context, q execer, it core.MemoryItem) error {
	if it.ID == "" {
		return errors.New("sqlite: item ID required")
	}
	now := time.Now().Unix()
	tags, _ := json.Marshal(it.Tags)
	emb, _ := json.Marshal(it.Embedding)

	// INSERT ... ON CONFLICT preserves the original CreatedAt.
	_, err := q.ExecContext(ctx, `
		INSERT INTO memory_items
			(id, kind, content, scope_kind, scope_ref, source_kind, source_ref, source_agent,
			 pinned, tags, embedding, created_at, updated_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			kind=excluded.kind,
			content=excluded.content,
			scope_kind=excluded.scope_kind,
			scope_ref=excluded.scope_ref,
			source_kind=excluded.source_kind,
			source_ref=excluded.source_ref,
			source_agent=excluded.source_agent,
			pinned=excluded.pinned,
			tags=excluded.tags,
			embedding=excluded.embedding,
			updated_at=excluded.updated_at,
			expires_at=excluded.expires_at
		`,
		it.ID, string(it.Kind), it.Content, string(it.Scope.Kind), it.Scope.Ref,
		it.Source.Kind, it.Source.Ref, it.Source.AgentID,
		boolToInt(it.Pinned), string(tags), string(emb),
		coalesceUnix(it.CreatedAt, now), now, it.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("sqlite upsert: %w", err)
	}
	return nil
}

func (s *ItemStore) Upsert(ctx context.Context, it core.MemoryItem) error {
	return s.upsertTx(ctx, s.db, it)
}

func (s *ItemStore) UpsertBatch(ctx context.Context, items []core.MemoryItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, it := range items {
		if err := s.upsertTx(ctx, tx, it); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *ItemStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memory_items WHERE id = ?`, id)
	return err
}

func (s *ItemStore) DeleteWhere(ctx context.Context, f core.MemoryFilter) (int, error) {
	if f.IsEmpty() {
		return 0, errors.New("sqlite: refuse delete with empty filter")
	}
	q, args := buildMemWhere("DELETE FROM memory_items", f, false)
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *ItemStore) Get(ctx context.Context, id string) (core.MemoryItem, error) {
	row := s.db.QueryRowContext(ctx, baseSelect()+" WHERE id = ?", id)
	it, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.MemoryItem{}, core.ErrNotFound
	}
	return it, err
}

func (s *ItemStore) List(ctx context.Context, f core.MemoryFilter) ([]core.MemoryItem, error) {
	q, args := buildMemWhere(baseSelect(), f, true)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.MemoryItem
	for rows.Next() {
		it, err := scanItem(rows)
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
		scored = append(scored, core.ScoredMemoryItem{Item: it, Score: core.CosineSimilarity(emb, it.Embedding)})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}

// --- helpers ---

func baseSelect() string {
	return `SELECT id, kind, content, scope_kind, scope_ref, source_kind, source_ref, source_agent,
	  pinned, tags, embedding, created_at, updated_at, expires_at FROM memory_items`
}

type rowScanner interface{ Scan(dest ...any) error }

func scanItem(r rowScanner) (core.MemoryItem, error) {
	var it core.MemoryItem
	var tagsJSON, embJSON sql.NullString
	var srcKind, srcRef, srcAgent sql.NullString
	var pinnedInt int
	if err := r.Scan(&it.ID, &it.Kind, &it.Content, &it.Scope.Kind, &it.Scope.Ref,
		&srcKind, &srcRef, &srcAgent, &pinnedInt, &tagsJSON, &embJSON,
		&it.CreatedAt, &it.UpdatedAt, &it.ExpiresAt); err != nil {
		return core.MemoryItem{}, err
	}
	it.Source = core.MemorySource{Kind: srcKind.String, Ref: srcRef.String, AgentID: srcAgent.String}
	it.Pinned = pinnedInt != 0
	if tagsJSON.Valid && tagsJSON.String != "" {
		_ = json.Unmarshal([]byte(tagsJSON.String), &it.Tags)
	}
	if embJSON.Valid && embJSON.String != "" {
		_ = json.Unmarshal([]byte(embJSON.String), &it.Embedding)
	}
	return it, nil
}

func buildMemWhere(base string, f core.MemoryFilter, withOrderLimit bool) (string, []any) {
	var sb strings.Builder
	sb.WriteString(base)
	var args []any
	var where []string
	if len(f.Kinds) > 0 {
		placeholders := strings.Repeat("?,", len(f.Kinds))
		where = append(where, "kind IN ("+strings.TrimSuffix(placeholders, ",")+")")
		for _, k := range f.Kinds {
			args = append(args, string(k))
		}
	}
	if f.Scope != nil {
		where = append(where, "scope_kind = ? AND scope_ref = ?")
		args = append(args, string(f.Scope.Kind), f.Scope.Ref)
	}
	if f.Pinned != nil {
		where = append(where, "pinned = ?")
		args = append(args, boolToInt(*f.Pinned))
	}
	if f.Since > 0 {
		where = append(where, "created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		where = append(where, "created_at <= ?")
		args = append(args, f.Until)
	}
	if !f.IncludeExp {
		where = append(where, "(expires_at = 0 OR expires_at > ?)")
		args = append(args, time.Now().Unix())
	}
	// Tags filter is post-applied in Go (JSON column doesn't support efficient AND-of-tags here).
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
		// Why: parameterize LIMIT like every other clause so the whole query is
		// uniformly bound (no fmt.Sprintf'd literal mixed into a parameterized
		// statement).
		sb.WriteString(" LIMIT ?")
		args = append(args, limit)
	}
	return sb.String(), args
}

func coalesceUnix(v, now int64) int64 {
	if v == 0 {
		return now
	}
	return v
}
