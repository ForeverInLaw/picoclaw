package keyspend

import (
	"os"
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
	answer := Totals{InputTokens: 10, CacheReadTokens: 30, OutputTokens: 5, EstimatedUSD: 0.5}
	for _, at := range []time.Time{day(2026, 10, 14), day(2026, 10, 15), day(2026, 11, 7)} {
		if err := Record("orgs", "first", at, answer); err != nil {
			t.Fatal(err)
		}
	}

	ledger, err := Read()
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
	if info, err := os.Stat(Path()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("spend file = %v, %v; want owner-only permissions", info, err)
	}
}
