package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"
)

// fakeClock drives Key pool availability: After advances time at once instead
// of sleeping, unless stalled, in which case the wait never ends.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waits   []time.Duration
	stalled bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waits = append(c.waits, d)
	if c.stalled {
		return nil
	}
	c.now = c.now.Add(d)
	fired := make(chan time.Time, 1)
	fired <- c.now
	return fired
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// useKeyPoolClock gives the test its own key availability registry on a fake
// clock, so key states never leak between tests.
func useKeyPoolClock(t *testing.T) *fakeClock {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	previous := keyAvailability
	keyAvailability = newKeyAvailabilityRegistry(clock)
	t.Cleanup(func() { keyAvailability = previous })
	return clock
}

// always makes a scripted key failure repeat until the key is healed.
const always = -1

// poolAnthropic is a fake Anthropic that answers "ok" except for keys with a
// scripted failure.
type poolAnthropic struct {
	*fakeAnthropic
	mu       sync.Mutex
	failures map[string]*scriptedFailure
}

type scriptedFailure struct {
	respond func(http.ResponseWriter)
	times   int
}

func newPoolAnthropic(t *testing.T) *poolAnthropic {
	t.Helper()
	fake := &poolAnthropic{failures: map[string]*scriptedFailure{}}
	fake.fakeAnthropic = newFakeAnthropicFor(t, func(w http.ResponseWriter, r *http.Request) {
		if respond := fake.nextFailure(r.Header.Get("X-Api-Key")); respond != nil {
			respond(w)
			return
		}
		writeJSON(w, okAnswer)
	})
	return fake
}

func (f *poolAnthropic) nextFailure(key string) func(http.ResponseWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	failure := f.failures[key]
	if failure == nil || failure.times == 0 {
		return nil
	}
	if failure.times > 0 {
		failure.times--
	}
	return failure.respond
}

// fail makes the next times requests with key get respond (always: until healed).
func (f *poolAnthropic) fail(key string, times int, respond func(http.ResponseWriter)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[key] = &scriptedFailure{respond: respond, times: times}
}

func (f *poolAnthropic) heal(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failures, key)
}

// requestsWith counts the Messages API requests that carried key.
func (f *poolAnthropic) requestsWith(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fakeAnthropic.mu.Lock()
	defer f.fakeAnthropic.mu.Unlock()
	n := 0
	for _, request := range f.requests {
		if request.path == "/v1/messages" && request.header.Get("X-Api-Key") == key {
			n++
		}
	}
	return n
}

// conversationOn finds a conversation that the healthy pool binds to key.
func (f *poolAnthropic) conversationOn(t *testing.T, provider LLMProvider, modelID, key string) string {
	t.Helper()
	for i := range 100 {
		conversation := fmt.Sprintf("agent:main:telegram:direct:%s:%d", key, i)
		if f.keyFor(t, provider, modelID, conversation) == key {
			return conversation
		}
	}
	t.Fatalf("no conversation is bound to %s", key)
	return ""
}

// anthropicStatus answers with Anthropic's error envelope and the given headers.
func anthropicStatus(status int, body string, headers ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func rateLimitedFor(retryAfter string) func(http.ResponseWriter) {
	body := `{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests exceeded your rate limit."}}`
	return anthropicStatus(http.StatusTooManyRequests, body, "Retry-After", retryAfter)
}

func TestKeyPoolWaitsOutAShortRateLimitOnTheSameKey(t *testing.T) {
	clock := useKeyPoolClock(t)
	fake := newPoolAnthropic(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b")
	provider, modelID := createProvider(t, &cfg.ModelList[0])
	conversation := fake.conversationOn(t, provider, modelID, "sk-ant-a")
	ownBefore, otherBefore := fake.requestsWith("sk-ant-a"), fake.requestsWith("sk-ant-b")

	fake.fail("sk-ant-a", 1, rateLimitedFor("3"))
	if got := fake.keyFor(t, provider, modelID, conversation); got != "sk-ant-a" {
		t.Fatalf("answer came from %s, want the conversation's own key", got)
	}

	if got := fake.requestsWith("sk-ant-a") - ownBefore; got != 2 {
		t.Errorf("own key received %d requests, want the rate-limited one and its retry", got)
	}
	if got := fake.requestsWith("sk-ant-b") - otherBefore; got != 0 {
		t.Errorf("other key received %d requests, want none", got)
	}
	if len(clock.waits) != 1 || clock.waits[0] != 3*time.Second {
		t.Errorf("waits = %v, want one wait of the 3s retry-after", clock.waits)
	}
}

func TestKeyPoolStopsWaitingOutARateLimitWhenTheRequestIsCancelled(t *testing.T) {
	clock := useKeyPoolClock(t)
	clock.stalled = true
	ctx, cancel := context.WithCancel(t.Context())
	fake := newPoolAnthropic(t)
	fake.fail("sk-ant-a", always, func(w http.ResponseWriter) {
		cancel()
		rateLimitedFor("5")(w)
	})
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a")
	provider, modelID := createProvider(t, &cfg.ModelList[0])

	_, err := provider.Chat(ctx, []Message{{Role: "user", Content: "Hi"}}, nil, modelID,
		map[string]any{"max_tokens": 64})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat() error = %v, want the cancellation", err)
	}
}

func TestKeyPoolMovesALongRateLimitedConversationAndReturnsItAfterExpiry(t *testing.T) {
	cases := map[string]func(http.ResponseWriter){
		"retry-after": rateLimitedFor("30"),
		"rate limit reset headers": anthropicStatus(http.StatusTooManyRequests,
			`{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`,
			"anthropic-ratelimit-requests-remaining", "0",
			"anthropic-ratelimit-requests-reset", "2026-11-07T12:00:30Z"),
	}
	for name, limited := range cases {
		t.Run(name, func(t *testing.T) {
			clock := useKeyPoolClock(t)
			fake := newPoolAnthropic(t)
			cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b", "sk-ant-c")
			provider, modelID := createProvider(t, &cfg.ModelList[0])
			conversation := fake.conversationOn(t, provider, modelID, "sk-ant-a")
			bystanders := map[string]string{}
			for _, key := range []string{"sk-ant-b", "sk-ant-c"} {
				bystanders[fake.conversationOn(t, provider, modelID, key)] = key
			}

			fake.fail("sk-ant-a", 1, limited)
			moved := fake.keyFor(t, provider, modelID, conversation)
			if moved == "sk-ant-a" {
				t.Fatal("rate-limited conversation stayed on its key")
			}
			limitedRequests := fake.requestsWith("sk-ant-a")
			clock.advance(29 * time.Second)
			if got := fake.keyFor(t, provider, modelID, conversation); got != moved {
				t.Errorf("during the rate limit the conversation used %s, want %s", got, moved)
			}
			for other, key := range bystanders {
				if got := fake.keyFor(t, provider, modelID, other); got != key {
					t.Errorf("conversation on %s moved to %s while another key was rate limited", key, got)
				}
			}
			if fake.requestsWith("sk-ant-a") != limitedRequests {
				t.Error("a request reached the rate-limited key before the limit expired")
			}
			if len(clock.waits) != 0 {
				t.Errorf("waits = %v, want none for a long rate limit", clock.waits)
			}

			clock.advance(2 * time.Second)
			if got := fake.keyFor(t, provider, modelID, conversation); got != "sk-ant-a" {
				t.Fatalf("after the rate limit the conversation used %s, want its own key", got)
			}
		})
	}
}

func TestKeyPoolMovesOnWhenAShortRateLimitRepeats(t *testing.T) {
	useKeyPoolClock(t)
	fake := newPoolAnthropic(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b")
	provider, modelID := createProvider(t, &cfg.ModelList[0])
	conversation := fake.conversationOn(t, provider, modelID, "sk-ant-a")

	fake.fail("sk-ant-a", always, rateLimitedFor("2"))
	if got := fake.keyFor(t, provider, modelID, conversation); got != "sk-ant-b" {
		t.Fatalf("answer came from %s, want the next key after the retry was rate limited again", got)
	}
}

const anthropicSpendCapBody = `{"type":"error","error":{"type":"rate_limit_error",` +
	`"message":"You have reached your API usage limits: Your organization has reached its monthly spend limit. ` +
	`You will regain access on 2026-12-01 at 00:00 UTC.","details":{"error_code":"enforced_spend_limit_reached"}}}`

// downForms are the ways Anthropic says a key's organization is out of money
// or refuses the key itself.
var downForms = map[string]func(http.ResponseWriter){
	"credit balance 400": anthropicStatus(http.StatusBadRequest, anthropicErrorBody(anthropicCreditBalanceMessage)),
	"billing 402": anthropicStatus(http.StatusPaymentRequired,
		`{"type":"error","error":{"type":"billing_error","message":"There is an issue with your billing."}}`),
	"spend cap 429": anthropicStatus(http.StatusTooManyRequests, anthropicSpendCapBody),
	"specified usage limit 400": anthropicStatus(http.StatusBadRequest, anthropicErrorBody(
		"You have reached your specified API usage limits. You will regain access on 2026-12-01 at 00:00 UTC.")),
	"revoked key 401": anthropicStatus(http.StatusUnauthorized,
		`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`),
	"permission 403": anthropicStatus(http.StatusForbidden,
		`{"type":"error","error":{"type":"permission_error","message":"Your API key does not have permission."}}`),
}

func TestKeyPoolKeepsAKeyOutOfCreditOrRejectedOutUntilARecheckSucceeds(t *testing.T) {
	for name, down := range downForms {
		t.Run(name, func(t *testing.T) {
			clock := useKeyPoolClock(t)
			fake := newPoolAnthropic(t)
			cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b", "sk-ant-c")
			opus, opusID := createProvider(t, &cfg.ModelList[0])
			haiku, haikuID := createProvider(t, &cfg.ModelList[1])
			conversation := fake.conversationOn(t, opus, opusID, "sk-ant-a")

			fake.fail("sk-ant-a", always, down)
			if got := fake.keyFor(t, opus, opusID, conversation); got == "sk-ant-a" {
				t.Fatal("the key answered")
			}
			if len(clock.waits) != 0 {
				t.Errorf("waits = %v, want none for an exhausted key", clock.waits)
			}
			exhaustedRequests := fake.requestsWith("sk-ant-a")
			assertKeyOut := func(phase string) {
				t.Helper()
				for i := range 30 {
					fake.keyFor(t, haiku, haikuID, fmt.Sprintf("agent:main:pico:direct:%s:%d", phase, i))
				}
				fake.keyFor(t, opus, opusID, conversation)
				if got := fake.requestsWith("sk-ant-a") - exhaustedRequests; got != 0 {
					t.Fatalf("%s: exhausted key received %d requests, want none", phase, got)
				}
			}
			clock.advance(time.Hour) // past any rate limit
			assertKeyOut("exhausted")

			clock.advance(downRecheckInterval - time.Hour)
			fake.keyFor(t, opus, opusID, conversation)
			if got := fake.requestsWith("sk-ant-a") - exhaustedRequests; got != 1 {
				t.Fatalf("recheck sent %d requests to the exhausted key, want one", got)
			}
			exhaustedRequests++
			clock.advance(time.Hour)
			assertKeyOut("failed recheck")

			fake.heal("sk-ant-a")
			clock.advance(downRecheckInterval)
			for range 2 {
				if got := fake.keyFor(t, opus, opusID, conversation); got != "sk-ant-a" {
					t.Fatalf("after a successful recheck the conversation used %s, want its own key", got)
				}
			}
		})
	}
}

func TestKeyPoolRecheckThatMeetsAnotherErrorLeavesTheNextRequestToRecheck(t *testing.T) {
	cases := map[string]struct {
		respond func(http.ResponseWriter)
		wait    time.Duration // until the outcome stops blocking the key
	}{
		"server error": {anthropicStatus(http.StatusInternalServerError,
			`{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`), 0},
		"rate limit": {rateLimitedFor("30"), 30 * time.Second},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clock := useKeyPoolClock(t)
			fake := newPoolAnthropic(t)
			cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b")
			provider, modelID := createProvider(t, &cfg.ModelList[0])
			conversation := fake.conversationOn(t, provider, modelID, "sk-ant-a")

			fake.fail("sk-ant-a", 1, downForms["credit balance 400"])
			fake.keyFor(t, provider, modelID, conversation)
			clock.advance(downRecheckInterval)
			fake.fail("sk-ant-a", 1, tc.respond)
			_, _ = provider.Chat(t.Context(), []Message{{Role: "user", Content: "Hi"}}, nil, modelID,
				map[string]any{"max_tokens": 64, ConversationIdentityOption: conversation})

			clock.advance(tc.wait)
			if got := fake.keyFor(t, provider, modelID, conversation); got != "sk-ant-a" {
				t.Fatalf("after a recheck that met a %s the next request used %s, want a recheck of the key", name, got)
			}
			if state := KeyPoolAvailability(&cfg.KeyPools[0])[0].State; state != KeyAvailable {
				t.Errorf("rechecked key state = %s, want available", state)
			}
		})
	}
}

func TestKeyPoolRateLimitsAKeyOnlyForTheLimitedModel(t *testing.T) {
	useKeyPoolClock(t)
	fake := newPoolAnthropic(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b")
	opus, opusID := createProvider(t, &cfg.ModelList[0])
	haiku, haikuID := createProvider(t, &cfg.ModelList[1])
	conversation := fake.conversationOn(t, opus, opusID, "sk-ant-a")

	fake.fail("sk-ant-a", 1, rateLimitedFor("60"))
	if got := fake.keyFor(t, opus, opusID, conversation); got == "sk-ant-a" {
		t.Fatal("rate-limited Opus request stayed on its key")
	}
	if got := fake.keyFor(t, haiku, haikuID, conversation); got != "sk-ant-a" {
		t.Errorf("Haiku request moved to %s because Opus was rate limited on its key", got)
	}
	if got := fake.keyFor(t, opus, opusID, conversation); got == "sk-ant-a" {
		t.Error("Opus request returned to its key before the rate limit expired")
	}
}

func TestKeyPoolAvailabilityReportsKeyStatesAndReturnsAKeyToService(t *testing.T) {
	clock := useKeyPoolClock(t)
	fake := newPoolAnthropic(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b", "sk-ant-c", "sk-ant-d", "sk-ant-e")
	cfg.KeyPools[0].Keys[4].Enabled = false
	if err := cfg.ResolveKeyPools(); err != nil {
		t.Fatal(err)
	}
	provider, modelID := createProvider(t, &cfg.ModelList[0])
	failures := map[string]func(http.ResponseWriter){
		"sk-ant-a": downForms["credit balance 400"],
		"sk-ant-b": rateLimitedFor("60"),
		"sk-ant-c": downForms["revoked key 401"],
	}
	exhaustedConversation := fake.conversationOn(t, provider, modelID, "sk-ant-a")
	for key, respond := range failures {
		conversation := fake.conversationOn(t, provider, modelID, key)
		fake.fail(key, 1, respond)
		fake.keyFor(t, provider, modelID, conversation)
	}

	now := clock.Now()
	want := []KeyAvailability{
		{KeyID: "org-1", State: KeyExhausted, Until: now.Add(downRecheckInterval)},
		{KeyID: "org-2", State: KeyRateLimited, RateLimitedModels: []ModelRateLimit{
			{Model: modelID, Until: now.Add(time.Minute)},
		}},
		{KeyID: "org-3", State: KeyRejected, Until: now.Add(downRecheckInterval)},
		{KeyID: "org-4", State: KeyAvailable},
		{KeyID: "org-5", State: KeyDisabled},
	}
	if got := KeyPoolAvailability(&cfg.KeyPools[0]); !reflect.DeepEqual(got, want) {
		t.Fatalf("KeyPoolAvailability() = %+v, want %+v", got, want)
	}

	// The dashboard returns a key to service by saving the time it did so; the
	// next provider built from that configuration applies it once.
	cfg.KeyPools[0].Keys[0].ReturnedAt = time.Date(2026, 11, 7, 13, 0, 0, 0, time.UTC)
	provider, modelID = createProvider(t, &cfg.ModelList[0])
	if got := fake.keyFor(t, provider, modelID, exhaustedConversation); got != "sk-ant-a" {
		t.Fatalf("returned key did not get its conversation back; it went to %s", got)
	}
	if got := KeyPoolAvailability(&cfg.KeyPools[0])[0]; got.State != KeyAvailable ||
		!got.ReturnedAt.Equal(cfg.KeyPools[0].Keys[0].ReturnedAt) {
		t.Fatalf("returned key reports %+v, want available with the applied return", got)
	}

	fake.fail("sk-ant-a", 1, downForms["credit balance 400"])
	fake.keyFor(t, provider, modelID, exhaustedConversation)
	provider, modelID = createProvider(t, &cfg.ModelList[0])
	if got := fake.keyFor(t, provider, modelID, exhaustedConversation); got == "sk-ant-a" {
		t.Fatal("an already applied return put the newly exhausted key back in service")
	}
}

func TestKeyPoolWithNoAvailableKeyLetsTheRoutingChainAnswerFromItsReserve(t *testing.T) {
	cases := map[string]struct {
		a, b   func(http.ResponseWriter)
		reason FailoverReason
	}{
		"exhausted and rate limited": {downForms["credit balance 400"], rateLimitedFor("60"), FailoverRateLimit},
		"all rejected":               {downForms["revoked key 401"], downForms["permission 403"], FailoverAuth},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useKeyPoolClock(t)
			fake := newPoolAnthropic(t)
			fake.fail("sk-ant-a", always, tc.a)
			fake.fail("sk-ant-b", always, tc.b)
			cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b")
			provider, _ := createProvider(t, &cfg.ModelList[0])

			for _, phase := range []string{"keys fail", "keys known unavailable"} {
				result, reserveCalls, _, err := runAnthropicThenReserve(t, provider)
				if err != nil {
					t.Fatalf("%s: Execute() error = %v, want the reserve to answer", phase, err)
				}
				if reserveCalls != 1 || result.Response.Content != "from reserve" {
					t.Fatalf("%s: reserve calls = %d, response = %+v; want one reserve answer",
						phase, reserveCalls, result.Response)
				}
				if got := result.Attempts[0].Reason; got != tc.reason {
					t.Errorf("%s: pool attempt reason = %q, want %q", phase, got, tc.reason)
				}
			}
			if got := fake.requestsWith("sk-ant-a") + fake.requestsWith("sk-ant-b"); got != 2 {
				t.Errorf("pool keys received %d requests, want one each before both were marked", got)
			}
		})
	}
}

func TestKeyPoolKeepsAKeyThatAnsweredWithARefusal(t *testing.T) {
	useKeyPoolClock(t)
	fake := newPoolAnthropic(t)
	cfg := keyPoolConfig(t, fake.URL, "sk-ant-a", "sk-ant-b")
	provider, modelID := createProvider(t, &cfg.ModelList[0])
	conversation := fake.conversationOn(t, provider, modelID, "sk-ant-a")

	fake.fail("sk-ant-a", 1, func(w http.ResponseWriter) {
		writeJSON(w, `{"id":"msg_r","type":"message","role":"assistant","model":"claude-opus-5-5",
			"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber"},"content":[],
			"usage":{"input_tokens":1,"output_tokens":0}}`)
	})
	if got := fake.keyFor(t, provider, modelID, conversation); got != "sk-ant-a" {
		t.Fatalf("refusal came from %s, want the conversation's own key", got)
	}
	if got := fake.keyFor(t, provider, modelID, conversation); got != "sk-ant-a" {
		t.Fatalf("after a refusal the conversation moved to %s", got)
	}
}
