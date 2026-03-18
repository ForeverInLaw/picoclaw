package memoryindex

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

func (i *Index) UpsertChatCatalog(ctx context.Context, entry ChatCatalogEntry) error {
	if i == nil || i.db == nil {
		return nil
	}
	entry.Channel = strings.TrimSpace(entry.Channel)
	entry.ChatID = strings.TrimSpace(entry.ChatID)
	entry.PeerKind = strings.TrimSpace(entry.PeerKind)
	entry.Label = strings.TrimSpace(entry.Label)
	if entry.Channel == "" || entry.ChatID == "" {
		return nil
	}
	if entry.LastSeen.IsZero() {
		entry.LastSeen = time.Now().UTC()
	}

	_, err := i.db.ExecContext(
		ctx,
		`INSERT INTO chat_catalog(channel, chat_id, peer_kind, label, last_seen_ms)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(channel, chat_id) DO UPDATE SET
			peer_kind = CASE WHEN excluded.peer_kind <> '' THEN excluded.peer_kind ELSE chat_catalog.peer_kind END,
			label = CASE WHEN excluded.label <> '' THEN excluded.label ELSE chat_catalog.label END,
			last_seen_ms = CASE WHEN excluded.last_seen_ms > chat_catalog.last_seen_ms THEN excluded.last_seen_ms ELSE chat_catalog.last_seen_ms END`,
		entry.Channel,
		entry.ChatID,
		entry.PeerKind,
		entry.Label,
		entry.LastSeen.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("memoryindex: upsert chat catalog: %w", err)
	}
	return nil
}

func (i *Index) UpsertChatParticipant(ctx context.Context, record ChatParticipantRecord) error {
	if i == nil || i.db == nil {
		return nil
	}
	record.Channel = strings.TrimSpace(record.Channel)
	record.ChatID = strings.TrimSpace(record.ChatID)
	record.SenderID = strings.TrimSpace(record.SenderID)
	record.Label = strings.TrimSpace(record.Label)
	if record.Channel == "" || record.ChatID == "" || record.SenderID == "" {
		return nil
	}
	if record.LastSeenAt.IsZero() {
		record.LastSeenAt = time.Now().UTC()
	}

	_, err := i.db.ExecContext(
		ctx,
		`INSERT INTO chat_participants(channel, chat_id, sender_id, label, last_seen_ms)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(channel, chat_id, sender_id) DO UPDATE SET
			label = CASE WHEN excluded.label <> '' THEN excluded.label ELSE chat_participants.label END,
			last_seen_ms = CASE WHEN excluded.last_seen_ms > chat_participants.last_seen_ms THEN excluded.last_seen_ms ELSE chat_participants.last_seen_ms END`,
		record.Channel,
		record.ChatID,
		record.SenderID,
		record.Label,
		record.LastSeenAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("memoryindex: upsert chat participant: %w", err)
	}
	return nil
}

func (i *Index) SyncChatAliases(ctx context.Context, aliases []ChatAliasRecord) error {
	if i == nil || i.db == nil {
		return nil
	}

	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memoryindex: begin alias sync tx: %w", err)
	}
	defer tx.Rollback()

	normalized := make([]ChatAliasRecord, 0, len(aliases))
	for _, alias := range aliases {
		alias.Alias = normalizeAlias(alias.Alias)
		alias.Channel = strings.TrimSpace(alias.Channel)
		alias.ChatID = strings.TrimSpace(alias.ChatID)
		alias.Label = strings.TrimSpace(alias.Label)
		alias.AllowedRequesters = compactStrings(alias.AllowedRequesters)
		if alias.Alias == "" || alias.Channel == "" || alias.ChatID == "" {
			continue
		}
		normalized = append(normalized, alias)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_aliases`); err != nil {
		return fmt.Errorf("memoryindex: clear chat aliases: %w", err)
	}

	for _, alias := range normalized {
		data, err := json.Marshal(alias.AllowedRequesters)
		if err != nil {
			return fmt.Errorf("memoryindex: marshal chat alias requesters: %w", err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO chat_aliases(alias, channel, chat_id, label, allowed_requesters_json)
			 VALUES(?, ?, ?, ?, ?)`,
			alias.Alias,
			alias.Channel,
			alias.ChatID,
			alias.Label,
			string(data),
		); err != nil {
			return fmt.Errorf("memoryindex: upsert chat alias: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memoryindex: commit alias sync tx: %w", err)
	}
	return nil
}

func (i *Index) ListAccessibleChats(ctx context.Context, requesterID string) ([]ChatAccessEntry, error) {
	if i == nil || i.db == nil {
		return nil, nil
	}

	catalogRows, err := i.db.QueryContext(
		ctx,
		`SELECT channel, chat_id, peer_kind, label, last_seen_ms
		 FROM chat_catalog`,
	)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: query chat catalog: %w", err)
	}
	defer catalogRows.Close()

	entriesByKey := make(map[string]ChatAccessEntry)
	for catalogRows.Next() {
		var (
			entry      ChatAccessEntry
			lastSeenMS int64
		)
		if err := catalogRows.Scan(&entry.Channel, &entry.ChatID, &entry.PeerKind, &entry.Label, &lastSeenMS); err != nil {
			return nil, fmt.Errorf("memoryindex: scan chat catalog: %w", err)
		}
		entry.LastSeen = time.UnixMilli(lastSeenMS)
		entriesByKey[entry.Channel+"\x00"+entry.ChatID] = entry
	}
	if err := catalogRows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: chat catalog rows: %w", err)
	}

	if strings.TrimSpace(requesterID) == "" {
		return nil, nil
	}

	requesterID = strings.TrimSpace(requesterID)
	participantRows, err := i.db.QueryContext(
		ctx,
		`SELECT channel, chat_id FROM chat_participants WHERE sender_id = ?`,
		requesterID,
	)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: query participant chats: %w", err)
	}
	defer participantRows.Close()

	accessible := make(map[string]ChatAccessEntry)
	for participantRows.Next() {
		var channel, chatID string
		if err := participantRows.Scan(&channel, &chatID); err != nil {
			return nil, fmt.Errorf("memoryindex: scan participant chat: %w", err)
		}
		key := channel + "\x00" + chatID
		if entry, ok := entriesByKey[key]; ok {
			accessible[key] = entry
		}
	}
	if err := participantRows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: participant chat rows: %w", err)
	}

	aliasRows, err := i.db.QueryContext(
		ctx,
		`SELECT alias, channel, chat_id, label, allowed_requesters_json FROM chat_aliases`,
	)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: query chat aliases: %w", err)
	}
	defer aliasRows.Close()

	for aliasRows.Next() {
		var (
			alias          string
			channel        string
			chatID         string
			label          string
			requestersJSON string
			requesters     []string
		)
		if err := aliasRows.Scan(&alias, &channel, &chatID, &label, &requestersJSON); err != nil {
			return nil, fmt.Errorf("memoryindex: scan chat alias: %w", err)
		}
		if err := json.Unmarshal([]byte(requestersJSON), &requesters); err != nil {
			return nil, fmt.Errorf("memoryindex: decode chat alias requesters: %w", err)
		}
		if !slices.Contains(requesters, requesterID) {
			continue
		}
		key := channel + "\x00" + chatID
		entry, ok := entriesByKey[key]
		if !ok {
			entry = ChatAccessEntry{
				ChatCatalogEntry: ChatCatalogEntry{
					Channel: channel,
					ChatID:  chatID,
					Label:   label,
				},
			}
		}
		if entry.Label == "" {
			entry.Label = label
		}
		if entry.Alias == "" {
			entry.Alias = alias
		}
		accessible[key] = entry
	}
	if err := aliasRows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: chat alias rows: %w", err)
	}

	results := make([]ChatAccessEntry, 0, len(accessible))
	for _, entry := range accessible {
		results = append(results, entry)
	}
	slices.SortFunc(results, func(a, b ChatAccessEntry) int {
		left := firstCatalogValue(a.Label, a.Alias, a.ChatID)
		right := firstCatalogValue(b.Label, b.Alias, b.ChatID)
		switch {
		case left < right:
			return -1
		case left > right:
			return 1
		case a.Channel < b.Channel:
			return -1
		case a.Channel > b.Channel:
			return 1
		case a.ChatID < b.ChatID:
			return -1
		case a.ChatID > b.ChatID:
			return 1
		default:
			return 0
		}
	})
	return results, nil
}

func (i *Index) ListChatParticipants(ctx context.Context, scope ChatScope) ([]ChatParticipantRecord, error) {
	if i == nil || i.db == nil {
		return nil, nil
	}
	scope.Channel = strings.TrimSpace(scope.Channel)
	scope.ChatID = strings.TrimSpace(scope.ChatID)
	if scope.Channel == "" || scope.ChatID == "" {
		return nil, nil
	}

	rows, err := i.db.QueryContext(
		ctx,
		`SELECT channel, chat_id, sender_id, label, last_seen_ms
		 FROM chat_participants
		 WHERE channel = ? AND chat_id = ?
		 ORDER BY CASE WHEN label = '' THEN 1 ELSE 0 END, label ASC, sender_id ASC`,
		scope.Channel,
		scope.ChatID,
	)
	if err != nil {
		return nil, fmt.Errorf("memoryindex: query chat participants: %w", err)
	}
	defer rows.Close()

	results := make([]ChatParticipantRecord, 0)
	for rows.Next() {
		var (
			record     ChatParticipantRecord
			lastSeenMS int64
		)
		if err := rows.Scan(&record.Channel, &record.ChatID, &record.SenderID, &record.Label, &lastSeenMS); err != nil {
			return nil, fmt.Errorf("memoryindex: scan chat participant: %w", err)
		}
		record.LastSeenAt = time.UnixMilli(lastSeenMS)
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memoryindex: chat participant rows: %w", err)
	}
	return results, nil
}

func normalizeAlias(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func firstCatalogValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
