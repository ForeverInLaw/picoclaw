package api

import (
	"errors"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func modelCredentialPlaceholder(key string) bool {
	return strings.Contains(key, "****") || strings.HasPrefix(key, "file://") || strings.HasPrefix(key, "enc://")
}

// Retention is tied to the selected entry and its credential destination, never
// another model with the same alias. Endpoint/proxy edits require a new key.
func compatibleModelCredentials(edited, saved config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(edited.Model)
	savedProtocol, _ := providers.ExtractProtocol(saved.Model)
	base := func(mc config.ModelConfig, protocol string) string {
		value := strings.TrimSpace(mc.APIBase)
		if value == "" {
			value = providers.DefaultAPIBaseForProtocol(protocol)
		}
		return strings.TrimRight(value, "/")
	}
	return edited.ModelName == saved.ModelName && protocol == savedProtocol &&
		base(edited, protocol) == base(saved, savedProtocol) && edited.AuthMethod == saved.AuthMethod &&
		edited.Proxy == saved.Proxy && edited.AccountID == saved.AccountID
}

func retainModelCredential(edited *config.ModelConfig, saved config.ModelConfig) error {
	compatible := compatibleModelCredentials(*edited, saved)
	if edited.APIKey == "" || modelCredentialPlaceholder(edited.APIKey) {
		if compatible {
			edited.APIKey = saved.APIKey
		} else if edited.APIKey != "" {
			return errors.New("Enter a new API key when changing the selected model's provider, endpoint or proxy.")
		}
	}
	if edited.APIKeys == nil && compatible {
		edited.APIKeys = saved.APIKeys
	}
	for _, key := range edited.APIKeys {
		if modelCredentialPlaceholder(key) {
			return errors.New(
				"Use an explicit replacement API key or keep the selected saved configuration; masked key lists cannot be saved or tested.",
			)
		}
	}
	return nil
}
