package extract

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Logger reads and writes the extraction_log table.
type Logger struct{ DB *sql.DB }

func (l *Logger) LastProcessed(ctx context.Context, sessionKey string) (int, error) {
	row := l.DB.QueryRowContext(ctx,
		`SELECT last_processed_msg_idx FROM extraction_log WHERE session_key = ?`,
		sessionKey)
	var n int
	if err := row.Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

func (l *Logger) Mark(ctx context.Context, sessionKey string, idx int) error {
	_, err := l.DB.ExecContext(ctx, `
		INSERT INTO extraction_log(session_key, last_processed_msg_idx, last_run_at)
		VALUES (?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			last_processed_msg_idx = excluded.last_processed_msg_idx,
			last_run_at = excluded.last_run_at`,
		sessionKey, idx, time.Now().UTC())
	return err
}

func (l *Logger) RecordFailure(ctx context.Context, sessionKey, msgRange, errMsg string) error {
	_, err := l.DB.ExecContext(ctx, `
		INSERT INTO failed_extractions(session_key, msg_range, error, failed_at)
		VALUES (?, ?, ?, ?)`,
		sessionKey, msgRange, errMsg, time.Now().UTC())
	return err
}
