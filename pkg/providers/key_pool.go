package providers

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/sipeed/picoclaw/pkg/config"
	anthropicprovider "github.com/sipeed/picoclaw/pkg/providers/anthropic"
)

// ConversationIdentityOption is the request option naming the conversation a
// request belongs to: its session key. Spawned tasks, subturns and summaries
// pass their parent conversation's identity. Key pool providers bind each
// identity to one key (ADR 0004); other providers ignore it.
const ConversationIdentityOption = "conversation_identity"

// keyPool serves a Key pool model with one Messages API client per enabled key.
type keyPool struct {
	keys []pooledKey
}

// pooledKey is one enabled key: its stable ID, which rendezvous hashing uses,
// and a client that logs usage under the key's label.
type pooledKey struct {
	id     string
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
			id:     key.ID,
			client: newAnthropicAPIKeyClient(key.APIKey, mc).WithKeyLabel(key.Label),
		})
	}
	return &keyPool{keys: keys}, nil
}

// rank orders the keys for a conversation by rendezvous (highest random weight)
// hashing of its identity and each key ID. The first key serves the
// conversation; removing a key moves only the conversations it ranked first.
func (p *keyPool) rank(conversation string) []pooledKey {
	weight := func(key pooledKey) uint64 {
		sum := sha256.Sum256([]byte(conversation + "\x00" + key.id))
		return binary.BigEndian.Uint64(sum[:8])
	}
	ranked := slices.Clone(p.keys)
	slices.SortFunc(ranked, func(a, b pooledKey) int { return cmp.Compare(weight(b), weight(a)) })
	return ranked
}

// serve runs one request on the key bound to the request's conversation.
func (p *keyPool) serve(
	options map[string]any,
	call func(*anthropicprovider.Provider) (*LLMResponse, error),
) (*LLMResponse, error) {
	conversation, _ := options[ConversationIdentityOption].(string)
	return call(p.rank(conversation)[0].client)
}
