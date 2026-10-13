package memoryindex

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestIndex_RefreshRollupReplacesLegacyPartialHourRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	idx, err := Open(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idx.Close() }()
	hour := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	// Load the exact persisted shape left by older partial-hour summaries.
	legacy, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, minute := range []int{15, 20} {
		if _, err = legacy.ExecContext(
			t.Context(),
			`INSERT INTO chat_rollups VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"telegram",
			"42",
			hour.UnixMilli(),
			hour.Add(time.Duration(minute)*time.Minute).UnixMilli(),
			1,
			hour.UnixMilli(),
			"old partial",
			hour.UnixMilli(),
			hour.UnixMilli(),
		); err != nil {
			t.Fatal(err)
		}
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	for _, record := range []RollupRecord{
		{Channel: "telegram", ChatID: "other", WindowStart: hour, WindowEnd: hour.Add(time.Hour), Summary: "other chat"},
		{Channel: "telegram", ChatID: "42", WindowStart: hour.Add(-time.Hour), WindowEnd: hour, Summary: "previous hour"},
		{Channel: "telegram", ChatID: "42", WindowStart: hour, WindowEnd: hour.Add(30 * time.Minute), Summary: "latest partial"},
	} {
		if err = idx.UpsertChatRollup(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	if err = idx.Close(); err != nil {
		t.Fatal(err)
	}
	idx, err = Open(path, Config{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := idx.ListChatRollups(t.Context(), ChatScope{Channel: "telegram", ChatID: "42"}, TimeWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Summary != "previous hour" || rows[1].Summary != "latest partial" ||
		!rows[1].WindowEnd.Equal(hour.Add(30*time.Minute)) {
		t.Fatalf("replacement must remove legacy duplicates and preserve adjacent hours: %+v", rows)
	}
	other, err := idx.ListChatRollups(t.Context(), ChatScope{Channel: "telegram", ChatID: "other"}, TimeWindow{})
	if err != nil || len(other) != 1 || other[0].Summary != "other chat" {
		t.Fatalf("other chat changed: rows=%+v err=%v", other, err)
	}
}
