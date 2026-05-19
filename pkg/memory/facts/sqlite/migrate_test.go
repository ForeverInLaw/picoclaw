package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrate_FreshDB(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "facts.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var name string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='facts'`).Scan(&name)
	if err != nil {
		t.Fatalf("facts table missing: %v", err)
	}
	if name != "facts" {
		t.Fatalf("got %q want facts", name)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()

	for i := range 3 {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatalf("Migrate iter %d: %v", i, err)
		}
	}
}

func TestMigrate_FTSTable(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "facts.db")
	db, _ := sql.Open("sqlite", dsn)
	defer db.Close()

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='facts_fts'`)
	var name string
	if err := row.Scan(&name); err != nil {
		t.Fatalf("facts_fts missing: %v", err)
	}
}
