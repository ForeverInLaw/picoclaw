package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
)

const (
	OpenAIRegistrationIssuer      = "https://auth.openai.com"
	OpenAIRegistrationResource    = "https://api.openai.com/v1"
	OpenAIRegistrationRedirect    = "http://127.0.0.1:1455/auth/callback"
	OpenAIRegistrationEntryClient = "dynamic_agent_client"
	OpenAIPlanScope               = "chatgpt.tokens.use.direct"
)

func OpenAIRegistrationConfig() OAuthProviderConfig {
	return OAuthProviderConfig{
		Issuer: OpenAIRegistrationIssuer, ClientID: OpenAIRegistrationEntryClient,
		TokenURL: OpenAIRegistrationIssuer + "/api/accounts/oauth/token", Resource: OpenAIRegistrationResource,
		Scopes: "openid profile email offline_access resource.invoke " + OpenAIPlanScope,
	}
}

// InstanceHostID survives account changes and process restarts in protected storage.
func InstanceHostID() (string, error) { return InstanceHostIDContext(context.Background()) }

func InstanceHostIDContext(ctx context.Context) (string, error) {
	unlock, err := lockStoreContext(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return "", err
	}
	if store.HostID != "" {
		return store.HostID, nil
	}
	store.HostID = "urn:uuid:" + uuid.NewString()
	if err := saveStore(store); err != nil {
		return "", err
	}
	return store.HostID, nil
}

// ExchangeRegisteredAccount uses the issued public-client ID, then verifies the
// signed ID token before any credentials become visible to model routing.
func ExchangeRegisteredAccount(ctx context.Context, cfg OAuthProviderConfig, code, verifier, redirect, nonce, hostID string, previous *AccountConnection) (*AccountConnection, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 15 * time.Second}
	ctx = oidc.ClientContext(ctx, client)
	token, err := requestRegisteredToken(ctx, cfg, url.Values{"grant_type": {"authorization_code"}, "client_id": {cfg.ClientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {cfg.Resource}})
	if err != nil {
		return nil, err
	}
	return registeredTokenAccount(ctx, cfg, token, nonce, hostID, previous)
}

func registeredTokenAccount(ctx context.Context, cfg OAuthProviderConfig, token *registeredToken, nonce, hostID string, previous *AccountConnection) (*AccountConnection, error) {
	rawIDToken := token.IDToken
	if rawIDToken == "" && previous != nil && nonce == "" {
		copy := *previous
		copy.Credential = registeredCredential(token, previous.Credential.Email)
		if token.Scope != "" {
			copy.Scopes = token.Scope
		}
		return &copy, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, errors.New("OpenAI identity verification unavailable; start a fresh sign-in")
	}
	verified, err := provider.VerifierContext(ctx, &oidc.Config{ClientID: cfg.ClientID}).Verify(ctx, rawIDToken)
	if err != nil || verified.Subject == "" || (nonce != "" && verified.Nonce != nonce) {
		return nil, errors.New("OpenAI identity verification failed")
	}
	if previous != nil && (verified.Subject != previous.Subject || verified.Issuer != previous.Issuer || cfg.ClientID != previous.OAuth.ClientID) {
		return nil, errors.New("OpenAI sign-in does not match the selected account registration")
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := verified.Claims(&claims); err != nil {
		return nil, errors.New("OpenAI identity claims are invalid")
	}
	scopes := token.Scope
	account := &AccountConnection{ID: uuid.NewString(), Label: claims.Email, Provider: "openai", Enabled: true, Subject: verified.Subject, Issuer: verified.Issuer, HostID: hostID, IDToken: rawIDToken, Resource: cfg.Resource, Scopes: scopes, OAuth: cfg, Credential: registeredCredential(token, claims.Email)}
	if previous != nil {
		account.ID = previous.ID
		account.Label = previous.Label
		account.Enabled = previous.Enabled
		if scopes == "" {
			account.Scopes = previous.Scopes
		}
	}
	if account.Label == "" {
		account.Label = "ChatGPT account"
	}
	return account, nil
}

func registeredCredential(token *registeredToken, email string) *AuthCredential {
	return &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second), Email: email}
}

func (a *AccountConnection) CanUsePlan() bool {
	if a.Resource == "" {
		return true
	}
	for _, scope := range strings.Fields(a.Scopes) {
		if scope == OpenAIPlanScope {
			return true
		}
	}
	return false
}

func refreshRegisteredAccount(ctx context.Context, account *AccountConnection) (*AccountConnection, error) {
	cfg := account.OAuth
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: 15 * time.Second})
	token, err := requestRegisteredToken(ctx, cfg, url.Values{"grant_type": {"refresh_token"}, "client_id": {cfg.ClientID}, "refresh_token": {account.Credential.RefreshToken}, "resource": {cfg.Resource}})
	if err != nil {
		return nil, err
	}
	if token.RefreshToken == "" {
		token.RefreshToken = account.Credential.RefreshToken
	}
	return registeredTokenAccount(ctx, cfg, token, "", account.HostID, account)
}

// Token fields are decoded only at this external boundary and never returned by status APIs.
type registeredToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

func requestRegisteredToken(ctx context.Context, cfg OAuthProviderConfig, form url.Values) (*registeredToken, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("OpenAI token request could not be prepared")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &TokenRequestError{Wrapped: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, tokenEndpointError(response)
	}
	var token registeredToken
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token); err != nil || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || token.ExpiresIn <= 0 {
		return nil, errors.New("OpenAI token response is invalid")
	}
	return &token, nil
}

// SaveRegisteredAccount preserves live labels/enabled state and rejects a
// registration deleted or changed while browser authorization was pending.
func SaveRegisteredAccount(account, previous *AccountConnection) (*AccountConnection, error) {
	return SaveRegisteredAccountContext(context.Background(), account, previous)
}

func SaveRegisteredAccountContext(ctx context.Context, account, previous *AccountConnection) (*AccountConnection, error) {
	unlock, err := lockStoreContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return nil, err
	}
	if previous != nil {
		current := store.Accounts[previous.ID]
		if current == nil || current.Subject != previous.Subject || current.Issuer != previous.Issuer || current.OAuth.ClientID != previous.OAuth.ClientID {
			return nil, errors.New("selected account registration changed")
		}
		account.Label = current.Label
		account.Enabled = current.Enabled
	} else {
		for _, current := range store.Accounts {
			if current.OAuth.ClientID == account.OAuth.ClientID {
				if current.Subject != account.Subject || current.Issuer != account.Issuer {
					return nil, errors.New("issued client identity does not match saved registration")
				}
				account.ID = current.ID
				account.Label = current.Label
				account.Enabled = current.Enabled
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.Accounts[account.ID] = account
	if err := saveStore(store); err != nil {
		return nil, err
	}
	return account, nil
}
