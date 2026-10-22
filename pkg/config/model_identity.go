package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

const ModelIDPrefix = "model-id:"

// EnsureModelIDs bootstraps legacy identities without secrets or a write on read.
// The first authorized save persists them; subsequent edits retain that ID.
func (c *Config) EnsureModelIDs() {
	occurrences := make(map[string]int)
	used := make(map[string]bool, len(c.ModelList))
	for _, m := range c.ModelList {
		if m.ID != "" {
			used[m.ID] = true
		}
	}
	for i := range c.ModelList {
		m := &c.ModelList[i]
		descriptor, _ := json.Marshal(
			[]string{m.ModelName, m.Model, m.APIBase, m.AccountID, m.AuthMethod, m.ConnectMode, m.Workspace},
		)
		key := string(descriptor)
		occurrence := occurrences[key]
		if m.ID == "" {
			for {
				m.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("picoclaw:model:%s:%d", key, occurrence))).
					String()
				occurrence++
				if !used[m.ID] {
					used[m.ID] = true
					break
				}
			}
		} else {
			occurrence++
		}
		occurrences[key] = occurrence
	}
}

// ConfiguredModels removes runtime-only key expansion before model editing.
func (c *Config) ConfiguredModels() []ModelConfig { return cloneModels(c.modelsForSave()) }

// ValidateRoutingReferences rejects dangling explicit entry references. Legacy
// aliases retain their existing provider/model resolution and round-robin rules.
func (c *Config) ValidateRoutingReferences() error {
	for location, ref := range c.routingReferences() {
		if strings.HasPrefix(ref, ModelIDPrefix) {
			if _, err := c.GetModelConfig(ref); err != nil {
				return fmt.Errorf("%s references unavailable configured model %q", location, ref)
			}
		}
	}
	return nil
}

func (c *Config) routingReferences() map[string]string {
	refs := map[string]string{
		"defaults.primary":     c.Agents.Defaults.GetModelName(),
		"defaults.image_model": c.Agents.Defaults.ImageModel,
	}
	if c.Agents.Defaults.Routing != nil {
		refs["defaults.light_model"] = c.Agents.Defaults.Routing.LightModel
	}
	add := func(prefix string, values []string) {
		for i, v := range values {
			refs[fmt.Sprintf("%s[%d]", prefix, i)] = v
		}
	}
	add("defaults.fallbacks", c.Agents.Defaults.ModelFallbacks)
	add("defaults.image_fallbacks", c.Agents.Defaults.ImageModelFallbacks)
	for _, a := range c.Agents.List {
		if a.Model != nil {
			refs["agents."+a.ID+".primary"] = a.Model.Primary
			add("agents."+a.ID+".fallbacks", a.Model.Fallbacks)
		}
		if a.Subagents != nil && a.Subagents.Model != nil {
			refs["subagents."+a.ID+".primary"] = a.Subagents.Model.Primary
			add("subagents."+a.ID+".fallbacks", a.Subagents.Model.Fallbacks)
		}
	}
	for index, m := range c.ModelList {
		add(fmt.Sprintf("models[%d].%s.fallbacks", index, m.ModelName), m.Fallbacks)
	}
	return refs
}

// ModelReferences identifies the settings that must be changed before deletion.
func (c *Config) ModelReferences(model ModelConfig) []string {
	refs := []string{}
	for location, ref := range c.routingReferences() {
		if ref == ModelIDPrefix+model.ID || ref == model.ModelName {
			refs = append(refs, location)
		}
	}
	return refs
}

func derivedKeyModelID(parent, suffix string, used map[string]bool) string {
	if parent == "" {
		return ""
	}
	base := parent + suffix
	id := base
	for n := 1; used[id]; n++ {
		id = fmt.Sprintf("%s__slot_%d", base, n)
	}
	used[id] = true
	return id
}
