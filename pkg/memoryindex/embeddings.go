package memoryindex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type EmbeddingRow struct {
	ObservationID int64
	Hit           Hit
	Vector        []float32
}

type RollupEmbeddingRow struct {
	Record RollupRecord
	Vector []float32
}

func (i *Index) ListObservationsMissingEmbeddings(
	ctx context.Context,
	modelName string,
	limit int,
	minContentChars int,
) ([]struct {
	ID      int64
	Content string
}, error) {
	if i == nil || i.db == nil || strings.TrimSpace(modelName) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 32
	}
	if minContentChars < 0 {
		minContentChars = 0
	}
	rows, err := i.db.QueryContext(
		ctx,
		`SELECT o.id, o.content
		 FROM observations o
		 LEFT JOIN observation_embeddings e ON e.observation_id = o.id AND e.model_name = ?
		 WHERE e.observation_id IS NULL
		   AND LENGTH(TRIM(o.content)) >= ?
		 ORDER BY o.created_at_ms DESC
		 LIMIT ?`,
		modelName,
		minContentChars,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: list missing observation embeddings: %w", err)
	}
	defer rows.Close()

	results := make([]struct {
		ID      int64
		Content string
	}, 0)
	for rows.Next() {
		var row struct {
			ID      int64
			Content string
		}
		if err := rows.Scan(&row.ID, &row.Content); err != nil {
			return nil, fmt.Errorf("memoryindex: scan missing observation embedding: %w", err)
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: missing observation embedding rows: %w", err)
	}
	return results, nil
}

func (i *Index) LoadObservationEmbeddings(
	ctx context.Context,
	scopes []ChatScope,
	modelName string,
	since time.Time,
	until time.Time,
	limit int,
) ([]EmbeddingRow, error) {
	if i == nil || i.db == nil || strings.TrimSpace(modelName) == "" {
		return nil, nil
	}
	query := `SELECT o.id, o.session_key, o.channel, o.chat_id, o.peer_kind, o.chat_label, o.role, o.sender_id, o.content, o.created_at_ms, e.vector_json
		FROM observation_embeddings e
		JOIN observations o ON o.id = e.observation_id
		WHERE e.model_name = ?`
	args := []any{modelName}
	if len(scopes) > 0 {
		clauses := make([]string, 0, len(scopes))
		for _, scope := range scopes {
			clauses = append(clauses, "(o.channel = ? AND o.chat_id = ?)")
			args = append(args, scope.Channel, scope.ChatID)
		}
		query += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	if !since.IsZero() {
		query += ` AND o.created_at_ms >= ?`
		args = append(args, since.UnixMilli())
	}
	if !until.IsZero() {
		query += ` AND o.created_at_ms <= ?`
		args = append(args, until.UnixMilli())
	}
	query += ` ORDER BY o.created_at_ms DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := i.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: load observation embeddings: %w", err)
	}
	defer rows.Close()

	results := make([]EmbeddingRow, 0)
	for rows.Next() {
		var (
			row         EmbeddingRow
			createdAtMS int64
			vectorJSON  string
		)
		if err := rows.Scan(
			&row.ObservationID,
			&row.Hit.SessionKey,
			&row.Hit.Channel,
			&row.Hit.ChatID,
			&row.Hit.PeerKind,
			&row.Hit.ChatLabel,
			&row.Hit.Role,
			&row.Hit.SenderID,
			&row.Hit.Content,
			&createdAtMS,
			&vectorJSON,
		); err != nil {
			return nil, fmt.Errorf("memoryindex: scan observation embedding: %w", err)
		}
		row.Hit.CreatedAt = time.UnixMilli(createdAtMS)
		if err := json.Unmarshal([]byte(vectorJSON), &row.Vector); err != nil {
			return nil, fmt.Errorf("memoryindex: decode observation embedding: %w", err)
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: observation embedding rows: %w", err)
	}
	return results, nil
}

func (i *Index) UpsertObservationEmbedding(
	ctx context.Context,
	observationID int64,
	modelName string,
	vector []float32,
) error {
	if i == nil || i.db == nil || observationID <= 0 || strings.TrimSpace(modelName) == "" || len(vector) == 0 {
		return nil
	}
	data, err := json.Marshal(vector)
	if err != nil {
		return fmt.Errorf("memoryindex: marshal observation embedding: %w", err)
	}
	_, err = i.db.ExecContext(
		ctx,
		`INSERT INTO observation_embeddings(observation_id, model_name, dims, vector_json, updated_at_ms)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(observation_id) DO UPDATE SET
			model_name = excluded.model_name,
			dims = excluded.dims,
			vector_json = excluded.vector_json,
			updated_at_ms = excluded.updated_at_ms`,
		observationID,
		modelName,
		len(vector),
		string(data),
		time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("memoryindex: upsert observation embedding: %w", err)
	}
	return nil
}

func (i *Index) UpsertChatRollupEmbedding(
	ctx context.Context,
	record RollupRecord,
	modelName string,
	vector []float32,
) error {
	if i == nil || i.db == nil || strings.TrimSpace(modelName) == "" || len(vector) == 0 {
		return nil
	}
	data, err := json.Marshal(vector)
	if err != nil {
		return fmt.Errorf("memoryindex: marshal rollup embedding: %w", err)
	}
	_, err = i.db.ExecContext(
		ctx,
		`INSERT INTO chat_rollup_embeddings(channel, chat_id, window_start_ms, window_end_ms, model_name, dims, vector_json, updated_at_ms)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel, chat_id, window_start_ms, window_end_ms) DO UPDATE SET
			model_name = excluded.model_name,
			dims = excluded.dims,
			vector_json = excluded.vector_json,
			updated_at_ms = excluded.updated_at_ms`,
		record.Channel,
		record.ChatID,
		record.WindowStart.UnixMilli(),
		record.WindowEnd.UnixMilli(),
		modelName,
		len(vector),
		string(data),
		time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("memoryindex: upsert rollup embedding: %w", err)
	}
	return nil
}

func (i *Index) LoadChatRollupEmbeddings(
	ctx context.Context,
	scope ChatScope,
	modelName string,
	window TimeWindow,
) ([]RollupEmbeddingRow, error) {
	if i == nil || i.db == nil || strings.TrimSpace(modelName) == "" {
		return nil, nil
	}
	query := `SELECT r.channel, r.chat_id, r.window_start_ms, r.window_end_ms, r.source_count, r.last_observation_ms, r.summary, r.created_at_ms, r.updated_at_ms, e.vector_json
		FROM chat_rollup_embeddings e
		JOIN chat_rollups r
		  ON r.channel = e.channel AND r.chat_id = e.chat_id AND r.window_start_ms = e.window_start_ms AND r.window_end_ms = e.window_end_ms
		WHERE e.model_name = ? AND r.channel = ? AND r.chat_id = ?`
	args := []any{modelName, scope.Channel, scope.ChatID}
	if !window.Start.IsZero() {
		query += ` AND r.window_end_ms > ?`
		args = append(args, window.Start.UnixMilli())
	}
	if !window.End.IsZero() {
		query += ` AND r.window_start_ms < ?`
		args = append(args, window.End.UnixMilli())
	}
	query += ` ORDER BY r.window_start_ms ASC`

	rows, err := i.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: load rollup embeddings: %w", err)
	}
	defer rows.Close()

	results := make([]RollupEmbeddingRow, 0)
	for rows.Next() {
		var (
			row        RollupEmbeddingRow
			startMS    int64
			endMS      int64
			lastObsMS  int64
			createdMS  int64
			updatedMS  int64
			vectorJSON string
		)
		if err := rows.Scan(
			&row.Record.Channel,
			&row.Record.ChatID,
			&startMS,
			&endMS,
			&row.Record.SourceCount,
			&lastObsMS,
			&row.Record.Summary,
			&createdMS,
			&updatedMS,
			&vectorJSON,
		); err != nil {
			return nil, fmt.Errorf("memoryindex: scan rollup embedding: %w", err)
		}
		row.Record.WindowStart = time.UnixMilli(startMS)
		row.Record.WindowEnd = time.UnixMilli(endMS)
		row.Record.LastObservationAt = time.UnixMilli(lastObsMS)
		row.Record.CreatedAt = time.UnixMilli(createdMS)
		row.Record.UpdatedAt = time.UnixMilli(updatedMS)
		if err := json.Unmarshal([]byte(vectorJSON), &row.Vector); err != nil {
			return nil, fmt.Errorf("memoryindex: decode rollup embedding: %w", err)
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: rollup embedding rows: %w", err)
	}
	return results, nil
}
