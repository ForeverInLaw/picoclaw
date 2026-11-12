package config

import (
	"net/url"
	"strings"
)

// imageCapabilityVersion is the first config version where capabilities.images
// is written explicitly for every model entry.
const imageCapabilityVersion = 4

// migrateImageCapabilities makes image support explicit for configs written
// before version 4, where an unset flag deferred to the protocol adapter.
func (c *Config) migrateImageCapabilities() {
	if c.sourceVersion < imageCapabilityVersion {
		c.fillImageCapabilities()
	}
}

// fillImageCapabilities sets every unset capabilities.images flag. Anthropic
// and first-party OpenAI models accept images, as does any model the agent
// already routes images to. Every other unset entry becomes text-only.
func (c *Config) fillImageCapabilities() {
	imageRefs := map[string]bool{}
	for _, ref := range append([]string{c.Agents.Defaults.ImageModel}, c.Agents.Defaults.ImageModelFallbacks...) {
		if ref = strings.TrimSpace(ref); ref != "" {
			imageRefs[ref] = true
		}
	}
	for i := range c.ModelList {
		m := &c.ModelList[i]
		if m.Capabilities != nil && m.Capabilities.Images != nil {
			continue
		}
		accepts := hasNativeImageInput(m) || imageRefs[m.ModelName] || imageRefs[ModelIDPrefix+m.ID]
		if m.Capabilities == nil {
			m.Capabilities = &ModelCapabilities{}
		} else {
			copied := *m.Capabilities
			m.Capabilities = &copied
		}
		m.Capabilities.Images = &accepts
	}
}

// hasNativeImageInput identifies Anthropic models and OpenAI's own endpoints,
// whose current models all take image input. OpenAI-compatible third-party
// endpoints are excluded because the protocol says nothing about the model.
func hasNativeImageInput(m *ModelConfig) bool {
	protocol, _, found := strings.Cut(m.Model, "/")
	if !found {
		protocol = "openai"
	}
	switch protocol {
	case "anthropic":
		return true
	case "openai":
		if m.AuthMethod == "oauth" || m.AuthMethod == "token" || strings.TrimSpace(m.APIBase) == "" {
			return true
		}
		base, err := url.Parse(m.APIBase)
		return err == nil && strings.EqualFold(base.Hostname(), "api.openai.com")
	}
	return false
}
