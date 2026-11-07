package providers

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
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
	// downRecheckInterval spaces the single attempts that recheck whether an
	// exhausted or rejected key serves again.
	downRecheckInterval = 6 * time.Hour
)

// KeyState names the availability of one Key pool key.
type KeyState string

const (
	KeyAvailable   KeyState = "available"
	KeyRateLimited KeyState = "rate_limited" // for at least one model
	KeyExhausted   KeyState = "exhausted"    // out of credit
	KeyRejected    KeyState = "rejected"     // Anthropic refused the key itself (401/403)
	KeyDisabled    KeyState = "disabled"
)

// KeyAvailability is one key's state. Until is the next recheck of an
// exhausted or rejected key, zero otherwise. Anthropic rate-limits each model
// separately, so RateLimitedModels lists the models the key cannot serve now.
// ReturnedAt is the latest config.KeyPoolKey.ReturnedAt this process applied.
type KeyAvailability struct {
	KeyID             string           `json:"key_id"`
	State             KeyState         `json:"state"`
	Until             time.Time        `json:"until,omitzero"`
	RateLimitedModels []ModelRateLimit `json:"rate_limited_models,omitempty"`
	ReturnedAt        time.Time        `json:"returned_at,omitzero"`
}

// ModelRateLimit is a rate limit of one key for one model id, ending at Until.
type ModelRateLimit struct {
	Model string    `json:"model"`
	Until time.Time `json:"until"`
}

// KeyPoolAvailability reports the state of each key of pool in configured
// order, after applying the pool's returns to service. States are held in
// memory for the process and shared by every configured model using the pool.
func KeyPoolAvailability(pool *config.KeyPool) []KeyAvailability {
	keyPools.availability.applyReturns(pool)
	states := make([]KeyAvailability, 0, len(pool.Keys))
	for _, key := range pool.Keys {
		if !key.Enabled {
			states = append(states, KeyAvailability{KeyID: key.ID, State: KeyDisabled})
			continue
		}
		states = append(states, keyPools.availability.report(newKeyRef(pool.ID, key)))
	}
	return states
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

// keyRef identifies a key across every configured model using its pool. Its
// secret is a fingerprint of the key's value, so availability recorded for one
// secret never follows the key to a replacement; conversations and spend
// follow the key ID alone.
type keyRef struct{ pool, key, secret string }

// newKeyRef refers to key of the pool with poolID. The fingerprint is a
// truncated SHA-256 of the secret and never reveals it.
func newKeyRef(poolID string, key config.KeyPoolKey) keyRef {
	sum := sha256.Sum256([]byte(key.APIKey))
	return keyRef{pool: poolID, key: key.ID, secret: hex.EncodeToString(sum[:8])}
}

// keyHealth is a key's recorded trouble; a key without an entry is available.
type keyHealth struct {
	down    KeyState             // KeyExhausted or KeyRejected; empty while the key itself is fine
	recheck time.Time            // next single attempt on a down key
	limits  map[string]time.Time // model id → end of its rate limit
}

type keyAvailabilityRegistry struct {
	clock    clock
	mu       sync.Mutex
	keys     map[keyRef]*keyHealth
	returned map[keyRef]time.Time // latest applied return to service per key
}

func newKeyAvailabilityRegistry(clock clock) *keyAvailabilityRegistry {
	return &keyAvailabilityRegistry{clock: clock, keys: map[keyRef]*keyHealth{}, returned: map[keyRef]time.Time{}}
}

// applyReturns clears the recorded trouble of each key whose saved return to
// service is new to this process, making it available at once. A value is
// applied only once, so trouble recorded after it stays.
func (r *keyAvailabilityRegistry) applyReturns(pool *config.KeyPool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range pool.Keys {
		ref := newKeyRef(pool.ID, key)
		if !key.ReturnedAt.IsZero() && !key.ReturnedAt.Equal(r.returned[ref]) {
			delete(r.keys, ref)
			r.returned[ref] = key.ReturnedAt
		}
	}
}

// health returns the key's entry, creating it; callers hold mu.
func (r *keyAvailabilityRegistry) health(ref keyRef) *keyHealth {
	health := r.keys[ref]
	if health == nil {
		health = &keyHealth{limits: map[string]time.Time{}}
		r.keys[ref] = health
	}
	return health
}

// blocked reports what keeps the key from serving model at now and until when
// it might serve again; KeyAvailable once nothing does. Callers hold mu.
func (r *keyAvailabilityRegistry) blocked(ref keyRef, model string, now time.Time) (KeyState, time.Time) {
	health := r.keys[ref]
	if health == nil {
		return KeyAvailable, time.Time{}
	}
	limit := health.limits[model]
	switch {
	case health.down != "" && now.Before(health.recheck):
		return health.down, latest(health.recheck, limit)
	case now.Before(limit):
		return KeyRateLimited, limit
	}
	return KeyAvailable, time.Time{}
}

// blockedNow is blocked for a caller without the lock.
func (r *keyAvailabilityRegistry) blockedNow(ref keyRef, model string) (KeyState, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blocked(ref, model, r.clock.Now())
}

// claim reports whether the key may serve model now, and whether this request
// is the recheck of a down key. A due recheck goes to this one request: the
// next recheck moves an interval ahead, and only a success makes the key
// available again.
func (r *keyAvailabilityRegistry) claim(ref keyRef, model string) (ok, recheck bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	if state, _ := r.blocked(ref, model, now); state != KeyAvailable {
		return false, false
	}
	if health := r.keys[ref]; health != nil && health.down != "" {
		health.recheck = now.Add(downRecheckInterval)
		return true, true
	}
	return true, false
}

// releaseRecheck hands a down key's recheck back after an outcome that says
// nothing about its credit or credentials, so the next request rechecks it.
func (r *keyAvailabilityRegistry) releaseRecheck(ref keyRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if health := r.keys[ref]; health != nil && health.down != "" {
		health.recheck = r.clock.Now()
	}
}

// recovered records the success of a claimed recheck, which returns the down
// key to service. Only the recheck may: an ordinary request that started before
// the key went down says nothing about it now. Active rate limits stay.
func (r *keyAvailabilityRegistry) recovered(ref keyRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if health := r.keys[ref]; health != nil {
		health.down = ""
	}
}

// takeDown marks the key exhausted or rejected until a recheck succeeds.
func (r *keyAvailabilityRegistry) takeDown(ref keyRef, state KeyState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	health := r.health(ref)
	health.down = state
	health.recheck = r.clock.Now().Add(downRecheckInterval)
}

// rateLimit pauses the key for model for d, never shortening a longer pause.
func (r *keyAvailabilityRegistry) rateLimit(ref keyRef, model string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	health := r.health(ref)
	health.limits[model] = latest(health.limits[model], r.clock.Now().Add(d))
}

func (r *keyAvailabilityRegistry) report(ref keyRef) KeyAvailability {
	r.mu.Lock()
	defer r.mu.Unlock()
	report := KeyAvailability{KeyID: ref.key, State: KeyAvailable, ReturnedAt: r.returned[ref]}
	health := r.keys[ref]
	if health == nil {
		return report
	}
	now := r.clock.Now()
	for model, until := range health.limits {
		if now.Before(until) {
			report.State = KeyRateLimited
			report.RateLimitedModels = append(report.RateLimitedModels, ModelRateLimit{Model: model, Until: until})
		}
	}
	slices.SortFunc(report.RateLimitedModels, func(a, b ModelRateLimit) int { return cmp.Compare(a.Model, b.Model) })
	if health.down != "" {
		report.State, report.Until = health.down, health.recheck
	}
	return report
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

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
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
