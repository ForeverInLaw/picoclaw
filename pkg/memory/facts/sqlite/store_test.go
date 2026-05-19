package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
	_ "modernc.org/sqlite"
)

func newStoreT(t *testing.T) *Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "facts.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}

func sampleFact() facts.Fact {
	now := time.Now().UTC()
	return facts.Fact{
		Namespace:    "tg:user:1",
		Entity:       "Андрей",
		Attribute:    "likes",
		Value:        "грейпфрут",
		Confidence:   0.8,
		SourceMsgRef: "msg-1",
		CreatedAt:    now,
		UpdatedAt:    now,
		LastSeenAt:   now,
	}
}

func TestStore_InsertAndGetByID(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()

	id, err := s.Insert(context.Background(), sampleFact())
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id == 0 {
		t.Fatal("zero id")
	}
	got, err := s.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Entity != "Андрей" || got.Value != "грейпфрут" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStore_GetByID_NotFound(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	_, err := s.GetByID(context.Background(), 999)
	if !errors.Is(err, facts.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStore_SoftDelete(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	id, _ := s.Insert(context.Background(), sampleFact())
	if err := s.SoftDelete(context.Background(), id); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	got, _ := s.GetByID(context.Background(), id)
	if got.DeletedAt.IsZero() {
		t.Fatal("DeletedAt still zero after SoftDelete")
	}
}

func TestStore_Update(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	id, _ := s.Insert(context.Background(), sampleFact())
	got, _ := s.GetByID(context.Background(), id)
	got.Value = "арбуз"
	got.Confidence = 0.95
	got.UpdatedAt = time.Now().UTC()
	if err := s.Update(context.Background(), got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	back, _ := s.GetByID(context.Background(), id)
	if back.Value != "арбуз" || back.Confidence != 0.95 {
		t.Fatalf("Update did not persist: %+v", back)
	}
}

func TestStore_FindByKey(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	id, _ := s.Insert(context.Background(), sampleFact())
	f, err := s.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if err != nil {
		t.Fatalf("FindByKey: %v", err)
	}
	if f.ID != id {
		t.Fatalf("got id %d want %d", f.ID, id)
	}
}

func TestStore_FindByKey_NotFound(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	_, err := s.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if !errors.Is(err, facts.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStore_FindByKey_SkipsDeleted(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	id, _ := s.Insert(context.Background(), sampleFact())
	_ = s.SoftDelete(context.Background(), id)
	_, err := s.FindByKey(context.Background(), "tg:user:1", "Андрей", "likes")
	if !errors.Is(err, facts.ErrNotFound) {
		t.Fatalf("want ErrNotFound for deleted, got %v", err)
	}
}

func TestStore_ListByNamespace(t *testing.T) {
	s := newStoreT(t)
	defer s.Close()
	for _, attr := range []string{"likes", "lives_in", "calls_self"} {
		f := sampleFact()
		f.Attribute = attr
		_, _ = s.Insert(context.Background(), f)
	}
	out, err := s.ListByNamespace(context.Background(), []string{"tg:user:1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3, got %d", len(out))
	}
}
