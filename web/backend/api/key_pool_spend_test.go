package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/keyspend"
)

// keySpendListing is the budget and spend part of a key in GET /api/key-pools.
type keySpendListing struct {
	ID               string   `json:"id"`
	MonthlyBudgetUSD *float64 `json:"monthly_budget_usd"`
	ResetDay         int      `json:"reset_day"`
	Spend            struct {
		Since            time.Time `json:"since"`
		InputTokens      int64     `json:"input_tokens"`
		OutputTokens     int64     `json:"output_tokens"`
		CacheWriteTokens int64     `json:"cache_write_tokens"`
		CacheReadTokens  int64     `json:"cache_read_tokens"`
		EstimatedUSD     float64   `json:"estimated_usd"`
		UnpricedTokens   int64     `json:"unpriced_tokens"`
		CacheHitRate     float64   `json:"cache_hit_rate"`
	} `json:"spend"`
}

func listKeySpend(t *testing.T, mux *http.ServeMux) map[string]keySpendListing {
	t.Helper()
	rec := serve(mux, http.MethodGet, "/api/key-pools", "")
	var listing struct {
		Pools []struct {
			Keys []keySpendListing `json:"keys"`
		} `json:"pools"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listing) != nil {
		t.Fatalf("GET /api/key-pools = %d %s", rec.Code, rec.Body.String())
	}
	keys := map[string]keySpendListing{}
	for _, key := range listing.Pools[0].Keys {
		keys[key.ID] = key
	}
	return keys
}

func TestKeyPoolKeysShowSpendSinceTheirResetDayAgainstTheirBudget(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	path := keyPoolConfigPath(t)
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	now := time.Now().UTC()
	record := func(key string, at time.Time, totals keyspend.Totals) {
		t.Helper()
		if err := (keyspend.Store{}).Record("orgs", key, at, totals); err != nil {
			t.Fatal(err)
		}
	}
	record("first", now, keyspend.Totals{
		InputTokens: 1000, CacheWriteTokens: 2000, CacheReadTokens: 7000, OutputTokens: 500, EstimatedUSD: 12.5,
	})
	record("first", now.AddDate(0, 0, -40), keyspend.Totals{InputTokens: 1, EstimatedUSD: 100}) // an earlier period
	record("second", now, keyspend.Totals{InputTokens: 4000, OutputTokens: 1000, UnpricedTokens: 5000})

	keys := listKeySpend(t, mux)
	first, second := keys["first"], keys["second"]
	if first.MonthlyBudgetUSD != nil || first.ResetDay != 1 || !first.Spend.Since.Equal(keyspend.PeriodStart(1, now)) {
		t.Errorf("first key without a budget = %+v, want reset day 1 and no budget", first)
	}
	if first.Spend.EstimatedUSD != 12.5 || first.Spend.InputTokens != 1000 || first.Spend.CacheReadTokens != 7000 ||
		first.Spend.CacheHitRate != 0.7 {
		t.Errorf("first key spend = %+v, want $12.50 at a 70%% cache hit rate", first.Spend)
	}
	if second.Spend.EstimatedUSD != 0 || second.Spend.UnpricedTokens != 5000 || second.Spend.CacheHitRate != 0 {
		t.Errorf("second key spend = %+v, want 5000 unpriced tokens and no dollars", second.Spend)
	}

	resetDay := now.Day()
	if rec := serve(mux, http.MethodPatch, "/api/key-pools/orgs/keys/first",
		`{"monthly_budget_usd":200,"reset_day":`+strconv.Itoa(resetDay)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("setting a budget = %d %s", rec.Code, rec.Body.String())
	}
	first = listKeySpend(t, mux)["first"]
	if first.MonthlyBudgetUSD == nil || *first.MonthlyBudgetUSD != 200 || first.ResetDay != resetDay ||
		!first.Spend.Since.Equal(keyspend.PeriodStart(resetDay, now)) || first.Spend.EstimatedUSD != 12.5 {
		t.Fatalf("first key with a budget = %+v", first)
	}

	for _, invalid := range []string{
		`{"monthly_budget_usd":-1}`, `{"reset_day":0}`, `{"reset_day":32}`, `{"monthly_budget_usd":"200"}`,
	} {
		if rec := serve(mux, http.MethodPatch, "/api/key-pools/orgs/keys/first", invalid); rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400", invalid, rec.Code)
		}
	}
	if rec := serve(mux, http.MethodPatch, "/api/key-pools/orgs/keys/first",
		`{"monthly_budget_usd":0}`); rec.Code != http.StatusOK {
		t.Fatalf("clearing the budget = %d %s", rec.Code, rec.Body.String())
	}
	if first = listKeySpend(t, mux)["first"]; first.MonthlyBudgetUSD != nil || first.ResetDay != resetDay {
		t.Errorf("after clearing the budget = %+v, want no budget and the same reset day", first)
	}
}
