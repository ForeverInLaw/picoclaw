package auth

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

var ErrAccountNotFound = errors.New("account connection not found")

// AccountConnection owns one renewable session. OAuth client metadata and tokens
// stay in the protected auth store; dashboard responses use a separate view.
type AccountConnection struct {
	ID         string              `json:"id"`
	Label      string              `json:"label"`
	Provider   string              `json:"provider"`
	Enabled    bool                `json:"enabled"`
	Subject    string              `json:"subject,omitempty"`
	Issuer     string              `json:"issuer,omitempty"`
	HostID     string              `json:"host_id,omitempty"`
	IDToken    string              `json:"id_token,omitempty"`
	Resource   string              `json:"resource,omitempty"`
	Scopes     string              `json:"scopes,omitempty"`
	OAuth      OAuthProviderConfig `json:"oauth"`
	Credential *AuthCredential     `json:"credential,omitempty"`
}

func GetAccount(id string) (*AccountConnection, error) {
	store, err := LoadStore()
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
	store.Accounts[account.ID] = account
	if account.ID == config.LegacyOpenAIAccountID {
		store.Credentials["openai"] = account.Credential
	}
	return saveStore(store)
}

func UpdateAccount(id, label string, enabled bool) error {
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
	account.Label = label
	account.Enabled = enabled
	return saveStore(store)
}

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
	key := authFilePath() + "\x00" + id
	value, _ := accountRefreshLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	refreshFile, err := lockAuthFile(fmt.Sprintf("%s.refresh-%x.lock", authFilePath(), sha256.Sum256([]byte(id))))
	if err != nil {
		return nil, err
	}
	defer refreshFile.Close()
	account, err := GetAccount(id)
	if err != nil {
		return nil, err
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
	refreshed, err := RefreshAccessToken(cred, account.OAuth)
	if err != nil {
		return nil, err
	}
	unlock, err := lockStore()
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
	if !current.Enabled {
		return nil, fmt.Errorf("account connection %q is disabled", id)
	}
	if current.Credential == nil || *current.Credential != *cred {
		return nil, errors.New("account authorization changed during refresh")
	}
	current.Credential = refreshed
	if id == config.LegacyOpenAIAccountID {
		store.Credentials["openai"] = refreshed
	}
	if err := saveStore(store); err != nil {
		return nil, err
	}
	return refreshed, nil
}
