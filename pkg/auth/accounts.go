package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

var ErrAccountNotFound = errors.New("account connection not found")

// AccountConnection owns one renewable session. OAuth client metadata and tokens
// stay in the protected auth store; dashboard responses use a separate view.
type AccountConnection struct {
	LastProbe      *AccountProbe       `json:"last_probe,omitempty"`
	LastSuccessAt  time.Time           `json:"last_success_at,omitzero"`
	SessionVersion string              `json:"session_version,omitempty"`
	ID             string              `json:"id"`
	Label          string              `json:"label"`
	Provider       string              `json:"provider"`
	Enabled        bool                `json:"enabled"`
	Subject        string              `json:"subject,omitempty"`
	Issuer         string              `json:"issuer,omitempty"`
	HostID         string              `json:"host_id,omitempty"`
	IDToken        string              `json:"id_token,omitempty"`
	Resource       string              `json:"resource,omitempty"`
	Scopes         string              `json:"scopes,omitempty"`
	OAuth          OAuthProviderConfig `json:"oauth"`
	Credential     *AuthCredential     `json:"credential,omitempty"`
}

func GetAccount(id string) (*AccountConnection, error) {
	return GetAccountContext(context.Background(), id)
}

func GetAccountContext(ctx context.Context, id string) (*AccountConnection, error) {
	store, err := LoadStoreContext(ctx)
	if err != nil {
		return nil, err
	}
	account := store.Accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

func ListAccounts() ([]*AccountConnection, error) {
	store, err := LoadStore()
	if err != nil {
		return nil, err
	}
	accounts := make([]*AccountConnection, 0, len(store.Accounts))
	for _, account := range store.Accounts {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	return accounts, nil
}

// SaveAccount atomically updates one connection without replacing other sessions.
// New connections must use an opaque ID generated after identity validation.
func SaveAccount(account *AccountConnection) error {
	if account == nil || account.ID == "" || account.Provider != "openai" {
		return errors.New("invalid account connection")
	}
	unlock, err := lockStore()
	if err != nil {
		return err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return err
	}
	if current := store.Accounts[account.ID]; sameAccountSession(current, account) {
		account.SessionVersion = current.SessionVersion
	} else {
		account.SessionVersion = uuid.NewString()
	}
	store.Accounts[account.ID] = account
	if account.ID == config.LegacyOpenAIAccountID {
		store.Credentials["openai"] = account.Credential
	}
	return saveStore(store)
}

// PatchAccount applies only provided fields under the store lock.
func PatchAccount(id string, label *string, enabled *bool) error {
	unlock, err := lockStore()
	if err != nil {
		return err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return err
	}
	account := store.Accounts[id]
	if account == nil {
		return ErrAccountNotFound
	}
	if label != nil {
		account.Label = *label
	}
	if enabled != nil {
		account.Enabled = *enabled
	}
	return saveStore(store)
}

func UpdateAccount(id, label string, enabled bool) error { return PatchAccount(id, &label, &enabled) }

func DeleteAccount(id string) error {
	unlock, err := lockStore()
	if err != nil {
		return err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return err
	}
	if store.Accounts[id] == nil {
		return ErrAccountNotFound
	}
	delete(store.Accounts, id)
	if id == config.LegacyOpenAIAccountID {
		delete(store.Credentials, "openai")
	}
	return saveStore(store)
}

var accountRefreshLocks sync.Map

// AccountCredential serializes rotating-token refresh per account and reloads
// the latest record before writing, preserving concurrent metadata/other accounts.
// A deleted, disabled or reconnected session cannot be resurrected by a late refresh.
func AccountCredential(id string) (*AuthCredential, error) {
	return AccountCredentialContext(context.Background(), id)
}

func AccountCredentialContext(ctx context.Context, id string) (*AuthCredential, error) {
	key := authFilePath() + "\x00" + id
	value, _ := accountRefreshLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	if err := acquireAuthMutex(ctx, mu); err != nil {
		return nil, err
	}
	defer mu.Unlock()
	refreshFile, err := fileutil.LockFile(
		ctx,
		fmt.Sprintf("%s.refresh-%x.lock", authFilePath(), sha256.Sum256([]byte(id))),
	)
	if err != nil {
		return nil, err
	}
	defer refreshFile.Close()
	unlockRead, err := lockStoreContext(ctx)
	if err != nil {
		return nil, err
	}
	loaded, err := loadStore()
	unlockRead()
	if err != nil {
		return nil, err
	}
	account := loaded.Accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	if !account.CanUsePlan() {
		return nil, &TokenRequestError{StatusCode: 403, Code: "consent_required", ReauthorizationRequired: true}
	}
	if !account.Enabled {
		return nil, fmt.Errorf("account connection %q is disabled", id)
	}
	cred := account.Credential
	if cred == nil {
		return nil, fmt.Errorf("account connection %q requires authorization", id)
	}
	if cred.AuthMethod != "oauth" || !cred.NeedsRefresh() || cred.RefreshToken == "" {
		return cred, nil
	}
	var refreshed *AuthCredential
	var registered *AccountConnection
	if account.Resource != "" {
		registered, err = refreshRegisteredAccount(ctx, account)
		if err == nil {
			refreshed = registered.Credential
		}
	} else {
		refreshed, err = RefreshAccessTokenContext(ctx, cred, account.OAuth)
	}
	if err != nil {
		var tokenError *TokenRequestError
		if errors.As(err, &tokenError) && tokenError.ReauthorizationRequired {
			if markErr := clearRejectedAccountSession(ctx, account); markErr != nil {
				return nil, markErr
			}
		}
		return nil, err
	}
	unlock, err := lockStoreContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return nil, err
	}
	current := store.Accounts[id]
	if current == nil {
		return nil, ErrAccountNotFound
	}
	if !sameAccountSession(current, account) {
		return nil, errors.New("account authorization changed during refresh")
	}
	current.Credential = refreshed
	if registered != nil {
		current.IDToken = registered.IDToken
		current.Scopes = registered.Scopes
	}
	if id == config.LegacyOpenAIAccountID {
		store.Credentials["openai"] = refreshed
	}
	// Rotation may invalidate the old refresh token even after an administrator
	// disables the connection. Save the new session under CAS, then block its use.
	if err := saveStore(store); err != nil {
		return nil, err
	}
	if !current.Enabled {
		return nil, fmt.Errorf("account connection %q is disabled", id)
	}
	if !current.CanUsePlan() {
		return nil, &TokenRequestError{StatusCode: 403, Code: "consent_required", ReauthorizationRequired: true}
	}
	return refreshed, nil
}

func sameAccountSession(current, expected *AccountConnection) bool {
	return current != nil && current.SessionVersion == expected.SessionVersion && current.Credential != nil &&
		expected.Credential != nil &&
		*current.Credential == *expected.Credential &&
		current.OAuth == expected.OAuth &&
		current.Subject == expected.Subject &&
		current.Issuer == expected.Issuer &&
		current.HostID == expected.HostID &&
		current.IDToken == expected.IDToken &&
		current.Resource == expected.Resource &&
		current.Scopes == expected.Scopes
}

func clearRejectedAccountSession(ctx context.Context, expected *AccountConnection) error {
	unlock, err := lockStoreContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return err
	}
	current := store.Accounts[expected.ID]
	if !sameAccountSession(current, expected) {
		return errors.New("account authorization changed during refresh")
	}
	current.Credential = nil
	current.IDToken = ""
	if current.ID == config.LegacyOpenAIAccountID {
		delete(store.Credentials, "openai")
	}
	return saveStore(store)
}

// AccountAuthorization classifies saved authorization without refreshing or probing.
type AccountAuthorization string

const (
	AccountAuthorized              AccountAuthorization = "authorized"
	AccountReauthorizationRequired AccountAuthorization = "reauthorization_required"
	AccountPlanPermissionRequired  AccountAuthorization = "plan_permission_required"
	AccountDisabled                AccountAuthorization = "disabled"
)

// AccountSessionState is a local snapshot; reading it never refreshes or probes.
type AccountSessionState struct {
	LastSuccessfulProbeAt   time.Time
	SessionVersion          string
	Authorization           AccountAuthorization
	Enabled                 bool
	RequiresReauthorization bool
}

// LocalState keeps credential, plan and enabled policy consistent for all callers.
func (account *AccountConnection) LocalState() AccountSessionState {
	state := AccountSessionState{
		LastSuccessfulProbeAt: account.LastSuccessAt,
		SessionVersion:        account.SessionVersion,
		Enabled:               account.Enabled,
		Authorization:         AccountAuthorized,
	}
	credential := account.Credential
	switch {
	case !account.CanUsePlan():
		state.Authorization = AccountPlanPermissionRequired
	case credential == nil || credential.AccessToken == "" ||
		(credential.IsExpired() && credential.RefreshToken == ""):
		state.Authorization = AccountReauthorizationRequired
	}
	state.RequiresReauthorization = state.Authorization != AccountAuthorized
	if !state.Enabled {
		state.Authorization = AccountDisabled
	}
	return state
}

func AccountState(id string) (AccountSessionState, error) {
	return AccountStateContext(context.Background(), id)
}

func AccountStateContext(ctx context.Context, id string) (AccountSessionState, error) {
	account, err := GetAccountContext(ctx, id)
	if err != nil {
		return AccountSessionState{}, err
	}
	return account.LocalState(), nil
}

// AccountProbe contains only bounded operational metadata, never provider text.
type AccountProbe struct {
	At        time.Time `json:"at"`
	Model     string    `json:"model"`
	Status    string    `json:"status"`
	LatencyMS int64     `json:"latency_ms"`
}

// RecordAccountProbe ignores results from a deleted or superseded session.
func RecordAccountProbe(id, version string, probe AccountProbe) error {
	unlock, err := lockStore()
	if err != nil {
		return err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return err
	}
	account := store.Accounts[id]
	if account == nil || account.SessionVersion != version {
		return nil
	}
	account.LastProbe = &probe
	if probe.Status == "success" {
		account.LastSuccessAt = probe.At
	}
	return saveStore(store)
}
