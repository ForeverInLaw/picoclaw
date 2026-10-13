package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v11"
	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// applySecurity keeps the fork's existing .security.yml layout and also accepts
// named v3 channel credentials. Global legacy channel secrets only address the
// legacy-named instance, never another instance of the same platform.
func (c *Config) applySecurity(sec *SecurityConfig) error {
	list := c.EffectiveChannelList()
	legacyData, err := yaml.Marshal(sec.Channels)
	if err != nil {
		return err
	}
	var legacy map[string]map[string]any
	if err := yaml.Unmarshal(legacyData, &legacy); err != nil {
		return err
	}
	for name, settings := range legacy {
		if ch := list[name]; ch != nil {
			if err := mergeChannelSecrets(ch, settings); err != nil {
				return err
			}
		}
	}
	for name, entry := range sec.ChannelList {
		ch := list[name]
		if ch == nil {
			return fmt.Errorf(".security.yml channel_list.%s has no configured instance", name)
		}
		if err := mergeChannelSecrets(ch, entry.Settings); err != nil {
			return err
		}
	}
	c.ChannelList = list
	if err := c.refreshLegacyChannels(); err != nil {
		return err
	}
	modelCounts := map[string]int{}
	for i := range c.ModelList {
		m := &c.ModelList[i]
		index := modelCounts[m.ModelName]
		modelCounts[m.ModelName]++
		entry, ok := sec.ModelList[m.ModelName+":"+strconv.Itoa(index)]
		if !ok && index == 0 {
			entry, ok = sec.ModelList[m.ModelName]
		}
		if ok && len(entry.APIKeys) > 0 {
			m.APIKey = ""
			m.APIKeys = append([]string(nil), entry.APIKeys...)
		}
	}
	// Web and Skill credentials use the established separate security sections.
	if sec.Web.Parallel != nil {
		c.Tools.Web.Parallel.APIKey = ""
		c.Tools.Web.Parallel.APIKeys = sec.Web.Parallel.APIKeys
	}
	if sec.Web.Exa != nil {
		c.Tools.Web.Exa.APIKey = ""
		c.Tools.Web.Exa.APIKeys = sec.Web.Exa.APIKeys
	}
	if sec.Web.Brave != nil {
		c.Tools.Web.Brave.APIKey = ""
		c.Tools.Web.Brave.APIKeys = sec.Web.Brave.APIKeys
	}
	if sec.Web.Tavily != nil {
		c.Tools.Web.Tavily.APIKey = ""
		c.Tools.Web.Tavily.APIKeys = sec.Web.Tavily.APIKeys
	}
	if sec.Web.Perplexity != nil {
		c.Tools.Web.Perplexity.APIKey = ""
		c.Tools.Web.Perplexity.APIKeys = sec.Web.Perplexity.APIKeys
	}
	if sec.Web.GLMSearch != nil {
		c.Tools.Web.GLMSearch.APIKey = sec.Web.GLMSearch.APIKey
	}
	if sec.Skills.Github != nil {
		c.Tools.Skills.Github.Token = sec.Skills.Github.Token
	}
	if sec.Skills.ClawHub != nil {
		c.Tools.Skills.Registries.ClawHub.AuthToken = sec.Skills.ClawHub.AuthToken
	}
	return nil
}

func mergeChannelSecrets(ch *Channel, overlay map[string]any) error {
	var settings map[string]any
	if len(ch.Settings) > 0 {
		if err := json.Unmarshal(ch.Settings, &settings); err != nil {
			return err
		}
	}
	if settings == nil {
		settings = map[string]any{}
	}
	for key, value := range overlay {
		if !isSecretField(key) {
			return fmt.Errorf(".security.yml channel_list.%s.settings.%s is not a credential field", ch.name, key)
		}
		settings[key] = value
	}
	data, err := json.Marshal(settings)
	if err == nil {
		ch.Settings = data
	}
	return err
}

func (c *Config) refreshLegacyChannels() error {
	for name, ch := range c.ChannelList {
		if ch == nil || name != ch.Type {
			continue
		}
		flat, err := ch.flatJSON()
		if err != nil {
			return err
		}
		wrapped, _ := json.Marshal(map[string]json.RawMessage{name: flat})
		if err := json.Unmarshal(wrapped, &c.Channels); err != nil {
			return err
		}
	}
	c.legacyChannelBaseline = legacyChannelsJSON(c.Channels)
	return nil
}

func (c *Config) applyNamedChannelEnv() error {
	list := c.EffectiveChannelList()
	for name, ch := range list {
		if name == ch.Type {
			continue
		}
		target := newChannelSettings(ch.Type)
		if target == nil {
			return fmt.Errorf("unknown channel type %q", ch.Type)
		}
		// Start with platform defaults, then overlay this instance's settings.
		defaults := legacyChannelsJSON(DefaultConfig().Channels)[ch.Type]
		if defaults != nil {
			if err := json.Unmarshal(defaults, target); err != nil {
				return err
			}
		}
		if err := ch.Decode(target); err != nil {
			return err
		}
		legacyPrefix := "PICOCLAW_CHANNELS_" + strings.ToUpper(ch.Type) + "_"
		prefix := "PICOCLAW_CHANNEL_LIST_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_"
		environment := map[string]string{}
		for _, pair := range os.Environ() {
			key, value, _ := strings.Cut(pair, "=")
			if suffix, ok := strings.CutPrefix(key, prefix); ok {
				environment[legacyPrefix+suffix] = value
			}
		}
		if err := env.ParseWithOptions(target, env.Options{Environment: environment}); err != nil {
			return fmt.Errorf("channel_list.%s environment: %w", name, err)
		}
		flat, err := json.Marshal(target)
		if err != nil {
			return err
		}
		// Extension settings prototypes may contain only platform-specific
		// fields. Preserve common policy that the typed adapter does not own.
		original, err := ch.flatJSON()
		if err != nil {
			return err
		}
		var originalFields, fields map[string]json.RawMessage
		_ = json.Unmarshal(original, &originalFields)
		_ = json.Unmarshal(flat, &fields)
		for key := range BaseFieldNames {
			if _, exists := fields[key]; !exists {
				if value, exists := originalFields[key]; exists {
					fields[key] = value
				}
			}
		}
		flat, _ = json.Marshal(fields)
		replacement, err := ChannelFromLegacy(ch.Type, flat)
		if err != nil {
			return err
		}
		replacement.name = name
		list[name] = replacement
	}
	c.ChannelList = list
	return c.refreshLegacyChannels()
}

func isSecretField(name string) bool {
	switch name {
	case "token",
		"bot_token",
		"app_token",
		"auth_token",
		"api_key",
		"api_keys",
		"password",
		"nickserv_password",
		"sasl_password",
		"app_secret",
		"secret",
		"client_secret",
		"corp_secret",
		"encrypt_key",
		"encoding_aes_key",
		"verification_token",
		"access_token",
		"channel_secret",
		"channel_access_token":
		return true
	}
	return false
}

// An existing security sidecar stays authoritative on load. Update its values
// on an explicit save so it cannot restore an old credential over a user's edit.
func saveExistingSecurityConfig(path string, cfg *Config) error {
	secPath := securityPath(path)
	if _, err := os.Stat(secPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	sec, err := loadSecurityConfig(secPath)
	if err != nil {
		return err
	}
	sec.ModelList = map[string]ModelSecurityEntry{}
	counts := map[string]int{}
	for _, model := range cfg.ModelList {
		name := model.ModelName
		index := counts[name]
		counts[name]++
		if index > 0 {
			name += ":" + strconv.Itoa(index)
		}
		sec.ModelList[name] = ModelSecurityEntry{APIKeys: MergeAPIKeys(model.APIKey, model.APIKeys)}
	}
	sec.ChannelList = map[string]ChannelSecurityEntry{}
	for name, ch := range cfg.EffectiveChannelList() {
		var raw map[string]any
		if decodeErr := json.Unmarshal(ch.Settings, &raw); decodeErr != nil {
			return decodeErr
		}
		secrets := map[string]any{}
		for key, value := range raw {
			if isSecretField(key) {
				secrets[key] = value
			}
		}
		if len(secrets) > 0 {
			sec.ChannelList[name] = ChannelSecurityEntry{Settings: secrets}
		}
	}
	web := cfg.Tools.Web
	if sec.Web.Parallel != nil {
		sec.Web.Parallel.APIKeys = MergeAPIKeys(web.Parallel.APIKey, web.Parallel.APIKeys)
	}
	if sec.Web.Exa != nil {
		sec.Web.Exa.APIKeys = MergeAPIKeys(web.Exa.APIKey, web.Exa.APIKeys)
	}
	if sec.Web.Brave != nil {
		sec.Web.Brave.APIKeys = MergeAPIKeys(web.Brave.APIKey, web.Brave.APIKeys)
	}
	if sec.Web.Tavily != nil {
		sec.Web.Tavily.APIKeys = MergeAPIKeys(web.Tavily.APIKey, web.Tavily.APIKeys)
	}
	if sec.Web.Perplexity != nil {
		sec.Web.Perplexity.APIKeys = MergeAPIKeys(web.Perplexity.APIKey, web.Perplexity.APIKeys)
	}
	if sec.Web.GLMSearch != nil {
		sec.Web.GLMSearch.APIKey = web.GLMSearch.APIKey
	}
	if sec.Skills.Github != nil {
		sec.Skills.Github.Token = cfg.Tools.Skills.Github.Token
	}
	if sec.Skills.ClawHub != nil {
		sec.Skills.ClawHub.AuthToken = cfg.Tools.Skills.Registries.ClawHub.AuthToken
	}
	data, err := yaml.Marshal(sec)
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(secPath, data, 0o600)
}

func (c *Config) cacheRuntimeSecrets() {
	data, _ := json.Marshal(c)
	var root any
	_ = json.Unmarshal(data, &root)
	var visit func(any)
	visit = func(value any) {
		switch values := value.(type) {
		case map[string]any:
			for key, item := range values {
				if isSecretField(key) {
					collectSensitive(reflect.ValueOf(item), &c.security.RuntimeValues)
				} else {
					visit(item)
				}
			}
		case []any:
			for _, item := range values {
				visit(item)
			}
		}
	}
	visit(root)
}
