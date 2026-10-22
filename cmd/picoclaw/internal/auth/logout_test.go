package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	authstore "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNewLogoutSubcommand(t *testing.T) {
	cmd := newLogoutCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "Remove stored credentials", cmd.Short)

	assert.True(t, cmd.HasFlags())

	assert.NotNil(t, cmd.Flags().Lookup("provider"))
}

func TestLogoutKeepsCodexModelBindingAndAPIKeyConfiguration(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cfg := config.DefaultConfig()
	cfg.ModelList = []config.ModelConfig{
		{ModelName: "codex", Model: "openai/gpt-5.4", AuthMethod: "oauth", AccountID: config.LegacyOpenAIAccountID},
		{ModelName: "paid", Model: "openai/gpt-5.4", APIKey: "saved-key", APIBase: "https://custom.example/v1"},
	}
	cfg.Agents.Defaults.ModelName = "paid"
	require.NoError(t, config.SaveConfig(internal.GetConfigPath(), cfg))
	require.NoError(
		t,
		authstore.SetCredential(
			"openai",
			&authstore.AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "codex-token"},
		),
	)
	cmd := NewAuthCommand()
	cmd.SetArgs([]string{"logout", "--provider", "openai"})
	require.NoError(t, cmd.Execute())
	saved, err := config.LoadConfig(internal.GetConfigPath())
	require.NoError(t, err)
	assert.Equal(t, "oauth", saved.ModelList[0].AuthMethod)
	assert.Equal(t, config.LegacyOpenAIAccountID, saved.ModelList[0].AccountID)
	assert.Equal(t, "saved-key", saved.ModelList[1].APIKey)
	assert.Equal(t, "https://custom.example/v1", saved.ModelList[1].APIBase)
	assert.Equal(t, "paid", saved.Agents.Defaults.ModelName)
}

func TestLogoutAllKeepsHostAndAccountRegistrationMappings(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	hostID, err := authstore.InstanceHostID()
	require.NoError(t, err)
	require.NoError(
		t,
		authstore.SetCredential(
			"openai",
			&authstore.AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "legacy-token"},
		),
	)
	require.NoError(
		t,
		authstore.SaveAccount(
			&authstore.AccountConnection{
				ID:       "registered",
				Label:    "Registered",
				Provider: "openai",
				Enabled:  true,
				HostID:   hostID,
				Subject:  "verified-subject",
				Issuer:   authstore.OpenAIRegistrationIssuer,
				IDToken:  "retained-id-token",
				Resource: authstore.OpenAIRegistrationResource,
				OAuth:    authstore.OAuthProviderConfig{ClientID: "issued-client"},
				Credential: &authstore.AuthCredential{
					Provider:     "openai",
					AuthMethod:   "oauth",
					AccessToken:  "access",
					RefreshToken: "refresh",
				},
			},
		),
	)
	command := NewAuthCommand()
	command.SetArgs([]string{"logout"})
	require.NoError(t, command.Execute())
	after, err := authstore.InstanceHostID()
	require.NoError(t, err)
	assert.Equal(t, hostID, after)
	registered, err := authstore.GetAccount("registered")
	require.NoError(t, err)
	assert.Equal(t, "issued-client", registered.OAuth.ClientID)
	assert.Equal(t, "verified-subject", registered.Subject)
	assert.Nil(t, registered.Credential)
	assert.Empty(t, registered.IDToken)
	legacy, err := authstore.GetAccount(config.LegacyOpenAIAccountID)
	require.NoError(t, err)
	assert.Nil(t, legacy.Credential)
}
