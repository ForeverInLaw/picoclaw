package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const CurrentVersion = 3

// Channel holds the v3 instance identity and common channel policy. Existing
// adapter settings stay in their established structs behind Decode.
type Channel struct {
	Enabled            bool                `json:"enabled"`
	Type               string              `json:"type"`
	AllowFrom          FlexibleStringSlice `json:"allow_from,omitempty"`
	ReasoningChannelID string              `json:"reasoning_channel_id,omitempty"`
	GroupTrigger       GroupTriggerConfig  `json:"group_trigger,omitempty"`
	Typing             TypingConfig        `json:"typing,omitempty"`
	Placeholder        PlaceholderConfig   `json:"placeholder,omitempty"`
	Settings           json.RawMessage     `json:"settings"`
	name               string
}

type ChannelList map[string]*Channel

func (c *Channel) Name() string { return c.name }

// Decode adapts one named instance to an existing channel constructor's settings.
func (c *Channel) Decode(target any) error {
	flat, err := c.flatJSON()
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(flat, &fields)
	allowed := jsonFieldTypeMap(reflect.TypeOf(target))
	for key := range BaseFieldNames {
		if _, ok := allowed[key]; !ok {
			delete(fields, key)
		}
	}
	flat, _ = json.Marshal(fields)
	return decodeJSONWithDiagnostics(flat, target, "channel_list."+c.name)
}

func (c *Channel) flatJSON() ([]byte, error) {
	settings := map[string]json.RawMessage{}
	if len(c.Settings) > 0 && string(c.Settings) != "null" {
		if err := json.Unmarshal(c.Settings, &settings); err != nil {
			return nil, err
		}
	}
	type alias Channel
	common, err := json.Marshal((*alias)(c))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(common, &fields); err != nil {
		return nil, err
	}
	for key, value := range fields {
		if key != "type" && key != "settings" {
			settings[key] = value
		}
	}
	return json.Marshal(settings)
}

var (
	channelSettingsMu      sync.RWMutex
	channelSettingsFactory = builtinChannelSettings()
)

func builtinChannelSettings() map[string]any {
	result := map[string]any{}
	t := reflect.TypeOf(ChannelsConfig{})
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		result[strings.Split(field.Tag.Get("json"), ",")[0]] = reflect.New(field.Type).Elem().Interface()
	}
	// Email uses the same extension mechanism as out-of-tree adapters.
	delete(result, "email")
	return result
}

func init() { RegisterChannelSettings("email", EmailConfig{}) }

// RegisterChannelSettings extends v3 validation without modifying the schema.
// A prototype is a struct value, matching the upstream extension contract.
func RegisterChannelSettings(channelType string, prototype any) {
	if reflect.TypeOf(prototype) == nil || reflect.TypeOf(prototype).Kind() != reflect.Struct {
		panic("channel settings prototype must be a struct")
	}
	channelSettingsMu.Lock()
	defer channelSettingsMu.Unlock()
	channelSettingsFactory[channelType] = prototype
}

func newChannelSettings(channelType string) any {
	channelSettingsMu.RLock()
	proto, ok := channelSettingsFactory[channelType]
	channelSettingsMu.RUnlock()
	if !ok {
		return nil
	}
	return reflect.New(reflect.TypeOf(proto)).Interface()
}

var BaseFieldNames = map[string]struct{}{
	"enabled": {}, "allow_from": {}, "reasoning_channel_id": {}, "group_trigger": {}, "typing": {}, "placeholder": {},
}

// ChannelFromLegacy converts flat adapter settings into a v3 instance.
func ChannelFromLegacy(name string, data json.RawMessage) (*Channel, error) {
	var flat map[string]json.RawMessage
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, err
	}
	common := map[string]json.RawMessage{}
	for key := range BaseFieldNames {
		if value, exists := flat[key]; exists {
			common[key] = value
			delete(flat, key)
		}
	}
	commonData, _ := json.Marshal(common)
	var ch Channel
	if err := json.Unmarshal(commonData, &ch); err != nil {
		return nil, err
	}
	ch.Type, ch.name = name, name
	ch.Settings, _ = json.Marshal(flat)
	return &ch, nil
}

func legacyChannelsJSON(ch ChannelsConfig) map[string]json.RawMessage {
	data, _ := json.Marshal(ch)
	var result map[string]json.RawMessage
	_ = json.Unmarshal(data, &result)
	return result
}

// EffectiveChannelList includes edits from typed legacy consumers. The baseline
// records only the adapter values at decode, so edits to canonical v3 instances
// remain authoritative unless that specific legacy adapter was edited too.
func (c *Config) EffectiveChannelList() ChannelList {
	result := make(ChannelList, len(c.ChannelList))
	for name, ch := range c.ChannelList {
		result[name] = ch
	}
	for name, data := range legacyChannelsJSON(c.Channels) {
		if c.legacyChannelBaseline == nil {
			if _, explicit := c.ChannelList[name]; explicit {
				continue
			}
		}
		if c.legacyChannelBaseline != nil && bytes.Equal(data, c.legacyChannelBaseline[name]) {
			continue
		}
		ch, err := ChannelFromLegacy(name, data)
		if err == nil {
			result[name] = ch
		}
	}
	return result
}

// ChannelConfig creates a shallow system config with the selected instance's
// typed channel settings. Factories retain their current constructors.
func (c *Config) ChannelConfig(name string) (*Config, error) {
	ch := c.EffectiveChannelList()[name]
	if ch == nil {
		return nil, fmt.Errorf("channel %q does not exist", name)
	}
	flat, err := ch.flatJSON()
	if err != nil {
		return nil, err
	}
	copyCfg := *c
	copyCfg.Channels = DefaultConfig().Channels
	wrapped, _ := json.Marshal(map[string]json.RawMessage{ch.Type: flat})
	if err := json.Unmarshal(wrapped, &copyCfg.Channels); err != nil {
		return nil, err
	}
	return &copyCfg, nil
}

func (c *Config) IsChannelType(name, channelType string) bool {
	if name == channelType {
		return true
	}
	ch := c.ChannelList[name]
	return ch != nil && ch.Type == channelType
}

func (c *Config) IsNamedChannel(name string) bool {
	ch := c.ChannelList[name]
	return ch != nil && name != ch.Type
}

// ValidateChannels checks settings against the actual fork's serialized structs.
func (c *Config) ValidateChannels() error {
	for name, ch := range c.EffectiveChannelList() {
		if ch == nil {
			return fmt.Errorf("channel_list.%s must be an object", name)
		}
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(strings.ToLower(name)) ||
			strings.ContainsAny(name, ":/\\") {
			return fmt.Errorf(
				"invalid channel instance name %q: use a lowercase name without whitespace, ':' or path separators",
				name,
			)
		}
		ch.name = name
		if ch.Type == "" {
			ch.Type = name
		}
		target := newChannelSettings(ch.Type)
		if target == nil {
			return fmt.Errorf("channel_list.%s has unknown type %q", name, ch.Type)
		}
		var raw any
		if len(ch.Settings) > 0 {
			if err := json.Unmarshal(ch.Settings, &raw); err != nil {
				return wrapJSONError(ch.Settings, err, "channel_list."+name+".settings")
			}
			unknown := collectUnknownJSONFields(raw, reflect.TypeOf(target), "channel_list."+name+".settings")
			for key := range BaseFieldNames {
				if fields, ok := raw.(map[string]any); ok {
					if _, exists := fields[key]; exists {
						unknown = append(unknown, "channel_list."+name+".settings."+key+" (belongs at instance level)")
					}
				}
			}
			if len(unknown) > 0 {
				sort.Strings(unknown)
				return fmt.Errorf("config.json contains unknown field(s): %s", strings.Join(unknown, ", "))
			}
		}
		if err := ch.Decode(target); err != nil {
			return err
		}
	}
	for _, singleton := range []string{"pico", "pico_client"} {
		count := 0
		for _, ch := range c.EffectiveChannelList() {
			if ch.Enabled && ch.Type == singleton {
				count++
			}
		}
		if count > 1 {
			return fmt.Errorf("channel type %q allows one enabled instance", singleton)
		}
	}
	return nil
}

// UnmarshalJSON accepts supported legacy schemas in memory. Loading never
// rewrites a source file, including a read-only container mount.
func (c *Config) UnmarshalJSON(data []byte) error {
	type Alias Config
	wire := struct {
		*Alias
		Channels *ChannelsConfig `json:"channels,omitempty"`
	}{Alias: (*Alias)(c)}
	if err := decodeJSONWithDiagnostics(data, &wire, "config.json"); err != nil {
		return err
	}
	if c.Version < 0 || c.Version > CurrentVersion {
		return fmt.Errorf("unsupported config version %d (maximum %d)", c.Version, CurrentVersion)
	}
	if wire.Channels != nil {
		if c.Version == CurrentVersion {
			return fmt.Errorf("version 3 uses channel_list; migrate legacy channels before setting version to 3")
		}
		if c.ChannelList != nil {
			return fmt.Errorf("config contains both channels and channel_list; keep only one channel schema")
		}
		// Decode on existing defaults to preserve omitted fork settings.
		var root map[string]json.RawMessage
		_ = json.Unmarshal(data, &root)
		if err := json.Unmarshal(root["channels"], &c.Channels); err != nil {
			return err
		}
		c.migrateChannelConfigs()
		c.ChannelList = c.EffectiveChannelList()
	} else if c.ChannelList == nil {
		c.ChannelList = c.EffectiveChannelList()
	} else {
		// Populate only default-named adapters. Named instances are constructed
		// through ChannelConfig and cannot take another instance's credentials.
		for name, ch := range c.ChannelList {
			if ch == nil {
				continue
			}
			ch.name = name
			if ch.Type == "" {
				ch.Type = name
			}
			if name != ch.Type {
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
	}
	c.legacyChannelBaseline = legacyChannelsJSON(c.Channels)
	c.Version = CurrentVersion
	return c.ValidateChannels()
}
