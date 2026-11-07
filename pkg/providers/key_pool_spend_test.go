package providers

import (
	"fmt"
	"math"
	"net/http"
	"testing"

	"github.com/sipeed/picoclaw/pkg/keyspend"
)

// usageAnswer is a Messages API response from model with the given input,
// cache-write, cache-read and output token counts.
func usageAnswer(model string, input, cacheWrite, cacheRead, output int) string {
	return fmt.Sprintf(`{"id":"msg_s","type":"message","role":"assistant","model":%q,
		"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],
		"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}}`,
		model, input, cacheWrite, cacheRead, output)
}

// keyIDs maps the fake keys of keyPoolConfig to their Key pool key IDs.
var keyIDs = map[string]string{"sk-ant-a": "org-1", "sk-ant-b": "org-2", "sk-ant-c": "org-3"}

// spendOf reads the current monthly totals of a key of keyPoolConfig's pool.
func spendOf(t *testing.T, clock *fakeClock, apiKey string) keyspend.Totals {
	t.Helper()
	ledger, err := keyspend.Store{}.Read()
	if err != nil {
		t.Fatalf("keyspend.Store{}.Read() error = %v", err)
	}
	return ledger.Since("orgs", keyIDs[apiKey], keyspend.PeriodStart(1, clock.Now()))
}

func assertSpend(t *testing.T, got, want keyspend.Totals) {
	t.Helper()
	gotUSD, wantUSD := got.EstimatedUSD, want.EstimatedUSD
	got.EstimatedUSD, want.EstimatedUSD = 0, 0
	if got != want || math.Abs(gotUSD-wantUSD) > 1e-9 {
		got.EstimatedUSD, want.EstimatedUSD = gotUSD, wantUSD
		t.Fatalf("spend = %+v, want %+v", got, want)
	}
}

func TestKeyPoolAnswersAddToTheServingKeysMonthlyTotals(t *testing.T) {
	clock := useKeyPoolClock(t)
	var fake *fakeAnthropic
	fake = newFakeAnthropicFor(t, func(w http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		model, _ := fake.requests[len(fake.requests)-1].body["model"].(string)
		fake.mu.Unlock()
		writeJSON(w, usageAnswer(model, 1000, 2000, 10000, 500))
	})
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b")
	opus, opusID := createProvider(t, &cfg.ModelList[0])
	haiku, haikuID := createProvider(t, &cfg.ModelList[1])

	const conversation = "agent:main:telegram:direct:42"
	key := fake.keyFor(t, opus, opusID, conversation)
	fake.keyFor(t, haiku, haikuID, conversation)
	restarted, _ := createProvider(t, &cfg.ModelList[0])
	fake.keyFor(t, restarted, opusID, conversation)

	// Opus 5.5 at $4 input, $5 5m cache write, $0.20 cache read and $20 output
	// per MTok costs $0.026 per answer; Haiku 4.5 at $1/$1.25/$0.10/$5 $0.007.
	assertSpend(t, spendOf(t, clock, key), keyspend.Totals{
		InputTokens: 3000, CacheWriteTokens: 6000, CacheReadTokens: 30000, OutputTokens: 1500,
		EstimatedUSD: 0.059,
	})
	other := map[string]string{"sk-ant-a": "sk-ant-b", "sk-ant-b": "sk-ant-a"}[key]
	assertSpend(t, spendOf(t, clock, other), keyspend.Totals{})
}

func TestKeyPoolSpendIsPricedByTheServedModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ttl    string
		answer string
		want   keyspend.Totals
	}{
		{
			name:   "1h cache writes",
			ttl:    "1h",
			answer: usageAnswer("claude-opus-5-5", 1000, 2000, 10000, 500),
			want: keyspend.Totals{
				InputTokens: 1000, CacheWriteTokens: 2000, CacheReadTokens: 10000, OutputTokens: 500,
				EstimatedUSD: 0.032, // $8 per MTok for 1h cache writes
			},
		},
		{
			name:   "server-side fallback model",
			answer: usageAnswer("claude-opus-4-8", 1000, 2000, 10000, 500),
			want: keyspend.Totals{
				InputTokens: 1000, CacheWriteTokens: 2000, CacheReadTokens: 10000, OutputTokens: 500,
				EstimatedUSD: 0.035, // Opus 4.8 at $5/$6.25/$0.50/$25
			},
		},
		{
			name:   "dated snapshot id",
			answer: usageAnswer("claude-haiku-4-5-20251001", 1000, 0, 0, 1000),
			want:   keyspend.Totals{InputTokens: 1000, OutputTokens: 1000, EstimatedUSD: 0.006},
		},
		{
			name:   "Haiku 5.5 prompt up to 100K tokens",
			answer: usageAnswer("claude-haiku-5-5", 50000, 0, 40000, 1000),
			want: keyspend.Totals{
				InputTokens: 50000, CacheReadTokens: 40000, OutputTokens: 1000, EstimatedUSD: 0.0059,
			},
		},
		{
			name:   "Haiku 5.5 prompt over 100K tokens",
			answer: usageAnswer("claude-haiku-5-5", 50000, 0, 60000, 1000),
			want: keyspend.Totals{
				InputTokens: 50000, CacheReadTokens: 60000, OutputTokens: 1000, EstimatedUSD: 0.0305,
			},
		},
		{
			name:   "model without a price",
			answer: usageAnswer("claude-mythos-preview", 1000, 2000, 10000, 500),
			want: keyspend.Totals{
				InputTokens: 1000, CacheWriteTokens: 2000, CacheReadTokens: 10000, OutputTokens: 500,
				UnpricedTokens: 13500,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := useKeyPoolClock(t)
			fake := newFakeAnthropic(t, func(w http.ResponseWriter) { writeJSON(w, tc.answer) })
			cfg := keyPoolConfig(t, fake.URL, "sk-ant-a")
			cfg.ModelList[0].CacheTTL = tc.ttl
			provider, modelID := createProvider(t, &cfg.ModelList[0])

			fake.keyFor(t, provider, modelID, "agent:main:telegram:direct:42")

			assertSpend(t, spendOf(t, clock, "sk-ant-a"), tc.want)
		})
	}
}
