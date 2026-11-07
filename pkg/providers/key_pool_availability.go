package providers

import (
	"context"
	"errors"
	"sync"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	// shortRateLimitWait is the longest rate limit waited out on the same key,
	// so the conversation keeps its prompt cache.
	shortRateLimitWait = 10 * time.Second
	// defaultRateLimitPause applies to a 429 that names no time.
	defaultRateLimitPause = time.Minute
	// exhaustedRecheckInterval spaces the single attempts that recheck whether an
	// exhausted key has credit again.
	exhaustedRecheckInterval = 6 * time.Hour
)

// KeyState names the availability of one Key pool key.
type KeyState string

const (
	KeyAvailable   KeyState = "available"
	KeyRateLimited KeyState = "rate_limited"
	KeyExhausted   KeyState = "exhausted"
	KeyDisabled    KeyState = "disabled"
)

// KeyAvailability is one key's state. Until is when a rate limit ends or when
// an exhausted key is next rechecked; it is zero for other states.
type KeyAvailability struct {
	KeyID string    `json:"key_id"`
	State KeyState  `json:"state"`
	Until time.Time `json:"until,omitzero"`
}

// KeyPoolAvailability reports the state of each key of pool in configured
// order. States are held in memory for the process and shared by every
// configured model using the pool.
func KeyPoolAvailability(pool *config.KeyPool) []KeyAvailability {
	states := make([]KeyAvailability, 0, len(pool.Keys))
	for _, key := range pool.Keys {
		state := KeyAvailability{KeyID: key.ID, State: KeyDisabled}
		if key.Enabled {
			state.State, state.Until = keyAvailability.state(keyRef{pool: pool.ID, key: key.ID})
		}
		states = append(states, state)
	}
	return states
}

// ReturnKeyToService makes a rate-limited or exhausted key available at once,
// for an administrator who knows its credit is renewed.
func ReturnKeyToService(poolID, keyID string) {
	keyAvailability.clear(keyRef{pool: poolID, key: keyID})
}

// clock is the time source of key availability; tests replace it so that
// rate limits expire and short waits end without sleeping.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// keyAvailability is the process-wide registry of Key pool key states.
var keyAvailability = newKeyAvailabilityRegistry(systemClock{})

// keyRef identifies a key across every configured model using its pool.
type keyRef struct{ pool, key string }

// keyHealth is a key's recorded trouble; a key without an entry is available.
type keyHealth struct {
	exhausted bool
	until     time.Time // end of a rate limit, or an exhausted key's next recheck
}

type keyAvailabilityRegistry struct {
	clock clock
	mu    sync.Mutex
	keys  map[keyRef]keyHealth
}

func newKeyAvailabilityRegistry(clock clock) *keyAvailabilityRegistry {
	return &keyAvailabilityRegistry{clock: clock, keys: map[keyRef]keyHealth{}}
}

// claim reports whether the key may serve a request now. A due recheck of an
// exhausted key goes to this one request: the next recheck moves an interval
// ahead, and only a success makes the key available again.
func (r *keyAvailabilityRegistry) claim(ref keyRef) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	health, troubled := r.keys[ref]
	now := r.clock.Now()
	switch {
	case !troubled:
		return true
	case now.Before(health.until):
		return false
	case health.exhausted:
		health.until = now.Add(exhaustedRecheckInterval)
		r.keys[ref] = health
	default:
		delete(r.keys, ref)
	}
	return true
}

// served records a success, which returns a rechecked exhausted key to service.
// An active rate limit set by a concurrent request stays.
func (r *keyAvailabilityRegistry) served(ref keyRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys[ref].exhausted {
		delete(r.keys, ref)
	}
}

// exhaust marks the key out of credit until a recheck succeeds.
func (r *keyAvailabilityRegistry) exhaust(ref keyRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[ref] = keyHealth{exhausted: true, until: r.clock.Now().Add(exhaustedRecheckInterval)}
}

// rateLimit pauses the key for d. It never shortens a longer pause or turns an
// exhausted key into a rate-limited one.
func (r *keyAvailabilityRegistry) rateLimit(ref keyRef, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	health := r.keys[ref]
	if until := r.clock.Now().Add(d); !health.exhausted && until.After(health.until) {
		health.until = until
		r.keys[ref] = health
	}
}

func (r *keyAvailabilityRegistry) clear(ref keyRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, ref)
}

func (r *keyAvailabilityRegistry) state(ref keyRef) (KeyState, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	health, troubled := r.keys[ref]
	switch {
	case troubled && health.exhausted:
		return KeyExhausted, health.until
	case troubled && r.clock.Now().Before(health.until):
		return KeyRateLimited, health.until
	}
	return KeyAvailable, time.Time{}
}

// wait sleeps for d unless ctx ends first.
func (r *keyAvailabilityRegistry) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.clock.After(d):
		return nil
	}
}

// rateLimitPause is how long a 429 keeps its key away: its retry-after, else
// the latest reset of an Anthropic rate limit with nothing remaining, else
// defaultRateLimitPause.
func rateLimitPause(err error, failure *FailoverError, now time.Time) time.Duration {
	if failure.RetryAfter > 0 {
		return failure.RetryAfter
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		header := apiErr.Response.Header
		var latest time.Time
		for _, limit := range []string{"requests", "tokens", "input-tokens", "output-tokens"} {
			prefix := "anthropic-ratelimit-" + limit + "-"
			if header.Get(prefix+"remaining") != "0" {
				continue
			}
			if reset, parseErr := time.Parse(time.RFC3339, header.Get(prefix+"reset")); parseErr == nil &&
				reset.After(latest) {
				latest = reset
			}
		}
		if latest.After(now) {
			return latest.Sub(now)
		}
	}
	return defaultRateLimitPause
}
