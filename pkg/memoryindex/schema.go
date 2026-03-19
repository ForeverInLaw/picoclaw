package memoryindex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	schemaStatusReady    = "ready"
	schemaStatusRepaired = "repaired"
)

var requiredTables = []string{
	"observations",
	"chat_catalog",
	"chat_aliases",
	"chat_participants",
	"observation_embeddings",
	"chat_rollups",
	"chat_rollup_embeddings",
	"memory_meta",
}

var requiredObservationColumns = []string{
	"session_key",
	"channel",
	"chat_id",
	"peer_kind",
	"chat_label",
	"role",
	"sender_id",
	"source_kind",
	"source_key",
	"content",
	"created_at_ms",
}

func (i *Index) ensureSchema(ctx context.Context) (string, error) {
	missingBefore, err := i.missingRequiredSchema(ctx)
	if err != nil {
		return "", err
	}

	for _, stmt := range []string{
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
	} {
		if _, err := i.db.ExecContext(ctx, stmt); err != nil {
			return "", fmt.Errorf("memoryindex: init schema: %w", err)
		}
	}

	if err := i.ensureObservationColumns(ctx); err != nil {
		return "", err
	}
	if _, err := i.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_observations_source ON observations(source_kind, source_key);`); err != nil {
		return "", fmt.Errorf("memoryindex: init schema: %w", err)
	}

	missingAfter, err := i.missingRequiredSchema(ctx)
	if err != nil {
		return "", err
	}
	if len(missingAfter) > 0 {
		return "", fmt.Errorf("memoryindex: schema validation failed: missing %s", strings.Join(missingAfter, ", "))
	}
	if len(missingBefore) > 0 {
		return schemaStatusRepaired, nil
	}
	return schemaStatusReady, nil
}

func (i *Index) ensureObservationColumns(ctx context.Context) error {
	columns, err := i.tableColumns(ctx, "observations")
	if err != nil {
		return err
	}
	migrations := map[string]string{
		"source_kind": `ALTER TABLE observations ADD COLUMN source_kind TEXT NOT NULL DEFAULT ''`,
		"source_key":  `ALTER TABLE observations ADD COLUMN source_key TEXT NOT NULL DEFAULT ''`,
		"peer_kind":   `ALTER TABLE observations ADD COLUMN peer_kind TEXT NOT NULL DEFAULT ''`,
		"chat_label":  `ALTER TABLE observations ADD COLUMN chat_label TEXT NOT NULL DEFAULT ''`,
	}
	for column, stmt := range migrations {
		if columns[column] {
			continue
		}
		if _, err := i.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("memoryindex: migrate schema: %w", err)
		}
	}
	return nil
}

func (i *Index) missingRequiredSchema(ctx context.Context) ([]string, error) {
	missing := make([]string, 0)
	for _, table := range requiredTables {
		ok, err := i.tableExists(ctx, table)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, "table:"+table)
		}
	}

	columns, err := i.tableColumns(ctx, "observations")
	if err != nil {
		return nil, err
	}
	for _, column := range requiredObservationColumns {
		if !columns[column] {
			missing = append(missing, "observations."+column)
		}
	}
	return missing, nil
}

func (i *Index) tableExists(ctx context.Context, table string) (bool, error) {
	var exists int
	err := i.db.QueryRowContext(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`,
		table,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("memoryindex: check table %s: %w", table, err)
	}
	return exists == 1, nil
}

func (i *Index) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	ok, err := i.tableExists(ctx, table)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]bool{}, nil
	}

	rows, err := i.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, fmt.Errorf("memoryindex: pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid      int
			name     string
			typ      string
			notNull  int
			defaultV sql.NullString
			pk       int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultV, &pk); err != nil {
			return nil, fmt.Errorf("memoryindex: scan pragma table_info(%s): %w", table, err)
		}
		columns[strings.TrimSpace(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: pragma table_info(%s) rows: %w", table, err)
	}
	return columns, nil
}
