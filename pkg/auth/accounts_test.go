package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAccountRefreshKeepsClientOwnershipAndConcurrentUpdates(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var refreshes atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refreshes.Add(1) == 1 {
			close(started)
		}
		<-release
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "issued-first" || r.Form.Get("refresh_token") != "rotate-first" || r.Form.Get("resource") != "https://api.openai.com/v1" || r.Form.Get("scope") != "" {
			t.Errorf("wrong refresh ownership: %v", r.Form)
		}
		fmt.Fprint(w, `{"access_token":"fresh-first","refresh_token":"rotated-first","expires_in":3600}`)
	}))
	defer server.Close()
	for _, account := range []*AccountConnection{
		{ID: "first", Label: "First", Provider: "openai", Enabled: true, OAuth: OAuthProviderConfig{Issuer: server.URL, ClientID: "issued-first", TokenURL: server.URL + "/token", Resource: "https://api.openai.com/v1"}, Credential: &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "expired-first", RefreshToken: "rotate-first", ExpiresAt: time.Now().Add(-time.Hour)}},
		{ID: "second", Label: "Second", Provider: "openai", Enabled: true, Credential: &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "second-secret"}},
	} {
		if err := SaveAccount(account); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			cred, err := AccountCredential("first")
			if err != nil {
				t.Error(err)
				return
			}
			if cred.AccessToken != "fresh-first" {
				t.Errorf("token=%s", cred.AccessToken)
			}
		})
	}
	<-started
	if err := UpdateAccount("first", "Renamed during refresh", true); err != nil {
		t.Fatal(err)
	}
	if err := UpdateAccount("second", "Independent update", false); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Errorf("refreshes=%d, want 1", refreshes.Load())
	}
	first, _ := GetAccount("first")
	second, _ := GetAccount("second")
	if first.Label != "Renamed during refresh" || second.Label != "Independent update" || second.Credential.AccessToken != "second-secret" {
		t.Fatalf("concurrent account update lost")
	}
}

func TestLateAccountRefreshCannotRecreateDeletedConnection(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"access_token":"late-secret","expires_in":3600}`)
	}))
	defer server.Close()
	if err := SaveAccount(&AccountConnection{ID: "first", Label: "First", Provider: "openai", Enabled: true, OAuth: OAuthProviderConfig{Issuer: server.URL, ClientID: "first"}, Credential: &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "expired", RefreshToken: "rotating", ExpiresAt: time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := AccountCredential("first"); done <- err }()
	<-started
	if err := DeleteAccount("first"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("deleted session refresh succeeded")
	}
	accounts, err := ListAccounts()
	if err != nil || len(accounts) != 0 {
		t.Fatalf("deleted connection restored: %+v %v", accounts, err)
	}
}

func TestAccountProcessHelper(t *testing.T) {
	if os.Getenv("PICOCLAW_ACCOUNT_PROCESS_TEST") != "refresh" {
		return
	}
	if _, err := AccountCredential("shared"); err != nil {
		t.Fatal(err)
	}
}

func TestAccountRefreshSerializesAcrossProcesses(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Let all processes contend for the same rotating session before replying.
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, `{"access_token":"fresh-process-token","refresh_token":"rotated-process-token","expires_in":3600}`)
	}))
	defer server.Close()
	if err := SaveAccount(&AccountConnection{ID: "shared", Label: "Shared", Provider: "openai", Enabled: true, OAuth: OAuthProviderConfig{Issuer: server.URL, ClientID: "issued-shared"}, Credential: &AuthCredential{AccessToken: "expired", RefreshToken: "rotate-once", Provider: "openai", AuthMethod: "oauth", ExpiresAt: time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 4)
	for i := range commands {
		command := exec.Command(executable, "-test.run=^TestAccountProcessHelper$")
		command.Env = append(os.Environ(), "PICOCLAW_ACCOUNT_PROCESS_TEST=refresh")
		command.Stdout = &bytes.Buffer{}
		command.Stderr = command.Stdout
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands[i] = command
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Errorf("refresh process: %v %s", err, command.Stdout)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("same session refreshed %d times across processes", calls.Load())
	}
}

func TestLateRefreshCannotReplaceChangedRegistration(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"access_token":"late-token","expires_in":3600}`)
	}))
	defer server.Close()
	connection := &AccountConnection{ID: "first", Label: "First", Provider: "openai", Enabled: true, Subject: "original", OAuth: OAuthProviderConfig{Issuer: server.URL, ClientID: "original-client"}, Credential: &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "old", RefreshToken: "rotating", ExpiresAt: time.Now().Add(-time.Hour)}}
	if err := SaveAccount(connection); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := AccountCredential("first"); done <- err }()
	<-started
	connection.OAuth.ClientID = "replacement-client"
	connection.Subject = "replacement"
	if err := SaveAccount(connection); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("old refresh saved under replacement registration")
	}
	saved, err := GetAccount("first")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Credential.AccessToken != "old" || saved.OAuth.ClientID != "replacement-client" {
		t.Fatal("replacement registration was overwritten")
	}
}

func TestAccountRefreshErrorsRetainRecoveryMetadataAndPreserveOtherAccounts(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, scenario := range []struct {
			status      int
			body, code  string
			reauthorize bool
		}{
			{400, `{"error":"invalid_grant","error_description":"PRIVATE_REFRESH_TOKEN"}`, "invalid_grant", true},
			{503, `{"error":"temporarily_unavailable","error_description":"PRIVATE_REFRESH_TOKEN"}`, "temporarily_unavailable", false},
		} {
			t.Run(fmt.Sprintf("registered=%v/status=%d", registered, scenario.status), func(t *testing.T) {
				t.Setenv(config.EnvHome, t.TempDir())
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", "37")
					w.WriteHeader(scenario.status)
					fmt.Fprint(w, scenario.body)
				}))
				defer server.Close()
				account := &AccountConnection{ID: "first", Label: "First", Provider: "openai", Enabled: true, OAuth: OAuthProviderConfig{Issuer: server.URL, ClientID: "issued-first", TokenURL: server.URL + "/token"}, Credential: &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "old-token", RefreshToken: "private", ExpiresAt: time.Now().Add(-time.Hour)}}
				if registered {
					account.Resource = OpenAIRegistrationResource
					account.Scopes = OpenAIPlanScope
					account.OAuth.Resource = account.Resource
				}
				if err := SaveAccount(account); err != nil {
					t.Fatal(err)
				}
				if err := SaveAccount(&AccountConnection{ID: "other", Label: "Other", Provider: "openai", Enabled: true, Credential: &AuthCredential{AccessToken: "untouched"}}); err != nil {
					t.Fatal(err)
				}
				_, err := AccountCredentialContext(t.Context(), "first")
				var failure *TokenRequestError
				if !errors.As(err, &failure) || failure.StatusCode != scenario.status || failure.Code != scenario.code || failure.RetryAfter != 37*time.Second || failure.ReauthorizationRequired != scenario.reauthorize {
					t.Fatalf("refresh recovery metadata lost: %v", err)
				}
				if strings.Contains(err.Error(), "PRIVATE_REFRESH_TOKEN") {
					t.Fatal("refresh diagnostic leaked token body")
				}
				saved, _ := GetAccount("first")
				other, _ := GetAccount("other")
				if (saved.Credential == nil) != scenario.reauthorize || saved.OAuth.ClientID != "issued-first" || other.Credential.AccessToken != "untouched" {
					t.Fatal("refresh lost registration or another account")
				}
			})
		}
	}
}

func TestAccountContextCancellationStopsRefreshAndLockWait(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	started, disconnected := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(disconnected)
	}))
	defer server.Close()
	if err := SaveAccount(&AccountConnection{ID: "first", Label: "First", Provider: "openai", Enabled: true, OAuth: OAuthProviderConfig{Issuer: server.URL, ClientID: "first"}, Credential: &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "old", RefreshToken: "rotating", ExpiresAt: time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := AccountCredentialContext(ctx, "first"); first <- err }()
	<-started
	waiting, stopWaiting := context.WithCancel(t.Context())
	second := make(chan error, 1)
	go func() { _, err := AccountCredentialContext(waiting, "first"); second <- err }()
	stopWaiting()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lock cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request hung on account lock")
	}
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("refresh cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled refresh stayed active")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("refresh HTTP call did not disconnect")
	}
}
