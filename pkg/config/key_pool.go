package config

import (
	"fmt"
	"slices"
	"strings"
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
type KeyPoolKey struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	APIKey  string `json:"api_key"`
}

// EnabledKeys returns the keys that may serve requests, in configured order.
func (p *KeyPool) EnabledKeys() []KeyPoolKey {
	return slices.DeleteFunc(slices.Clone(p.Keys), func(k KeyPoolKey) bool { return !k.Enabled })
}

// KeyPool returns the Key pool this model references, once Config.ResolveKeyPools
// has attached it; nil for models with their own credentials.
func (c *ModelConfig) KeyPool() *KeyPool { return c.keyPool }

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
		m.keyPool = pool
	}
	return nil
}
