package sherlock

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

const defaultPerUserLimit = 20

type Store struct {
	db *sql.DB
}

type User struct {
	UserID       string
	Username     string
	DisplayLabel string
	LastSeenAt   time.Time
	MessageCount int
}

type Message struct {
	Text      string
	CreatedAt time.Time
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("sherlock: mkdir: %w", err)
	}

	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		return nil, fmt.Errorf("sherlock: open db: %w", err)
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
		`CREATE TABLE IF NOT EXISTS users (
			user_id TEXT PRIMARY KEY,
			username TEXT NOT NULL DEFAULT '',
			display_label TEXT NOT NULL DEFAULT '',
			last_seen_ms INTEGER NOT NULL,
			message_count INTEGER NOT NULL DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			text TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(user_id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sherlock_messages_user_created
			ON messages(user_id, created_at_ms DESC, id DESC);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sherlock: init schema: %w", err)
		}
	}
	return nil
}

func (s *Store) RecordMessage(ctx context.Context, userID, username, displayLabel, text string) error {
	if s == nil || s.db == nil {
		return errors.New("store is not configured")
	}
	userID = strings.TrimSpace(userID)
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	displayLabel = strings.TrimSpace(displayLabel)
	text = normalizeText(text)
	if userID == "" {
		return errors.New("user_id is required")
	}
	if text == "" {
		return errors.New("text is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sherlock: begin record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO users(user_id, username, display_label, last_seen_ms, message_count)
		 VALUES(?, ?, ?, ?, 1)
		 ON CONFLICT(user_id) DO UPDATE SET
			username = CASE WHEN excluded.username <> '' THEN excluded.username ELSE users.username END,
			display_label = CASE WHEN excluded.display_label <> '' THEN excluded.display_label ELSE users.display_label END,
			last_seen_ms = excluded.last_seen_ms,
			message_count = users.message_count + 1`,
		userID,
		username,
		displayLabel,
		now.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("sherlock: upsert user: %w", err)
	}

	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO messages(user_id, text, created_at_ms) VALUES(?, ?, ?)`,
		userID,
		text,
		now.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("sherlock: insert message: %w", err)
	}

	_, err = tx.ExecContext(
		ctx,
		`DELETE FROM messages
		 WHERE user_id = ?
		   AND id NOT IN (
				SELECT id FROM messages WHERE user_id = ? ORDER BY created_at_ms DESC, id DESC LIMIT ?
		   )`,
		userID,
		userID,
		defaultPerUserLimit,
	)
	if err != nil {
		return fmt.Errorf("sherlock: prune messages: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sherlock: commit record: %w", err)
	}
	return nil
}

func (s *Store) ListUsers(ctx context.Context, offset, limit int) ([]User, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, errors.New("store is not configured")
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 8
	}

	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sherlock: count users: %w", err)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT user_id, username, display_label, last_seen_ms, message_count
		 FROM users
		 ORDER BY last_seen_ms DESC, user_id ASC
		 LIMIT ? OFFSET ?`,
		limit,
		offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("sherlock: list users: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0, limit)
	for rows.Next() {
		var (
			user       User
			lastSeenMS int64
		)
		if err := rows.Scan(&user.UserID, &user.Username, &user.DisplayLabel, &lastSeenMS, &user.MessageCount); err != nil {
			return nil, 0, fmt.Errorf("sherlock: scan user: %w", err)
		}
		user.LastSeenAt = time.UnixMilli(lastSeenMS).UTC()
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("sherlock: user rows: %w", err)
	}
	return users, total, nil
}

func (s *Store) GetUser(ctx context.Context, userID string) (User, bool, error) {
	if s == nil || s.db == nil {
		return User{}, false, errors.New("store is not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return User{}, false, nil
	}

	var (
		user       User
		lastSeenMS int64
	)
	err := s.db.QueryRowContext(
		ctx,
		`SELECT user_id, username, display_label, last_seen_ms, message_count FROM users WHERE user_id = ?`,
		userID,
	).Scan(&user.UserID, &user.Username, &user.DisplayLabel, &lastSeenMS, &user.MessageCount)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("sherlock: get user: %w", err)
	}
	user.LastSeenAt = time.UnixMilli(lastSeenMS).UTC()
	return user, true, nil
}

func (s *Store) GetMessages(ctx context.Context, userID string, limit int) ([]Message, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store is not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("user_id is required")
	}
	if limit <= 0 {
		limit = defaultPerUserLimit
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT text, created_at_ms
		 FROM messages
		 WHERE user_id = ?
		 ORDER BY created_at_ms DESC, id DESC
		 LIMIT ?`,
		userID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("sherlock: get messages: %w", err)
	}
	defer rows.Close()

	messages := make([]Message, 0, limit)
	for rows.Next() {
		var (
			message     Message
			createdAtMS int64
		)
		if err := rows.Scan(&message.Text, &createdAtMS); err != nil {
			return nil, fmt.Errorf("sherlock: scan message: %w", err)
		}
		message.CreatedAt = time.UnixMilli(createdAtMS).UTC()
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sherlock: message rows: %w", err)
	}
	return messages, nil
}

func normalizeText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.TrimSpace(value)
}
