// Package keyspend persists the token and estimated dollar totals of each Key
// pool key per UTC day, in a protected state file under the Picoclaw home that
// the gateway writes and the dashboard reads. Per-request logs are not kept.
package keyspend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	dayLayout = "2006-01-02"
	// retainedDays keeps every day of the longest possible period, a month from
	// the previous reset day, with room to spare.
	retainedDays = 62
)

// Totals is the usage of a key: uncached input, output, cache-write and
// cache-read tokens. EstimatedUSD covers answers from models in the price
// table; UnpricedTokens counts every token of answers from models without a
// price, which never get a guessed amount.
type Totals struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	EstimatedUSD     float64 `json:"estimated_usd"`
	UnpricedTokens   int64   `json:"unpriced_tokens"`
}

func (t *Totals) add(other Totals) {
	t.InputTokens += other.InputTokens
	t.OutputTokens += other.OutputTokens
	t.CacheWriteTokens += other.CacheWriteTokens
	t.CacheReadTokens += other.CacheReadTokens
	t.EstimatedUSD += other.EstimatedUSD
	t.UnpricedTokens += other.UnpricedTokens
}

// CacheHitRate is the share of prompt tokens read from the prompt cache; 0
// without prompt tokens.
func (t Totals) CacheHitRate() float64 {
	prompt := t.InputTokens + t.CacheWriteTokens + t.CacheReadTokens
	if prompt == 0 {
		return 0
	}
	return float64(t.CacheReadTokens) / float64(prompt)
}

// Ledger holds the daily totals of every key: pool ID → key ID → UTC day.
type Ledger struct {
	Version int                                     `json:"version"`
	Pools   map[string]map[string]map[string]Totals `json:"pools"`
}

// Since sums the key's totals from the UTC day of start to now.
func (l Ledger) Since(poolID, keyID string, start time.Time) Totals {
	var sum Totals
	first := start.UTC().Format(dayLayout)
	for day, totals := range l.Pools[poolID][keyID] {
		if day >= first {
			sum.add(totals)
		}
	}
	return sum
}

// PeriodStart is the start of the budget period that contains now: midnight UTC
// of the most recent reset day, clamped to the length of its month. Reset day 0
// means the 1st.
func PeriodStart(resetDay int, now time.Time) time.Time {
	now = now.UTC()
	resetDay = max(resetDay, 1)
	start := monthDay(now.Year(), now.Month(), resetDay)
	if start.After(now) {
		start = monthDay(now.Year(), now.Month()-1, resetDay)
	}
	return start
}

// monthDay is midnight UTC of day in the month, or of its last day if shorter.
func monthDay(year int, month time.Month, day int) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	return first.AddDate(0, 0, min(day, last)-1)
}

// Path is the spend state file, separate from configuration.
func Path() string { return filepath.Join(config.HomeDir(), "key-pool-spend.json") }

// Read loads the ledger; no file yet is an empty ledger.
func Read() (Ledger, error) {
	ledger := Ledger{Version: 1, Pools: map[string]map[string]map[string]Totals{}}
	data, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, fmt.Errorf("parse %s: %w", Path(), err)
	}
	if ledger.Pools == nil {
		ledger.Pools = map[string]map[string]map[string]Totals{}
	}
	return ledger, nil
}

// recordMu serializes this process's writers; the file lock serializes them
// with other processes.
var recordMu sync.Mutex

// Record adds usage to the key's totals for the UTC day of at and drops days
// older than any period could reach.
func Record(poolID, keyID string, at time.Time, usage Totals) error {
	recordMu.Lock()
	defer recordMu.Unlock()
	lock, err := fileutil.LockFile(context.Background(), Path()+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()

	ledger, err := Read()
	if err != nil {
		return err
	}
	days := ledger.Pools[poolID][keyID]
	if days == nil {
		if ledger.Pools[poolID] == nil {
			ledger.Pools[poolID] = map[string]map[string]Totals{}
		}
		days = map[string]Totals{}
		ledger.Pools[poolID][keyID] = days
	}
	today := at.UTC()
	totals := days[today.Format(dayLayout)]
	totals.add(usage)
	days[today.Format(dayLayout)] = totals
	ledger.prune(today.AddDate(0, 0, -retainedDays).Format(dayLayout))

	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(Path(), data, 0o600)
}

// prune drops days before oldest and keys left without days.
func (l Ledger) prune(oldest string) {
	for poolID, keys := range l.Pools {
		for keyID, days := range keys {
			for day := range days {
				if day < oldest {
					delete(days, day)
				}
			}
			if len(days) == 0 {
				delete(keys, keyID)
			}
		}
		if len(keys) == 0 {
			delete(l.Pools, poolID)
		}
	}
}
