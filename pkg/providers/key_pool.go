package providers

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http"
	"slices"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/keyspend"
	"github.com/sipeed/picoclaw/pkg/logger"
	anthropicprovider "github.com/sipeed/picoclaw/pkg/providers/anthropic"
)

// ConversationIdentityOption is the request option naming the conversation a
// request belongs to: its session key. Spawned tasks, subturns and summaries
// pass their parent conversation's identity. Key pool providers bind each
// identity to one key (ADR 0004); other providers ignore it.
const ConversationIdentityOption = "conversation_identity"

// keyPool serves a Key pool model with one Messages API client per enabled key.
// Key availability lives in a registry shared by every model using the pool.
type keyPool struct {
	name         string
	keys         []pooledKey
	availability *keyAvailabilityRegistry
	cacheTTL     string // prices this model's cache writes
}

// pooledKey is one enabled key: its pool and stable key ID, which rendezvous
// hashing and availability use, its label and a client that logs usage under it.
type pooledKey struct {
	ref    keyRef
	label  string
	client *anthropicprovider.Provider
}

// newKeyPool builds clients for the pool's enabled keys against the model's
// endpoint, after applying the pool's returns to service. A pool without
// enabled keys leaves the candidate unavailable, so the Routing chain moves to
// its reserves.
func newKeyPool(pool *config.KeyPool, mc *config.ModelConfig) (*keyPool, error) {
	keyAvailability.applyReturns(pool)
	enabled := pool.EnabledKeys()
	if len(enabled) == 0 {
		return nil, &CandidateUnavailableError{
			Reason:  FailoverAuth,
			Message: fmt.Sprintf("Key pool %q has no enabled keys", pool.Name),
		}
	}
	keys := make([]pooledKey, 0, len(enabled))
	for _, key := range enabled {
		keys = append(keys, pooledKey{
			ref:    keyRef{pool: pool.ID, key: key.ID},
			label:  key.Label,
			client: newAnthropicAPIKeyClient(key.APIKey, mc).WithKeyLabel(key.Label),
		})
	}
	return &keyPool{name: pool.Name, keys: keys, availability: keyAvailability, cacheTTL: mc.CacheTTL}, nil
}

// NewKeyPoolKeyProbe builds a provider that sends mc's requests through key
// alone: no other key of its pool, no key availability tracking and no
// server-side refusal fallback, so each result belongs to that key and model.
func NewKeyPoolKeyProbe(key config.KeyPoolKey, mc *config.ModelConfig) LLMProvider {
	client := newAnthropicAPIKeyClient(key.APIKey, mc).WithKeyLabel(key.Label)
	return newClaudeProviderWithDelegate(client.WithoutServerFallback())
}

// rank orders the keys for a conversation by rendezvous (highest random weight)
// hashing of its identity and each key ID. The first key serves the
// conversation; removing a key moves only the conversations it ranked first.
func (p *keyPool) rank(conversation string) []pooledKey {
	weight := func(key pooledKey) uint64 {
		sum := sha256.Sum256([]byte(conversation + "\x00" + key.ref.key))
		return binary.BigEndian.Uint64(sum[:8])
	}
	ranked := slices.Clone(p.keys)
	slices.SortFunc(ranked, func(a, b pooledKey) int { return cmp.Compare(weight(b), weight(a)) })
	return ranked
}

// serve runs one request for model on the first available key in the
// conversation's rendezvous order. A key that is out of credit or rejected is
// marked so for every model using the pool, a rate limit only for model, and
// the request moves to the next key. With no key left, the error makes the
// Routing chain move to its reserves.
func (p *keyPool) serve(
	ctx context.Context,
	model string,
	options map[string]any,
	call func(*anthropicprovider.Provider) (*LLMResponse, error),
) (*LLMResponse, error) {
	conversation, _ := options[ConversationIdentityOption].(string)
	for _, key := range p.rank(conversation) {
		if !p.availability.claim(key.ref, model) {
			continue
		}
		if response, keyDown, err := p.attempt(ctx, key, model, call); !keyDown {
			return response, err
		}
	}
	return nil, p.unavailable(model)
}

// attempt runs call on key. One rate limit of at most shortRateLimitWait is
// waited out on the same key to keep its prompt cache. keyDown reports that
// the key was marked unavailable and the request should move on.
func (p *keyPool) attempt(
	ctx context.Context,
	key pooledKey,
	model string,
	call func(*anthropicprovider.Provider) (*LLMResponse, error),
) (response *LLMResponse, keyDown bool, err error) {
	for waited := false; ; waited = true {
		response, err = call(key.client)
		if err == nil {
			p.availability.served(key.ref)
			p.recordSpend(key, model, response)
			return response, false, nil
		}
		failure := ClassifyError(err, "anthropic", model)
		switch {
		case failure == nil:
			return nil, false, err
		case failure.Reason == FailoverBilling:
			p.takeDown(key, KeyExhausted, err)
			return nil, true, err
		case failure.Reason == FailoverAuth &&
			(failure.Status == http.StatusUnauthorized || failure.Status == http.StatusForbidden):
			p.takeDown(key, KeyRejected, err)
			return nil, true, err
		case failure.Reason == FailoverRateLimit && failure.Status == http.StatusTooManyRequests:
			pause := rateLimitPause(err, failure, p.availability.clock.Now())
			if !waited && pause <= shortRateLimitWait {
				if waitErr := p.availability.wait(ctx, pause); waitErr != nil {
					return nil, false, waitErr
				}
				continue
			}
			p.availability.rateLimit(key.ref, model, pause)
			p.logKeyDown(key, KeyRateLimited, model, err)
			return nil, true, err
		default:
			return nil, false, err
		}
	}
}

// recordSpend adds an answer's usage to the key's monthly totals, priced for
// the model that served it. Every configured model using the pool adds to the
// same totals. A failed write is logged and never fails the answer.
func (p *keyPool) recordSpend(key pooledKey, model string, response *LLMResponse) {
	usage := response.Usage
	if usage == nil {
		return
	}
	spend := keyspend.Totals{
		InputTokens:      int64(usage.UncachedPromptTokens()),
		OutputTokens:     int64(usage.CompletionTokens),
		CacheWriteTokens: int64(usage.CacheCreationTokens),
		CacheReadTokens:  int64(usage.CacheReadTokens),
	}
	served := cmp.Or(response.ServedModel, model)
	if usd, priced := anthropicprovider.EstimateCost(served, *usage, p.cacheTTL); priced {
		spend.EstimatedUSD = usd
	} else {
		spend.UnpricedTokens = int64(usage.TotalTokens)
	}
	if err := keyspend.Record(key.ref.pool, key.ref.key, p.availability.clock.Now(), spend); err != nil {
		logger.WarnCF("provider.anthropic", "Key pool spend not recorded",
			map[string]any{"pool": p.name, "key": key.label, "error": err.Error()})
	}
}

func (p *keyPool) takeDown(key pooledKey, state KeyState, err error) {
	p.availability.takeDown(key.ref, state)
	p.logKeyDown(key, state, "", err)
}

func (p *keyPool) logKeyDown(key pooledKey, state KeyState, model string, err error) {
	fields := map[string]any{"pool": p.name, "key": key.label, "state": string(state), "error": err.Error()}
	if model != "" {
		fields["model"] = model
	}
	logger.WarnCF("provider.anthropic", "Key pool key unavailable", fields)
}

// unavailable is the error for a request no key could serve: a skipped
// candidate whose retry time is the earliest moment a key may serve model
// again. Its reason is a rate limit if any key is rate limited, else billing if
// any is exhausted, else auth.
func (p *keyPool) unavailable(model string) *CandidateUnavailableError {
	failure := &CandidateUnavailableError{
		Reason:  FailoverAuth,
		Message: fmt.Sprintf("every key in Key pool %q is rate limited, exhausted or rejected", p.name),
	}
	limited, exhausted := false, false
	for _, key := range p.keys {
		state, until := p.availability.blockedNow(key.ref, model)
		limited = limited || state == KeyRateLimited
		exhausted = exhausted || state == KeyExhausted
		if !until.IsZero() && (failure.RetryAt.IsZero() || until.Before(failure.RetryAt)) {
			failure.RetryAt = until
		}
	}
	switch {
	case limited:
		failure.Reason = FailoverRateLimit
	case exhausted:
		failure.Reason = FailoverBilling
	}
	if failure.RetryAt.IsZero() {
		failure.RetryAt = p.availability.clock.Now()
	}
	return failure
}
