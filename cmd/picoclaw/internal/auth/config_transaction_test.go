package auth

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	authstore "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

type deviceLoginTransport func(*http.Request) (*http.Response, error)

func (transport deviceLoginTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestDeviceLoginSerializesAccountBindingWithAdministratorConfigTransaction(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	path := internal.GetConfigPath()
	cfg := config.DefaultConfig()
	cfg.ModelList = []config.ModelConfig{{ModelName: "paid", Model: "openai/shared", APIKey: "initial-key"}}
	cfg.Agents.Defaults.ModelName = "paid"
	require.NoError(t, config.SaveConfig(path, cfg))
	require.NoError(
		t,
		authstore.SetCredential(
			"openai",
			&authstore.AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "old-token"},
		),
	)
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	var networkDone chan struct{}
	http.DefaultTransport = deviceLoginTransport(func(request *http.Request) (*http.Response, error) {
		var body string
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			body = `{"device_auth_id":"device","user_code":"TEST","interval":1}`
		case "/api/accounts/deviceauth/token":
			body = `{"authorization_code":"code","code_verifier":"verifier"}`
		case "/oauth/token":
			body = `{"access_token":"new-token","refresh_token":"refresh","expires_in":3600}`
			close(networkDone)
		default:
			t.Errorf("unexpected network call %s", request.URL.Path)
			body = `{}`
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	synctest.Test(t, func(t *testing.T) {
		networkDone = make(chan struct{})
		locked, release := make(chan struct{}), make(chan struct{})
		mutationDone := make(chan error, 1)
		go func() {
			mutationDone <- config.WithConfigMutation(path, func() error {
				close(locked)
				<-release
				latest, err := config.LoadConfig(path)
				if err != nil {
					return err
				}
				latest.ModelList[0].APIKey = "admin-key"
				if err = config.SaveConfig(path, latest); err != nil {
					return err
				}
				return authstore.DeleteAccount(config.LegacyOpenAIAccountID)
			})
		}()
		<-locked
		commandDone := make(chan error, 1)
		go func() {
			command := NewAuthCommand()
			command.SetArgs([]string{"login", "--provider", "openai", "--device-code"})
			commandDone <- command.Execute()
		}()
		<-networkDone
		synctest.Wait()
		completedEarly := false
		select {
		case err := <-commandDone:
			require.NoError(t, err)
			completedEarly = true
		default:
		}
		close(release)
		require.NoError(t, <-mutationDone)
		if !completedEarly {
			require.NoError(t, <-commandDone)
		}
		require.False(
			t,
			completedEarly,
			"login must wait to save tokens and bind configuration until the administrator transaction releases its guard",
		)
	})
	saved, err := config.LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "paid", saved.Agents.Defaults.GetModelName())
	require.Equal(t, "admin-key", saved.ModelList[0].APIKey)
	account, err := authstore.GetAccount(config.LegacyOpenAIAccountID)
	require.NoError(t, err)
	require.Equal(t, "new-token", account.Credential.AccessToken)
	bound := false
	for _, model := range saved.ModelList {
		bound = bound || model.AccountID == account.ID
	}
	require.True(t, bound)
}
