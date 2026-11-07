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
	"github.com/sipeed/picoclaw/pkg/logger"
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

// fileName is the spend state file in the Picoclaw home, separate from
// configuration.
const fileName = "key-pool-spend.json"

// errCorrupt marks a spend file that is not a ledger.
var errCorrupt = errors.New("spend file is not a ledger")

// Store is one spend state file. The zero Store is the file in the Picoclaw
// home as it is when each call runs; NewStore names another file.
type Store struct{ path string }

// NewStore is the spend state file at path.
func NewStore(path string) Store { return Store{path: path} }

// Path is the store's file.
func (s Store) Path() string {
	if s.path != "" {
		return s.path
	}
	return filepath.Join(config.HomeDir(), fileName)
}

// Read loads the ledger; no file yet is an empty ledger.
func (s Store) Read() (Ledger, error) {
	ledger := Ledger{Version: 1, Pools: map[string]map[string]map[string]Totals{}}
	data, err := os.ReadFile(s.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, fmt.Errorf("parse %s: %w: %w", s.Path(), errCorrupt, err)
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
// older than any period could reach. A corrupt file is moved aside to
// <file>.corrupt-<timestamp> and recording starts from an empty ledger.
func (s Store) Record(poolID, keyID string, at time.Time, usage Totals) error {
	recordMu.Lock()
	defer recordMu.Unlock()
	path := s.Path()
	lock, err := fileutil.LockFile(context.Background(), path+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()

	ledger, err := s.Read()
	if errors.Is(err, errCorrupt) {
		aside := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		if renameErr := os.Rename(path, aside); renameErr != nil {
			return renameErr
		}
		logger.WarnCF("key-pools", "Key pool spend file was corrupt; moved it aside and started afresh",
			map[string]any{"path": aside, "error": err.Error()})
		err = nil
	}
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
	return fileutil.WriteFileAtomic(path, data, 0o600)
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
