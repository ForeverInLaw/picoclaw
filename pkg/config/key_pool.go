package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// KeyPool is a named, instance-level set of interchangeable Anthropic API keys,
// usually one per provider organization. Configured models reference it by ID
// through ModelConfig.KeyPoolID; several models may share one pool (ADR 0004).
type KeyPool struct {
	ID   string       `json:"id"`
	Name string       `json:"name"`
	Keys []KeyPoolKey `json:"keys"`
}

// KeyPoolKey is one API key of a Key pool. Its ID is stable across label and
// secret edits, so conversation bindings follow the key rather than its value.
// When a .security.yml sidecar exists, it holds APIKey and config.json does not.
// ReturnedAt is when an administrator last returned the key to service; the
// gateway clears the key's exhausted, rejected or rate-limited state once per
// new value, which carries the request across processes.
// MonthlyBudgetUSD (0 for none) and ResetDay (1-31, 0 meaning the 1st) only
// describe the key's credit for the dashboard; they never limit traffic.
type KeyPoolKey struct {
	ID               string    `json:"id"`
	Label            string    `json:"label"`
	Enabled          bool      `json:"enabled"`
	APIKey           string    `json:"api_key,omitempty"`
	ReturnedAt       time.Time `json:"returned_at,omitzero"`
	MonthlyBudgetUSD float64   `json:"monthly_budget_usd,omitempty"`
	ResetDay         int       `json:"reset_day,omitempty"`
}

// MaxKeyBudgetUSD bounds a key's monthly budget.
const MaxKeyBudgetUSD = 1_000_000

// KeyBudgetRange describes the values ValidBudget accepts, for error messages.
const KeyBudgetRange = "a monthly budget of $0 to $1,000,000 and a reset day of 1 to 31, where 0 means the 1st"

// ValidBudget reports whether the key's monthly budget and reset day are in
// range; reset day 0 means the 1st.
func (k KeyPoolKey) ValidBudget() bool {
	return k.MonthlyBudgetUSD >= 0 && k.MonthlyBudgetUSD <= MaxKeyBudgetUSD && k.ResetDay >= 0 && k.ResetDay <= 31
}

// keyPoolsWithoutSecrets copies pools with only their metadata, for a
// config.json whose security sidecar holds the secrets.
func keyPoolsWithoutSecrets(pools []KeyPool) []KeyPool {
	stripped := slices.Clone(pools)
	for i := range stripped {
		stripped[i].Keys = slices.Clone(stripped[i].Keys)
		for j := range stripped[i].Keys {
			stripped[i].Keys[j].APIKey = ""
		}
	}
	return stripped
}

// EnabledKeys returns the keys that may serve requests, in configured order.
func (p *KeyPool) EnabledKeys() []KeyPoolKey {
	return slices.DeleteFunc(slices.Clone(p.Keys), func(k KeyPoolKey) bool { return !k.Enabled })
}

// KeyPool returns the Key pool this model references as Config.ResolveKeyPools
// last attached it; nil for models with their own credentials. It is a copy,
// so later edits to Config.KeyPools never reach it until the next resolve.
func (c *ModelConfig) KeyPool() *KeyPool { return c.keyPool }

// keyPoolByID returns a copy of the pool with id, or nil.
func (c *Config) keyPoolByID(id string) *KeyPool {
	for _, pool := range c.KeyPools {
		if id != "" && pool.ID == id {
			return pool.clone()
		}
	}
	return nil
}

func (p KeyPool) clone() *KeyPool {
	p.Keys = slices.Clone(p.Keys)
	return &p
}

// ResolveKeyPools validates Key pools and attaches each referenced pool to its
// configured models. LoadConfig calls it; callers that edit KeyPools or
// ModelList in memory call it again before creating providers.
func (c *Config) ResolveKeyPools() error {
	pools := make(map[string]*KeyPool, len(c.KeyPools))
	for i := range c.KeyPools {
		pool := &c.KeyPools[i]
		if pool.ID == "" {
			return fmt.Errorf("key_pools[%d]: id is required", i)
		}
		if pools[pool.ID] != nil {
			return fmt.Errorf("duplicate key pool id %q", pool.ID)
		}
		pools[pool.ID] = pool
		keyIDs := make(map[string]bool, len(pool.Keys))
		for j, key := range pool.Keys {
			if key.ID == "" || keyIDs[key.ID] {
				return fmt.Errorf("key pool %q: keys[%d] needs a unique id", pool.ID, j)
			}
			if !key.ValidBudget() {
				return fmt.Errorf("key pool %q: keys[%d] needs %s", pool.ID, j, KeyBudgetRange)
			}
			keyIDs[key.ID] = true
		}
	}
	for i := range c.ModelList {
		m := &c.ModelList[i]
		m.keyPool = nil
		if m.KeyPoolID == "" {
			continue
		}
		pool := pools[m.KeyPoolID]
		if pool == nil {
			return fmt.Errorf("model_list[%d] (%s) references unknown key pool %q", i, m.ModelName, m.KeyPoolID)
		}
		protocol, _, _ := strings.Cut(m.Model, "/")
		if protocol != "anthropic" || m.AuthMethod == "oauth" || m.AuthMethod == "token" {
			return fmt.Errorf("model_list[%d] (%s): key pools serve anthropic/ API-key models only", i, m.ModelName)
		}
		if m.APIKey != "" || len(m.APIKeys) > 0 {
			return fmt.Errorf("model_list[%d] (%s): use either key_pool_id or api_key/api_keys", i, m.ModelName)
		}
		m.keyPool = pool.clone()
	}
	return nil
}

// migrateAnthropicAPIKeysToPools turns each anthropic/ API-key entry with several
// api_keys into a Key pool that the entry references, replacing ordered failover
// for those models (ADR 0004). Entries with the same key list share one pool.
// IDs derive from the model ID, so an unsaved migration binds conversations
// identically on every load until the next configuration write persists it.
func (c *Config) migrateAnthropicAPIKeysToPools() {
	pools := map[string]string{} // key list -> pool ID
	for i := range c.ModelList {
		m := &c.ModelList[i]
		protocol, _, _ := strings.Cut(m.Model, "/")
		keys := MergeAPIKeys(m.APIKey, m.APIKeys)
		if protocol != "anthropic" || m.AuthMethod == "oauth" || m.AuthMethod == "token" ||
			m.KeyPoolID != "" || len(keys) < 2 {
			continue
		}
		signature := strings.Join(keys, "\x00")
		if pools[signature] == "" {
			pool := KeyPool{ID: derivedID("key-pool", m.ID), Name: m.ModelName}
			for j, key := range keys {
				pool.Keys = append(pool.Keys, KeyPoolKey{
					ID:      derivedID("key-pool-key", fmt.Sprintf("%s:%d", pool.ID, j)),
					Label:   fmt.Sprintf("Key %d", j+1),
					Enabled: true,
					APIKey:  key,
				})
			}
			c.KeyPools = append(c.KeyPools, pool)
			pools[signature] = pool.ID
		}
		m.KeyPoolID = pools[signature]
		m.APIKey, m.APIKeys = "", nil
	}
}

func derivedID(kind, seed string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("picoclaw:"+kind+":"+seed)).String()
}
