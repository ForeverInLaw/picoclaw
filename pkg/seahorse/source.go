package seahorse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqliteCodes "modernc.org/sqlite/lib"
)

// SyncSources appends canonical source messages idempotently. Each message,
// structured parts, provenance and context item commit in one transaction.
// Compaction never deletes those source records.
func (e *Engine) SyncSources(ctx context.Context, key string, messages []Message) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	mu := e.getSessionMutex(key)
	mu.Lock()
	defer mu.Unlock()
	// Retry outside the generation gate. Committed prefixes are idempotent, so
	// reset can advance between retries without permitting a late source write.
	return retrySourceWrite(ctx, func() error {
		return commitContext(ctx, func() error {
			conv, err := e.store.GetOrCreateConversation(ctx, key)
			if err != nil {
				return err
			}
			for index, msg := range messages {
				if err := e.store.appendSource(
					ctx,
					conv.ConversationID,
					fmt.Sprintf("canonical:%d", index),
					msg,
				); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func (s *Store) appendSource(ctx context.Context, convID int64, key string, msg Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing int64
	lookupErr := tx.QueryRowContext(ctx, "SELECT message_id FROM source_messages WHERE conversation_id=? AND source_key=?", convID, key).
		Scan(&existing)
	if lookupErr == nil {
		return nil
	}
	if lookupErr != sql.ErrNoRows {
		return lookupErr
	}
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO messages (conversation_id,role,content,model_name,reasoning_content,token_count,created_at) VALUES (?,?,?,?,?,?,?)`,
		convID,
		msg.Role,
		msg.Content,
		msg.ModelName,
		msg.ReasoningContent,
		msg.TokenCount,
		formatSQLiteTime(msg.CreatedAt),
	)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for ordinal, part := range msg.Parts {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO message_parts (message_id,type,text,name,arguments,tool_call_id,media_uri,mime_type,ordinal) VALUES (?,?,?,?,?,?,?,?,?)`,
			id,
			part.Type,
			part.Text,
			part.Name,
			part.Arguments,
			part.ToolCallID,
			part.MediaURI,
			part.MimeType,
			ordinal,
		); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(
		ctx,
		"INSERT INTO source_messages (conversation_id,source_key,message_id,payload) VALUES (?,?,?,?)",
		convID,
		key,
		id,
		string(msg.Payload),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO context_items (conversation_id,ordinal,item_type,message_id,token_count) SELECT ?,COALESCE(MAX(ordinal),0)+?,'message',?,? FROM context_items WHERE conversation_id=?`,
		convID,
		OrdinalStep,
		id,
		msg.TokenCount,
		convID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) loadPayload(ctx context.Context, msg *Message) error {
	var payload string
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM source_messages WHERE message_id=?", msg.ID).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	msg.Payload = json.RawMessage(payload)
	return nil
}

// Retrying a short native busy wait keeps cancellation responsive while still
// tolerating concurrent SQLite writers. Each source append is transactional.
func retrySourceWrite(ctx context.Context, write func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := write()
		if err == nil {
			return nil
		}
		var sqliteError *sqlite.Error
		if !errors.As(err, &sqliteError) {
			return err
		}
		code := sqliteError.Code() & 0xff
		if code != sqliteCodes.SQLITE_BUSY && code != sqliteCodes.SQLITE_LOCKED {
			return err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
