package keyspend

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 0, 0, 0, 0, time.UTC)
}

func TestPeriodStartsOnTheMostRecentResetDay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resetDay int
		now      time.Time
		want     time.Time
	}{
		{"default is the 1st", 0, day(2026, 11, 7).Add(15 * time.Hour), day(2026, 11, 1)},
		{"reset day later this month", 15, day(2026, 11, 7), day(2026, 10, 15)},
		{"on the reset day", 15, day(2026, 11, 15), day(2026, 11, 15)},
		{"clamped to a short month", 31, day(2026, 2, 28).Add(time.Hour), day(2026, 2, 28)},
		{"clamped previous month", 31, day(2026, 3, 5), day(2026, 2, 28)},
		{"across the year", 20, day(2027, 1, 3), day(2026, 12, 20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PeriodStart(tc.resetDay, tc.now); !got.Equal(tc.want) {
				t.Errorf("PeriodStart(%d, %v) = %v, want %v", tc.resetDay, tc.now, got, tc.want)
			}
		})
	}
}

func TestRecordedTotalsCountUntilTheNextResetDay(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var store Store // the Picoclaw home's spend file
	answer := Totals{InputTokens: 10, CacheReadTokens: 30, OutputTokens: 5, EstimatedUSD: 0.5}
	for _, at := range []time.Time{day(2026, 10, 14), day(2026, 10, 15), day(2026, 11, 7)} {
		if err := store.Record("orgs", "first", at, answer); err != nil {
			t.Fatal(err)
		}
	}

	ledger, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	got := ledger.Since("orgs", "first", PeriodStart(15, day(2026, 11, 7)))
	want := Totals{InputTokens: 20, CacheReadTokens: 60, OutputTokens: 10, EstimatedUSD: 1}
	if got != want {
		t.Errorf("period from Oct 15 = %+v, want %+v", got, want)
	}
	if got := ledger.Since("orgs", "first", PeriodStart(15, day(2026, 11, 15))); got != (Totals{}) {
		t.Errorf("new period on Nov 15 = %+v, want empty", got)
	}
	if store.Path() != filepath.Join(config.HomeDir(), "key-pool-spend.json") {
		t.Errorf("default spend file = %s, want it in the Picoclaw home", store.Path())
	}
	if info, err := os.Stat(store.Path()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("spend file = %v, %v; want owner-only permissions", info, err)
	}
}

func TestACorruptSpendFileIsMovedAsideAndRecordingStartsAfresh(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "spend.json"))
	if err := os.WriteFile(store.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	answer := Totals{InputTokens: 10, EstimatedUSD: 0.5}
	for range 2 {
		if err := store.Record("orgs", "first", day(2026, 11, 7), answer); err != nil {
			t.Fatalf("Record() over a corrupt file error = %v", err)
		}
	}

	ledger, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.Since("orgs", "first", day(2026, 11, 1)); got != (Totals{InputTokens: 20, EstimatedUSD: 1}) {
		t.Errorf("totals after recovery = %+v, want only the two new answers", got)
	}
	aside, _ := filepath.Glob(store.Path() + ".corrupt-*")
	if len(aside) != 1 {
		t.Fatalf("corrupt copies = %q, want one", aside)
	}
	if data, _ := os.ReadFile(aside[0]); string(data) != "{not json" {
		t.Errorf("corrupt copy holds %q, want the original bytes", data)
	}
}
