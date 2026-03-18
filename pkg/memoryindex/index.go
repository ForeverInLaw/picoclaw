package memoryindex

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

type Index struct {
	db     *sql.DB
	cfg    Config
	hasFTS bool
}

func Open(path string, cfg Config) (*Index, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("memoryindex: mkdir: %w", err)
	}

	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: open db: %w", err)
	}

	idx := &Index{db: db, cfg: normalizeConfig(cfg)}
	if err := idx.init(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return idx, nil
}

func normalizeConfig(cfg Config) Config {
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = 5
	}
	if cfg.MaxSnippetChars <= 0 {
		cfg.MaxSnippetChars = 280
	}
	if cfg.MinQueryChars <= 0 {
		cfg.MinQueryChars = 12
	}
	return cfg
}

func (i *Index) init(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA busy_timeout=5000;`,
		`CREATE TABLE IF NOT EXISTS observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key TEXT NOT NULL,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			role TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_observations_session ON observations(session_key);`,
		`CREATE INDEX IF NOT EXISTS idx_observations_chat ON observations(channel, chat_id);`,
		`CREATE TABLE IF NOT EXISTS memory_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
	}

	for _, stmt := range stmts {
		if _, err := i.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("memoryindex: init schema: %w", err)
		}
	}

	if _, err := i.db.ExecContext(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS observations_fts USING fts5(content, content='observations', content_rowid='id', tokenize='unicode61');`); err == nil {
		i.hasFTS = true
		triggers := []string{
			`CREATE TRIGGER IF NOT EXISTS observations_ai AFTER INSERT ON observations BEGIN
				INSERT INTO observations_fts(rowid, content) VALUES (new.id, new.content);
			END;`,
			`CREATE TRIGGER IF NOT EXISTS observations_ad AFTER DELETE ON observations BEGIN
				INSERT INTO observations_fts(observations_fts, rowid, content) VALUES('delete', old.id, old.content);
			END;`,
			`CREATE TRIGGER IF NOT EXISTS observations_au AFTER UPDATE ON observations BEGIN
				INSERT INTO observations_fts(observations_fts, rowid, content) VALUES('delete', old.id, old.content);
				INSERT INTO observations_fts(rowid, content) VALUES (new.id, new.content);
			END;`,
		}
		for _, trigger := range triggers {
			if _, err := i.db.ExecContext(ctx, trigger); err != nil {
				return fmt.Errorf("memoryindex: init fts trigger: %w", err)
			}
		}
	}

	return nil
}

func (i *Index) AddObservation(ctx context.Context, obs Observation) error {
	content := strings.TrimSpace(obs.Content)
	if i == nil || i.db == nil || content == "" {
		return nil
	}
	obs.Content = content
	if !ShouldIndexObservation(obs) {
		return nil
	}
	if obs.CreatedAt.IsZero() {
		obs.CreatedAt = time.Now()
	}

	_, err := i.db.ExecContext(
		ctx,
		`INSERT INTO observations(session_key, channel, chat_id, role, sender_id, content, created_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		obs.SessionKey,
		obs.Channel,
		obs.ChatID,
		obs.Role,
		obs.SenderID,
		obs.Content,
		obs.CreatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("memoryindex: insert observation: %w", err)
	}
	return nil
}

func (i *Index) Search(ctx context.Context, req SearchRequest) ([]Hit, error) {
	if i == nil || i.db == nil {
		return nil, nil
	}

	query := strings.TrimSpace(req.Query)
	if len([]rune(query)) < i.cfg.MinQueryChars {
		return nil, nil
	}

	limit := req.Limit
	if limit <= 0 {
		limit = i.cfg.MaxResults
	}

	if i.hasFTS {
		return i.searchFTS(ctx, req, limit)
	}
	return i.searchLike(ctx, req, limit)
}

func (i *Index) searchFTS(ctx context.Context, req SearchRequest, limit int) ([]Hit, error) {
	matchQuery := buildFTSQuery(req.Query)
	if matchQuery == "" {
		return nil, nil
	}

	rows, err := i.db.QueryContext(
		ctx,
		`SELECT o.session_key, o.channel, o.chat_id, o.role, o.sender_id, o.content, o.created_at_ms
		   FROM observations_fts f
		   JOIN observations o ON o.id = f.rowid
		  WHERE observations_fts MATCH ?
		  ORDER BY
		    CASE WHEN o.session_key = ? THEN 0 ELSE 1 END,
		    CASE WHEN o.channel = ? AND o.chat_id = ? THEN 0 ELSE 1 END,
		    bm25(observations_fts),
		    o.created_at_ms DESC
		  LIMIT ?`,
		matchQuery,
		req.SessionKey,
		req.Channel,
		req.ChatID,
		limit,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "fts5") {
			i.hasFTS = false
			return i.searchLike(ctx, req, limit)
		}
		return nil, fmt.Errorf("memoryindex: fts search: %w", err)
	}
	defer rows.Close()

	return scanHits(rows, i.cfg.MaxSnippetChars)
}

func (i *Index) searchLike(ctx context.Context, req SearchRequest, limit int) ([]Hit, error) {
	pattern := "%" + req.Query + "%"
	rows, err := i.db.QueryContext(
		ctx,
		`SELECT session_key, channel, chat_id, role, sender_id, content, created_at_ms
		   FROM observations
		  WHERE content LIKE ?
		  ORDER BY
		    CASE WHEN session_key = ? THEN 0 ELSE 1 END,
		    CASE WHEN channel = ? AND chat_id = ? THEN 0 ELSE 1 END,
		    created_at_ms DESC
		  LIMIT ?`,
		pattern,
		req.SessionKey,
		req.Channel,
		req.ChatID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: like search: %w", err)
	}
	defer rows.Close()

	return scanHits(rows, i.cfg.MaxSnippetChars)
}

func scanHits(rows *sql.Rows, maxSnippetChars int) ([]Hit, error) {
	hits := make([]Hit, 0)
	for rows.Next() {
		var (
			hit         Hit
			createdAtMS int64
		)
		if err := rows.Scan(
			&hit.SessionKey,
			&hit.Channel,
			&hit.ChatID,
			&hit.Role,
			&hit.SenderID,
			&hit.Content,
			&createdAtMS,
		); err != nil {
			return nil, fmt.Errorf("memoryindex: scan hit: %w", err)
		}
		hit.Content = truncateRunes(strings.TrimSpace(hit.Content), maxSnippetChars)
		hit.CreatedAt = time.UnixMilli(createdAtMS)
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: rows: %w", err)
	}
	return hits, nil
}

func buildFTSQuery(query string) string {
	terms := strings.Fields(strings.ToLower(query))
	filtered := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.Trim(term, `"'[](){}:,.!?`)
		if len([]rune(term)) < 3 {
			continue
		}
		filtered = append(filtered, `"`+term+`"`)
	}
	return strings.Join(filtered, " OR ")
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func (i *Index) IsBootstrapped(ctx context.Context) (bool, error) {
	return i.metaBool(ctx, "bootstrapped_sessions")
}

func (i *Index) MarkBootstrapped(ctx context.Context) error {
	return i.setMeta(ctx, "bootstrapped_sessions", "1")
}

func (i *Index) metaBool(ctx context.Context, key string) (bool, error) {
	var value string
	err := i.db.QueryRowContext(ctx, `SELECT value FROM memory_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("memoryindex: read meta: %w", err)
	}
	return value == "1", nil
}

func (i *Index) setMeta(ctx context.Context, key, value string) error {
	_, err := i.db.ExecContext(ctx, `INSERT INTO memory_meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("memoryindex: set meta: %w", err)
	}
	return nil
}

func (i *Index) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}
