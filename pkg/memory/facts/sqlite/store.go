package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// Store is a modernc.org/sqlite-backed facts.Store. Use NewStore.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Insert(ctx context.Context, f facts.Fact) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO facts
			(namespace, entity, attribute, value, confidence,
			 source_msg_ref, source_actor,
			 created_at, updated_at, last_seen_at,
			 access_count, ttl_seconds, embedding, embedding_norm)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.Namespace, f.Entity, f.Attribute, f.Value, f.Confidence,
		f.SourceMsgRef, nullableString(f.SourceActor),
		f.CreatedAt.UTC(), f.UpdatedAt.UTC(), f.LastSeenAt.UTC(),
		f.AccessCount, nullableInt(f.TTLSeconds),
		blobOrNil(f.Embedding), nullableFloat(f.EmbeddingNorm),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetByID(ctx context.Context, id int64) (facts.Fact, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, namespace, entity, attribute, value, confidence,
		       source_msg_ref, COALESCE(source_actor, ''),
		       created_at, updated_at, last_seen_at,
		       access_count, COALESCE(ttl_seconds, 0),
		       deleted_at, embedding, COALESCE(embedding_norm, 0)
		FROM facts WHERE id = ?`, id)
	return scanFact(row)
}

func (s *Store) SoftDelete(ctx context.Context, id int64) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE facts SET deleted_at = ?, updated_at = ? WHERE id = ?`,
		now, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return facts.ErrNotFound
	}
	return nil
}

// --- helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFact(r rowScanner) (facts.Fact, error) {
	var f facts.Fact
	var deletedAt sql.NullTime
	var embedding []byte
	err := r.Scan(
		&f.ID, &f.Namespace, &f.Entity, &f.Attribute, &f.Value, &f.Confidence,
		&f.SourceMsgRef, &f.SourceActor,
		&f.CreatedAt, &f.UpdatedAt, &f.LastSeenAt,
		&f.AccessCount, &f.TTLSeconds,
		&deletedAt, &embedding, &f.EmbeddingNorm,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return facts.Fact{}, facts.ErrNotFound
	}
	if err != nil {
		return facts.Fact{}, err
	}
	if deletedAt.Valid {
		f.DeletedAt = deletedAt.Time
	}
	if len(embedding) > 0 {
		f.Embedding = bytesToFloats(embedding)
	}
	return f, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

func nullableFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func blobOrNil(v []float32) any {
	if len(v) == 0 {
		return nil
	}
	return floatsToBytes(v)
}
