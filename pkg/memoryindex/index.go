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
			peer_kind TEXT NOT NULL DEFAULT '',
			chat_label TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			source_kind TEXT NOT NULL DEFAULT '',
			source_key TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_observations_session ON observations(session_key);`,
		`CREATE INDEX IF NOT EXISTS idx_observations_chat ON observations(channel, chat_id);`,
		`CREATE INDEX IF NOT EXISTS idx_observations_chat_time ON observations(channel, chat_id, created_at_ms DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_observations_source ON observations(source_kind, source_key);`,
		`CREATE TABLE IF NOT EXISTS chat_catalog (
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			peer_kind TEXT NOT NULL DEFAULT '',
			label TEXT NOT NULL DEFAULT '',
			last_seen_ms INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(channel, chat_id)
		);`,
		`CREATE TABLE IF NOT EXISTS chat_aliases (
			alias TEXT PRIMARY KEY,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			allowed_requesters_json TEXT NOT NULL DEFAULT '[]'
		);`,
		`CREATE TABLE IF NOT EXISTS chat_participants (
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			last_seen_ms INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(channel, chat_id, sender_id)
		);`,
		`CREATE TABLE IF NOT EXISTS observation_embeddings (
			observation_id INTEGER PRIMARY KEY,
			model_name TEXT NOT NULL,
			dims INTEGER NOT NULL,
			vector_json TEXT NOT NULL,
			updated_at_ms INTEGER NOT NULL,
			FOREIGN KEY(observation_id) REFERENCES observations(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS chat_rollups (
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			window_start_ms INTEGER NOT NULL,
			window_end_ms INTEGER NOT NULL,
			source_count INTEGER NOT NULL DEFAULT 0,
			last_observation_ms INTEGER NOT NULL DEFAULT 0,
			summary TEXT NOT NULL,
			created_at_ms INTEGER NOT NULL,
			updated_at_ms INTEGER NOT NULL,
			PRIMARY KEY(channel, chat_id, window_start_ms, window_end_ms)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_chat_rollups_window ON chat_rollups(channel, chat_id, window_start_ms DESC);`,
		`CREATE TABLE IF NOT EXISTS chat_rollup_embeddings (
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			window_start_ms INTEGER NOT NULL,
			window_end_ms INTEGER NOT NULL,
			model_name TEXT NOT NULL,
			dims INTEGER NOT NULL,
			vector_json TEXT NOT NULL,
			updated_at_ms INTEGER NOT NULL,
			PRIMARY KEY(channel, chat_id, window_start_ms, window_end_ms)
		);`,
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
	for _, stmt := range []string{
		`ALTER TABLE observations ADD COLUMN source_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE observations ADD COLUMN source_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE observations ADD COLUMN peer_kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE observations ADD COLUMN chat_label TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := i.db.ExecContext(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return fmt.Errorf("memoryindex: migrate schema: %w", err)
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
				DELETE FROM observation_embeddings WHERE observation_id = old.id;
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

	return i.insertObservation(ctx, i.db, obs)
}

type execContexter interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (i *Index) insertObservation(ctx context.Context, execer execContexter, obs Observation) error {
	_, err := execer.ExecContext(
		ctx,
		`INSERT INTO observations(session_key, channel, chat_id, peer_kind, chat_label, role, sender_id, source_kind, source_key, content, created_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		obs.SessionKey,
		obs.Channel,
		obs.ChatID,
		obs.PeerKind,
		obs.ChatLabel,
		obs.Role,
		obs.SenderID,
		obs.SourceKind,
		obs.SourceKey,
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

	query := `SELECT o.session_key, o.channel, o.chat_id, o.peer_kind, o.chat_label, o.role, o.sender_id, o.content, o.created_at_ms
		FROM observations_fts f
		JOIN observations o ON o.id = f.rowid`
	whereParts := []string{"observations_fts MATCH ?"}
	args := []any{matchQuery}
	whereParts, args = appendSearchFilters(whereParts, args, req)
	query += " WHERE " + strings.Join(whereParts, " AND ")
	query += `
		ORDER BY
			CASE WHEN o.session_key = ? THEN 0 ELSE 1 END,
			CASE WHEN o.channel = ? AND o.chat_id = ? THEN 0 ELSE 1 END,
			bm25(observations_fts),
			o.created_at_ms DESC
		LIMIT ?`
	args = append(args, req.SessionKey, req.Channel, req.ChatID, limit)

	rows, err := i.db.QueryContext(ctx, query, args...)
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
	query := `SELECT o.session_key, o.channel, o.chat_id, o.peer_kind, o.chat_label, o.role, o.sender_id, o.content, o.created_at_ms
		FROM observations o`
	whereParts := []string{"content LIKE ?"}
	args := []any{pattern}
	whereParts, args = appendSearchFilters(whereParts, args, req)
	query += " WHERE " + strings.Join(whereParts, " AND ")
	query += `
		ORDER BY
			CASE WHEN session_key = ? THEN 0 ELSE 1 END,
			CASE WHEN channel = ? AND chat_id = ? THEN 0 ELSE 1 END,
			created_at_ms DESC
		LIMIT ?`
	args = append(args, req.SessionKey, req.Channel, req.ChatID, limit)

	rows, err := i.db.QueryContext(ctx, query, args...)
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
			&hit.PeerKind,
			&hit.ChatLabel,
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

func appendSearchFilters(whereParts []string, args []any, req SearchRequest) ([]string, []any) {
	scopes := compactScopes(req)
	if len(scopes) > 0 {
		clauses := make([]string, 0, len(scopes))
		for _, scope := range scopes {
			clauses = append(clauses, "(o.channel = ? AND o.chat_id = ?)")
			args = append(args, scope.Channel, scope.ChatID)
		}
		whereParts = append(whereParts, "("+strings.Join(clauses, " OR ")+")")
	} else if strings.TrimSpace(req.Channel) != "" && strings.TrimSpace(req.ChatID) != "" {
		whereParts = append(whereParts, "o.channel = ?", "o.chat_id = ?")
		args = append(args, req.Channel, req.ChatID)
	}

	if !req.Since.IsZero() {
		whereParts = append(whereParts, "o.created_at_ms >= ?")
		args = append(args, req.Since.UnixMilli())
	}
	if !req.Until.IsZero() {
		whereParts = append(whereParts, "o.created_at_ms <= ?")
		args = append(args, req.Until.UnixMilli())
	}
	return whereParts, args
}

func compactScopes(req SearchRequest) []ChatScope {
	if len(req.ChatScopes) > 0 {
		return req.ChatScopes
	}
	if strings.TrimSpace(req.Channel) == "" || strings.TrimSpace(req.ChatID) == "" {
		return nil
	}
	return []ChatScope{{Channel: req.Channel, ChatID: req.ChatID}}
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

func (i *Index) metaString(ctx context.Context, key string) (string, error) {
	var value string
	err := i.db.QueryRowContext(ctx, `SELECT value FROM memory_meta WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("memoryindex: read meta: %w", err)
	}
	return value, nil
}

func (i *Index) ReplaceSourceObservations(ctx context.Context, sourceKind, sourceKey string, observations []Observation) error {
	if i == nil || i.db == nil {
		return nil
	}
	sourceKind = strings.TrimSpace(sourceKind)
	sourceKey = strings.TrimSpace(sourceKey)
	if sourceKind == "" || sourceKey == "" {
		return fmt.Errorf("memoryindex: source_kind and source_key are required")
	}

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memoryindex: begin replace source tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM observations WHERE source_kind = ? AND source_key = ?`, sourceKind, sourceKey); err != nil {
		return fmt.Errorf("memoryindex: delete source observations: %w", err)
	}

	for _, obs := range observations {
		obs.SourceKind = sourceKind
		obs.SourceKey = sourceKey
		if content := strings.TrimSpace(obs.Content); content != "" {
			obs.Content = content
		}
		if !ShouldIndexObservation(obs) {
			continue
		}
		if obs.CreatedAt.IsZero() {
			obs.CreatedAt = time.Now()
		}
		if err := i.insertObservation(ctx, tx, obs); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memoryindex: commit replace source tx: %w", err)
	}
	return nil
}

func (i *Index) Close() error {
	if i == nil || i.db == nil {
		return nil
	}
	return i.db.Close()
}
