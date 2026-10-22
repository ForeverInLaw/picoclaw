package config

import "strings"

// BindLegacyCodexAccount makes the compatibility connection selectable without
// changing the primary model or any existing endpoint/key configuration.
func (c *Config) BindLegacyCodexAccount(authMethod string) {
	for i := range c.ModelList {
		model := &c.ModelList[i]
		if (strings.HasPrefix(model.Model, "openai/") || !strings.Contains(model.Model, "/")) &&
			(model.AuthMethod == "oauth" || model.AuthMethod == "token") &&
			(model.AccountID == "" || model.AccountID == LegacyOpenAIAccountID) {
			model.AccountID = LegacyOpenAIAccountID
			return
		}
	}
	alias := "gpt-5.4"
	names := make(map[string]bool, len(c.ModelList))
	for _, model := range c.ModelList {
		names[model.ModelName] = true
	}
	for names[alias] {
		alias = "codex-" + alias
	}
	c.ModelList = append(
		c.ModelList,
		ModelConfig{
			ModelName:  alias,
			Model:      "openai/gpt-5.4",
			AuthMethod: authMethod,
			AccountID:  LegacyOpenAIAccountID,
		},
	)
}
