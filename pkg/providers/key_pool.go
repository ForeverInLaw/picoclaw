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
}

// pooledKey is one enabled key: its pool and stable key ID, which rendezvous
// hashing and availability use, its label and a client that logs usage under it.
type pooledKey struct {
	ref    keyRef
	label  string
	client *anthropicprovider.Provider
}

// newKeyPool builds clients for the pool's enabled keys against the model's
// endpoint. A pool without enabled keys leaves the candidate unavailable, so
// the Routing chain moves to its reserves.
func newKeyPool(pool *config.KeyPool, mc *config.ModelConfig) (*keyPool, error) {
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
	return &keyPool{name: pool.Name, keys: keys, availability: keyAvailability}, nil
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

// serve runs one request on the first available key in the conversation's
// rendezvous order. A key that is out of credit or rate limited is marked so
// for every model using the pool and the request moves to the next key. With
// no key left, the error makes the Routing chain move to its reserves.
func (p *keyPool) serve(
	ctx context.Context,
	options map[string]any,
	call func(*anthropicprovider.Provider) (*LLMResponse, error),
) (*LLMResponse, error) {
	conversation, _ := options[ConversationIdentityOption].(string)
	for _, key := range p.rank(conversation) {
		if !p.availability.claim(key.ref) {
			continue
		}
		if response, keyDown, err := p.attempt(ctx, key, call); !keyDown {
			return response, err
		}
	}
	return nil, p.unavailable()
}

// attempt runs call on key. One rate limit of at most shortRateLimitWait is
// waited out on the same key to keep its prompt cache. keyDown reports that
// the key was marked unavailable and the request should move on.
func (p *keyPool) attempt(
	ctx context.Context,
	key pooledKey,
	call func(*anthropicprovider.Provider) (*LLMResponse, error),
) (response *LLMResponse, keyDown bool, err error) {
	for waited := false; ; waited = true {
		response, err = call(key.client)
		if err == nil {
			p.availability.served(key.ref)
			return response, false, nil
		}
		failure := ClassifyError(err, "anthropic", "")
		switch {
		case failure == nil:
			return nil, false, err
		case failure.Reason == FailoverBilling:
			p.availability.exhaust(key.ref)
			p.logKeyDown(key, KeyExhausted, err)
			return nil, true, err
		case failure.Reason == FailoverRateLimit && failure.Status == http.StatusTooManyRequests:
			pause := rateLimitPause(err, failure, p.availability.clock.Now())
			if !waited && pause <= shortRateLimitWait {
				if waitErr := p.availability.wait(ctx, pause); waitErr != nil {
					return nil, false, waitErr
				}
				continue
			}
			p.availability.rateLimit(key.ref, pause)
			p.logKeyDown(key, KeyRateLimited, err)
			return nil, true, err
		default:
			return nil, false, err
		}
	}
}

func (p *keyPool) logKeyDown(key pooledKey, state KeyState, err error) {
	logger.WarnCF("provider.anthropic", "Key pool key unavailable", map[string]any{
		"pool": p.name, "key": key.label, "state": string(state), "error": err.Error(),
	})
}

// unavailable is the error for a request no key could serve: a skipped
// candidate whose retry time is the earliest moment a key may serve again.
func (p *keyPool) unavailable() *CandidateUnavailableError {
	failure := &CandidateUnavailableError{
		Reason:  FailoverBilling,
		Message: fmt.Sprintf("every key in Key pool %q is rate limited or exhausted", p.name),
	}
	for _, key := range p.keys {
		state, until := p.availability.state(key.ref)
		if state == KeyRateLimited {
			failure.Reason = FailoverRateLimit
		}
		if !until.IsZero() && (failure.RetryAt.IsZero() || until.Before(failure.RetryAt)) {
			failure.RetryAt = until
		}
	}
	if failure.RetryAt.IsZero() {
		failure.RetryAt = p.availability.clock.Now()
	}
	return failure
}
