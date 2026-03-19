package personaltodo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const sqliteDriver = "sqlite"

type Status string

const (
	StatusOpen Status = "open"
	StatusDone Status = "done"
)

type Item struct {
	OwnerID       string
	ItemID        int
	Text          string
	Status        Status
	CreatedAt     time.Time
	CompletedAt   time.Time
	UpdatedAt     time.Time
	SourceChannel string
	SourceChatID  string
}

type Counts struct {
	Open  int
	Done  int
	Total int
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("personaltodo: mkdir: %w", err)
	}

	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		return nil, fmt.Errorf("personaltodo: open db: %w", err)
	}

	store := &Store{db: db}
	if err := store.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) init(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA busy_timeout=5000;`,
		`CREATE TABLE IF NOT EXISTS todo_items (
			owner_id TEXT NOT NULL,
			item_id INTEGER NOT NULL,
			text TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL,
			completed_at_ms INTEGER NOT NULL DEFAULT 0,
			updated_at_ms INTEGER NOT NULL,
			source_channel TEXT NOT NULL DEFAULT '',
			source_chat_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(owner_id, item_id)
		);`,
		`CREATE TABLE IF NOT EXISTS todo_counters (
			owner_id TEXT PRIMARY KEY,
			next_item_id INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_todo_items_owner_status_updated
			ON todo_items(owner_id, status, updated_at_ms DESC, item_id DESC);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("personaltodo: init schema: %w", err)
		}
	}
	return nil
}

func (s *Store) Add(ctx context.Context, ownerID, text, sourceChannel, sourceChatID string) (Item, error) {
	if s == nil || s.db == nil {
		return Item{}, errors.New("store is not configured")
	}
	ownerID = strings.TrimSpace(ownerID)
	text = strings.TrimSpace(text)
	if ownerID == "" {
		return Item{}, errors.New("owner_id is required")
	}
	if text == "" {
		return Item{}, errors.New("text is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, fmt.Errorf("personaltodo: begin add: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	itemID, err := nextItemID(ctx, tx, ownerID)
	if err != nil {
		return Item{}, err
	}

	now := time.Now()
	item := Item{
		OwnerID:       ownerID,
		ItemID:        itemID,
		Text:          text,
		Status:        StatusOpen,
		CreatedAt:     now,
		UpdatedAt:     now,
		SourceChannel: strings.TrimSpace(sourceChannel),
		SourceChatID:  strings.TrimSpace(sourceChatID),
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO todo_items(owner_id, item_id, text, status, created_at_ms, completed_at_ms, updated_at_ms, source_channel, source_chat_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.OwnerID,
		item.ItemID,
		item.Text,
		item.Status,
		item.CreatedAt.UnixMilli(),
		0,
		item.UpdatedAt.UnixMilli(),
		item.SourceChannel,
		item.SourceChatID,
	)
	if err != nil {
		return Item{}, fmt.Errorf("personaltodo: insert item: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Item{}, fmt.Errorf("personaltodo: commit add: %w", err)
	}
	return item, nil
}

func (s *Store) List(ctx context.Context, ownerID string, scope Status, limit int) ([]Item, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store is not configured")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, errors.New("owner_id is required")
	}
	if limit <= 0 {
		limit = 10
	}

	query := `SELECT owner_id, item_id, text, status, created_at_ms, completed_at_ms, updated_at_ms, source_channel, source_chat_id
		FROM todo_items
		WHERE owner_id = ?`
	args := []any{ownerID}
	if scope == StatusOpen || scope == StatusDone {
		query += ` AND status = ?`
		args = append(args, string(scope))
	}
	query += ` ORDER BY updated_at_ms DESC, item_id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("personaltodo: list: %w", err)
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("personaltodo: list rows: %w", err)
	}
	return items, nil
}

func (s *Store) Counts(ctx context.Context, ownerID string) (Counts, error) {
	if s == nil || s.db == nil {
		return Counts{}, errors.New("store is not configured")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return Counts{}, errors.New("owner_id is required")
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT status, COUNT(*) FROM todo_items WHERE owner_id = ? GROUP BY status`,
		ownerID,
	)
	if err != nil {
		return Counts{}, fmt.Errorf("personaltodo: counts: %w", err)
	}
	defer rows.Close()

	var counts Counts
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return Counts{}, fmt.Errorf("personaltodo: scan counts: %w", err)
		}
		switch Status(status) {
		case StatusOpen:
			counts.Open = count
		case StatusDone:
			counts.Done = count
		}
		counts.Total += count
	}
	if err := rows.Err(); err != nil {
		return Counts{}, fmt.Errorf("personaltodo: counts rows: %w", err)
	}
	return counts, nil
}

func (s *Store) MarkDone(ctx context.Context, ownerID string, itemID int) (Item, error) {
	return s.updateStatus(ctx, ownerID, itemID, StatusDone)
}

func (s *Store) Reopen(ctx context.Context, ownerID string, itemID int) (Item, error) {
	return s.updateStatus(ctx, ownerID, itemID, StatusOpen)
}

func (s *Store) Delete(ctx context.Context, ownerID string, itemID int) error {
	if s == nil || s.db == nil {
		return errors.New("store is not configured")
	}
	if strings.TrimSpace(ownerID) == "" {
		return errors.New("owner_id is required")
	}
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM todo_items WHERE owner_id = ? AND item_id = ?`,
		ownerID,
		itemID,
	)
	if err != nil {
		return fmt.Errorf("personaltodo: delete: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("personaltodo: delete rows affected: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ClearCompleted(ctx context.Context, ownerID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("store is not configured")
	}
	if strings.TrimSpace(ownerID) == "" {
		return 0, errors.New("owner_id is required")
	}
	result, err := s.db.ExecContext(
		ctx,
		`DELETE FROM todo_items WHERE owner_id = ? AND status = ?`,
		ownerID,
		string(StatusDone),
	)
	if err != nil {
		return 0, fmt.Errorf("personaltodo: clear completed: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("personaltodo: clear completed rows affected: %w", err)
	}
	return rows, nil
}

func (s *Store) updateStatus(ctx context.Context, ownerID string, itemID int, status Status) (Item, error) {
	if s == nil || s.db == nil {
		return Item{}, errors.New("store is not configured")
	}
	if strings.TrimSpace(ownerID) == "" {
		return Item{}, errors.New("owner_id is required")
	}

	now := time.Now()
	completedAt := int64(0)
	if status == StatusDone {
		completedAt = now.UnixMilli()
	}

	result, err := s.db.ExecContext(
		ctx,
		`UPDATE todo_items
		 SET status = ?, completed_at_ms = ?, updated_at_ms = ?
		 WHERE owner_id = ? AND item_id = ?`,
		string(status),
		completedAt,
		now.UnixMilli(),
		ownerID,
		itemID,
	)
	if err != nil {
		return Item{}, fmt.Errorf("personaltodo: update status: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Item{}, fmt.Errorf("personaltodo: update rows affected: %w", err)
	}
	if rows == 0 {
		return Item{}, sql.ErrNoRows
	}
	return s.Get(ctx, ownerID, itemID)
}

func (s *Store) Get(ctx context.Context, ownerID string, itemID int) (Item, error) {
	if s == nil || s.db == nil {
		return Item{}, errors.New("store is not configured")
	}
	row := s.db.QueryRowContext(
		ctx,
		`SELECT owner_id, item_id, text, status, created_at_ms, completed_at_ms, updated_at_ms, source_channel, source_chat_id
		 FROM todo_items WHERE owner_id = ? AND item_id = ?`,
		ownerID,
		itemID,
	)
	return scanItem(row)
}

func nextItemID(ctx context.Context, tx *sql.Tx, ownerID string) (int, error) {
	var next sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT next_item_id FROM todo_counters WHERE owner_id = ?`, ownerID).Scan(&next)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("personaltodo: read counter: %w", err)
	}

	itemID := 1
	if next.Valid && next.Int64 > 0 {
		itemID = int(next.Int64)
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO todo_counters(owner_id, next_item_id)
		 VALUES (?, ?)
		 ON CONFLICT(owner_id) DO UPDATE SET next_item_id = excluded.next_item_id`,
		ownerID,
		itemID+1,
	)
	if err != nil {
		return 0, fmt.Errorf("personaltodo: write counter: %w", err)
	}
	return itemID, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanItem(row scanner) (Item, error) {
	var (
		item          Item
		createdAtMS   int64
		completedAtMS int64
		updatedAtMS   int64
		status        string
	)
	if err := row.Scan(
		&item.OwnerID,
		&item.ItemID,
		&item.Text,
		&status,
		&createdAtMS,
		&completedAtMS,
		&updatedAtMS,
		&item.SourceChannel,
		&item.SourceChatID,
	); err != nil {
		return Item{}, err
	}
	item.Status = Status(status)
	item.CreatedAt = time.UnixMilli(createdAtMS)
	item.UpdatedAt = time.UnixMilli(updatedAtMS)
	if completedAtMS > 0 {
		item.CompletedAt = time.UnixMilli(completedAtMS)
	}
	return item, nil
}
