package seahorse

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type canonicalPlan struct {
	Digests []string `json:"digests"`
	Keys    []string `json:"keys"`
	Summary string   `json:"summary"`
}

// ReconcileCanonical imports only the caller's effective active generation.
// A durable plan precedes source writes; the checkpoint follows publication.
// Original evidence and summary DAG nodes are never deleted by reconciliation.
func (e *Engine) ReconcileCanonical(ctx context.Context, key string, messages []Message, summary string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	mu := e.getSessionMutex(key)
	mu.Lock()
	defer mu.Unlock()
	return retrySourceWrite(ctx, func() error {
		return commitContext(ctx, func() error {
			conv, err := e.store.GetOrCreateConversation(ctx, key)
			if err != nil {
				return err
			}
			plan := canonicalPlan{
				Summary: summary,
				Digests: make([]string, len(messages)),
				Keys:    make([]string, len(messages)),
			}
			for i, msg := range messages {
				if contextErr := ctx.Err(); contextErr != nil {
					return contextErr
				}
				digest, digestErr := canonicalDigest(msg, key, i)
				if digestErr != nil {
					return digestErr
				}
				plan.Digests[i] = digest
			}
			data, err := json.Marshal(struct {
				Digests []string `json:"digests"`
				Summary string   `json:"summary"`
			}{plan.Digests, summary})
			if err != nil {
				return err
			}
			fingerprint := fmt.Sprintf("%x", sha256.Sum256(data))
			importID, stored, err := e.store.canonicalPlan(ctx, conv.ConversationID, fingerprint, plan, key)
			if err != nil {
				return err
			}
			plan = stored
			var current bool
			if err = e.store.db.QueryRowContext(
				ctx,
				`SELECT EXISTS(SELECT 1 FROM context_checkpoints cp JOIN context_imports ci USING(import_id) WHERE cp.conversation_id=? AND cp.import_id=? AND ci.completed=1)`,
				conv.ConversationID,
				importID,
			).Scan(&current); err != nil {
				return err
			}
			if current {
				return nil
			}
			known, err := e.store.canonicalSourceKeys(ctx, conv.ConversationID)
			if err != nil {
				return err
			}
			for i, msg := range messages {
				if known[plan.Keys[i]] {
					continue
				}
				if err := e.store.appendSource(ctx, conv.ConversationID, plan.Keys[i], msg); err != nil {
					return err
				}
			}
			return e.store.publishCanonical(ctx, conv.ConversationID, importID, plan)
		})
	})
}

func canonicalDigest(message Message, scope string, index int) (string, error) {
	var payload []byte
	if len(message.Payload) > 0 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(message.Payload, &fields); err != nil {
			return "", fmt.Errorf("canonical source payload: %w", err)
		}
		// IVO-103 used an ordinal fallback ID in payloads. It is an engine reference,
		// not external provenance, and must not change equality after truncation.
		generated, _ := json.Marshal(fmt.Sprintf("%x:%d", sha256.Sum256([]byte(scope)), index))
		if string(fields["source_id"]) == string(generated) {
			delete(fields, "source_id")
		}
		var err error
		payload, err = json.Marshal(fields)
		if err != nil {
			return "", err
		}
	} else {
		message.ID = 0
		message.ConversationID = 0
		message.TokenCount = 0
		message.CreatedAt = normalizeMessageCreatedAt(message.CreatedAt)
		message.Parts = append([]MessagePart(nil), message.Parts...)
		for i := range message.Parts {
			message.Parts[i].ID = 0
			message.Parts[i].MessageID = 0
		}
		var err error
		payload, err = json.Marshal(message)
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func (s *Store) canonicalPlan(
	ctx context.Context,
	convID int64,
	fingerprint string,
	plan canonicalPlan,
	scope string,
) (int64, canonicalPlan, error) {
	var id int64
	var encoded string
	err := s.db.QueryRowContext(ctx, "SELECT import_id,plan FROM context_imports WHERE conversation_id=? AND fingerprint=?", convID, fingerprint).
		Scan(&id, &encoded)
	if err == nil {
		if err = json.Unmarshal([]byte(encoded), &plan); err != nil {
			return 0, plan, err
		}
		return id, plan, nil
	}
	if err != sql.ErrNoRows {
		return 0, plan, err
	}
	var previous canonicalPlan
	err = s.db.QueryRowContext(ctx, "SELECT plan FROM context_imports WHERE conversation_id=? ORDER BY import_id DESC LIMIT 1", convID).
		Scan(&encoded)
	if err == nil {
		if err = json.Unmarshal([]byte(encoded), &previous); err != nil {
			return 0, plan, err
		}
	} else if err != sql.ErrNoRows {
		return 0, plan, err
	} else {
		// Upgrade sources already persisted by the selectable IVO-103 engine.
		rows, queryErr := s.db.QueryContext(
			ctx,
			"SELECT source_key,message_id FROM source_messages WHERE conversation_id=? ORDER BY message_id",
			convID,
		)
		if queryErr != nil {
			return 0, plan, queryErr
		}
		defer rows.Close()
		type source struct {
			key string
			id  int64
		}
		var sources []source
		for rows.Next() {
			var item source
			if err = rows.Scan(&item.key, &item.id); err != nil {
				rows.Close()
				return 0, plan, err
			}
			sources = append(sources, item)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return 0, plan, err
		}
		rows.Close()
		for i, item := range sources {
			msg, messageErr := s.GetMessageByID(ctx, item.id)
			if messageErr != nil {
				return 0, plan, messageErr
			}
			ordinal := i
			if strings.HasPrefix(item.key, "canonical:") {
				ordinal, _ = strconv.Atoi(strings.TrimPrefix(item.key, "canonical:"))
			}
			digest, err := canonicalDigest(*msg, scope, ordinal)
			if err != nil {
				return 0, plan, err
			}
			previous.Digests = append(previous.Digests, digest)
			previous.Keys = append(previous.Keys, item.key)
		}
	}
	reuseCanonicalKeys(previous, &plan)
	if err = s.reuseRevisionKeys(ctx, convID, &plan); err != nil {
		return 0, plan, err
	}
	for i := range plan.Keys {
		if plan.Keys[i] == "" {
			plan.Keys[i] = fmt.Sprintf("import:%s:%d", fingerprint, i)
		}
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return 0, plan, err
	}
	result, err := s.db.ExecContext(
		ctx,
		"INSERT INTO context_imports(conversation_id,fingerprint,plan) VALUES(?,?,?)",
		convID,
		fingerprint,
		string(data),
	)
	if err != nil {
		return 0, plan, err
	}
	id, err = result.LastInsertId()
	return id, plan, err
}

func reuseCanonicalKeys(previous canonicalPlan, next *canonicalPlan) {
	prefix := 0
	for prefix < min(len(previous.Digests), len(next.Digests)) && previous.Digests[prefix] == next.Digests[prefix] {
		prefix++
	}
	if prefix == len(previous.Digests) || previous.Summary == next.Summary {
		copy(next.Keys[:prefix], previous.Keys[:prefix])
		if prefix > 0 {
			return
		}
	}
	// Longest retained suffix becomes the new prefix after legacy compaction.
	combined := append(append(append([]string(nil), next.Digests...), "#"), previous.Digests...)
	matched := make([]int, len(combined))
	for i := 1; i < len(combined); i++ {
		length := matched[i-1]
		for length > 0 && combined[i] != combined[length] {
			length = matched[length-1]
		}
		if combined[i] == combined[length] {
			length++
		}
		matched[i] = length
	}
	overlap := 0
	if len(matched) > 0 {
		overlap = min(len(next.Digests), matched[len(matched)-1])
	}
	copy(next.Keys[:overlap], previous.Keys[len(previous.Keys)-overlap:])
}

func (s *Store) canonicalSourceKeys(ctx context.Context, convID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT source_key FROM source_messages WHERE conversation_id=?", convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = true
	}
	return keys, rows.Err()
}

// Ordinary append plans are replaced by the new checkpoint. Retained revision
// boundaries keep keys recoverable after a legacy rewrite or snapshot restore.
func (s *Store) reuseRevisionKeys(ctx context.Context, convID int64, next *canonicalPlan) error {
	rows, err := s.db.QueryContext(ctx,
		"SELECT plan FROM context_imports WHERE conversation_id=? AND retained=1 ORDER BY import_id DESC", convID)
	if err != nil {
		return err
	}
	defer rows.Close()
	used := map[string]bool{}
	for _, key := range next.Keys {
		if key != "" {
			used[key] = true
		}
	}
	for rows.Next() {
		var encoded string
		if err = rows.Scan(&encoded); err != nil {
			return err
		}
		var previous canonicalPlan
		if err = json.Unmarshal([]byte(encoded), &previous); err != nil {
			return err
		}
		hint := canonicalPlan{Digests: next.Digests, Summary: next.Summary, Keys: make([]string, len(next.Keys))}
		reuseCanonicalKeys(previous, &hint)
		for i, key := range hint.Keys {
			if key != "" && next.Keys[i] == "" && !used[key] {
				next.Keys[i] = key
				used[key] = true
			}
		}
	}
	return rows.Err()
}

func canonicalExtends(previous, next canonicalPlan) bool {
	if previous.Summary != next.Summary || len(previous.Digests) > len(next.Digests) {
		return false
	}
	for i, digest := range previous.Digests {
		if digest != next.Digests[i] {
			return false
		}
	}
	return true
}

func (s *Store) publishCanonical(ctx context.Context, convID, importID int64, plan canonicalPlan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, "SELECT import_id FROM context_checkpoints WHERE conversation_id=?", convID).
		Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if current == importID {
		return nil
	}
	if current != 0 {
		var encoded string
		if err = tx.QueryRowContext(ctx, "SELECT plan FROM context_imports WHERE import_id=?", current).
			Scan(&encoded); err != nil {
			return err
		}
		var previous canonicalPlan
		if err = json.Unmarshal([]byte(encoded), &previous); err != nil {
			return err
		}
		if !canonicalExtends(previous, plan) {
			if _, err = tx.ExecContext(
				ctx,
				"UPDATE context_imports SET retained=1 WHERE import_id IN (?,?)",
				current,
				importID,
			); err != nil {
				return err
			}
		}
	}
	// Drop only raw active projection entries no longer present in the effective
	// canonical history. Source rows and all existing summary DAG nodes survive.
	wanted := make(map[string]bool, len(plan.Keys))
	for _, key := range plan.Keys {
		wanted[key] = true
	}
	rows, err := tx.QueryContext(
		ctx,
		"SELECT ci.message_id,sm.source_key FROM context_items ci JOIN source_messages sm ON sm.message_id=ci.message_id WHERE ci.conversation_id=? AND ci.item_type='message'",
		convID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	var removed []int64
	for rows.Next() {
		var id int64
		var key string
		if err = rows.Scan(&id, &key); err != nil {
			rows.Close()
			return err
		}
		if !wanted[key] {
			removed = append(removed, id)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range removed {
		if _, err = tx.ExecContext(
			ctx,
			"DELETE FROM context_items WHERE conversation_id=? AND message_id=? AND item_type='message'",
			convID,
			id,
		); err != nil {
			return err
		}
	}
	if err = publishImportedSummary(ctx, tx, convID, plan.Summary, removed); err != nil {
		return err
	}
	if err = rebuildCanonicalProjection(ctx, tx, convID, plan.Keys); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE context_imports SET completed=1 WHERE import_id=?", importID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(
		ctx,
		"INSERT INTO context_checkpoints(conversation_id,import_id) VALUES(?,?) ON CONFLICT(conversation_id) DO UPDATE SET import_id=excluded.import_id",
		convID,
		importID,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(
		ctx,
		"DELETE FROM context_imports WHERE conversation_id=? AND import_id<>? AND retained=0",
		convID,
		importID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// Imported summary replacement changes only active projection entries. Existing
// summary records and their source links remain available for explicit recovery.
func publishImportedSummary(ctx context.Context, tx *sql.Tx, convID int64, content string, removed []int64) error {
	id := ""
	if content != "" {
		id = fmt.Sprintf("sum_%x", sha256.Sum256([]byte(fmt.Sprint(convID)+":"+content)))
	}
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM context_items WHERE conversation_id=? AND summary_id IN (SELECT summary_id FROM summaries WHERE kind='imported' AND summary_id<>?)`,
		convID,
		id,
	); err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	tokens := EstimateMessageTokens(Message{Content: content})
	if _, err := tx.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO summaries(summary_id,conversation_id,kind,depth,content,token_count,model) VALUES(?,?,'imported',0,?,?,'legacy')`,
		id,
		convID,
		content,
		tokens,
	); err != nil {
		return err
	}
	var represented bool
	if err := tx.QueryRowContext(ctx, activeSummaryGraph+` SELECT EXISTS(SELECT 1 FROM active_summaries WHERE summary_id=?)`, convID, id).
		Scan(&represented); err != nil {
		return err
	}
	if !represented {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO context_items(conversation_id,ordinal,item_type,summary_id,token_count) SELECT ?,COALESCE(MIN(ordinal),0)-?,'summary',?,? FROM context_items WHERE conversation_id=?`,
			convID,
			OrdinalStep,
			id,
			tokens,
			convID,
		); err != nil {
			return err
		}
	}
	for i, messageID := range removed {
		if _, err := tx.ExecContext(
			ctx,
			"INSERT OR IGNORE INTO summary_messages(summary_id,message_id,ordinal) VALUES(?,?,?)",
			id,
			messageID,
			i,
		); err != nil {
			return err
		}
	}
	return nil
}

const activeSummaryGraph = `WITH RECURSIVE active_summaries(summary_id) AS (
	SELECT summary_id FROM context_items WHERE conversation_id=? AND item_type='summary'
	UNION SELECT sp.parent_summary_id FROM summary_parents sp JOIN active_summaries a ON sp.summary_id=a.summary_id
)`

// Rebuild only the raw projection in canonical order. Sources already represented
// by a retained active summary are kept compressed, including condensed DAGs.
func rebuildCanonicalProjection(ctx context.Context, tx *sql.Tx, convID int64, keys []string) error {
	rows, err := tx.QueryContext(
		ctx,
		activeSummaryGraph+` SELECT DISTINCT message_id FROM summary_messages JOIN active_summaries USING(summary_id)`,
		convID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	covered := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		covered[id] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err = tx.ExecContext(
		ctx,
		"DELETE FROM context_items WHERE conversation_id=? AND item_type='message'",
		convID,
	); err != nil {
		return err
	}
	for _, key := range keys {
		var id int64
		var tokens int
		if err = tx.QueryRowContext(
			ctx,
			`SELECT sm.message_id,m.token_count FROM source_messages sm JOIN messages m ON m.message_id=sm.message_id WHERE sm.conversation_id=? AND sm.source_key=?`,
			convID,
			key,
		).Scan(&id, &tokens); err != nil {
			return err
		}
		if covered[id] {
			continue
		}
		if _, err = tx.ExecContext(
			ctx,
			`INSERT INTO context_items(conversation_id,ordinal,item_type,message_id,token_count) SELECT ?,COALESCE(MAX(ordinal),0)+?,'message',?,? FROM context_items WHERE conversation_id=?`,
			convID,
			OrdinalStep,
			id,
			tokens,
			convID,
		); err != nil {
			return err
		}
	}
	return nil
}
