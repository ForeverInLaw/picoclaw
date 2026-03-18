package memoryindex

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (i *Index) UpsertChatRollup(ctx context.Context, record RollupRecord) error {
	if i == nil || i.db == nil {
		return nil
	}
	record.Channel = strings.TrimSpace(record.Channel)
	record.ChatID = strings.TrimSpace(record.ChatID)
	record.Summary = strings.TrimSpace(record.Summary)
	if record.Channel == "" || record.ChatID == "" || record.WindowStart.IsZero() || record.WindowEnd.IsZero() || record.Summary == "" {
		return nil
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now

	_, err := i.db.ExecContext(
		ctx,
		`INSERT INTO chat_rollups(channel, chat_id, window_start_ms, window_end_ms, source_count, last_observation_ms, summary, created_at_ms, updated_at_ms)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(channel, chat_id, window_start_ms, window_end_ms) DO UPDATE SET
			source_count = excluded.source_count,
			last_observation_ms = excluded.last_observation_ms,
			summary = excluded.summary,
			updated_at_ms = excluded.updated_at_ms`,
		record.Channel,
		record.ChatID,
		record.WindowStart.UnixMilli(),
		record.WindowEnd.UnixMilli(),
		record.SourceCount,
		record.LastObservationAt.UnixMilli(),
		record.Summary,
		record.CreatedAt.UnixMilli(),
		record.UpdatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("memoryindex: upsert chat rollup: %w", err)
	}
	return nil
}

func (i *Index) ListChatRollups(ctx context.Context, scope ChatScope, window TimeWindow) ([]RollupRecord, error) {
	if i == nil || i.db == nil {
		return nil, nil
	}
	scope.Channel = strings.TrimSpace(scope.Channel)
	scope.ChatID = strings.TrimSpace(scope.ChatID)
	if scope.Channel == "" || scope.ChatID == "" {
		return nil, nil
	}

	query := `SELECT channel, chat_id, window_start_ms, window_end_ms, source_count, last_observation_ms, summary, created_at_ms, updated_at_ms
		FROM chat_rollups
		WHERE channel = ? AND chat_id = ?`
	args := []any{scope.Channel, scope.ChatID}
	if !window.Start.IsZero() {
		query += ` AND window_end_ms > ?`
		args = append(args, window.Start.UnixMilli())
	}
	if !window.End.IsZero() {
		query += ` AND window_start_ms < ?`
		args = append(args, window.End.UnixMilli())
	}
	query += ` ORDER BY window_start_ms ASC`

	rows, err := i.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: query chat rollups: %w", err)
	}
	defer rows.Close()

	results := make([]RollupRecord, 0)
	for rows.Next() {
		var (
			record            RollupRecord
			windowStartMS     int64
			windowEndMS       int64
			lastObservationMS int64
			createdMS         int64
			updatedMS         int64
		)
		if err := rows.Scan(
			&record.Channel,
			&record.ChatID,
			&windowStartMS,
			&windowEndMS,
			&record.SourceCount,
			&lastObservationMS,
			&record.Summary,
			&createdMS,
			&updatedMS,
		); err != nil {
			return nil, fmt.Errorf("memoryindex: scan chat rollup: %w", err)
		}
		record.WindowStart = time.UnixMilli(windowStartMS)
		record.WindowEnd = time.UnixMilli(windowEndMS)
		record.LastObservationAt = time.UnixMilli(lastObservationMS)
		record.CreatedAt = time.UnixMilli(createdMS)
		record.UpdatedAt = time.UnixMilli(updatedMS)
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: chat rollup rows: %w", err)
	}
	return results, nil
}

func (i *Index) ListObservationsInWindow(ctx context.Context, scope ChatScope, window TimeWindow, limit int) ([]Hit, error) {
	if i == nil || i.db == nil {
		return nil, nil
	}
	scope.Channel = strings.TrimSpace(scope.Channel)
	scope.ChatID = strings.TrimSpace(scope.ChatID)
	if scope.Channel == "" || scope.ChatID == "" {
		return nil, nil
	}

	query := `SELECT session_key, channel, chat_id, peer_kind, chat_label, role, sender_id, content, created_at_ms
		FROM observations
		WHERE channel = ? AND chat_id = ?`
	args := []any{scope.Channel, scope.ChatID}
	if !window.Start.IsZero() {
		query += ` AND created_at_ms >= ?`
		args = append(args, window.Start.UnixMilli())
	}
	if !window.End.IsZero() {
		query += ` AND created_at_ms < ?`
		args = append(args, window.End.UnixMilli())
	}
	query += ` ORDER BY created_at_ms ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := i.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: query observations in window: %w", err)
	}
	defer rows.Close()

	return scanHits(rows, i.cfg.MaxSnippetChars)
}
