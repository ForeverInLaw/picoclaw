package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type AuthCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Provider     string    `json:"provider"`
	AuthMethod   string    `json:"auth_method"`
	Email        string    `json:"email,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
}

var storeMu sync.Mutex

type AuthStore struct {
	HostID      string                        `json:"host_id,omitempty"`
	Accounts    map[string]*AccountConnection `json:"accounts,omitempty"`
	Credentials map[string]*AuthCredential    `json:"credentials"`
}

func (c *AuthCredential) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

func (c *AuthCredential) NeedsRefresh() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(c.ExpiresAt)
}

func authFilePath() string {
	if home := os.Getenv(config.EnvHome); home != "" {
		return filepath.Join(home, "auth.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".picoclaw", "auth.json")
}

func LoadStore() (*AuthStore, error) { return LoadStoreContext(context.Background()) }

func LoadStoreContext(ctx context.Context) (*AuthStore, error) {
	unlock, err := lockStoreContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return loadStore()
}

func loadStore() (*AuthStore, error) {
	path := authFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthStore{
				Credentials: make(map[string]*AuthCredential),
				Accounts:    make(map[string]*AccountConnection),
			}, nil
		}
		return nil, err
	}

	var store AuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	if store.Credentials == nil {
		store.Credentials = make(map[string]*AuthCredential)
	}
	if store.Accounts == nil {
		store.Accounts = make(map[string]*AccountConnection)
	}
	if cred := store.Credentials["openai"]; cred != nil && (cred.AccessToken != "" || cred.RefreshToken != "") &&
		store.Accounts[config.LegacyOpenAIAccountID] == nil {
		label := cred.Email
		if label == "" {
			label = "Existing Codex account"
		}
		store.Accounts[config.LegacyOpenAIAccountID] = &AccountConnection{
			ID:         config.LegacyOpenAIAccountID,
			Label:      label,
			Provider:   "openai",
			Enabled:    true,
			Credential: cred,
			OAuth:      OpenAIOAuthConfig(),
		}
		if err := saveStore(&store); err != nil {
			return nil, err
		}
	}
	for _, account := range store.Accounts {
		if account.SessionVersion == "" {
			// Legacy records need a stable revision before their first mutation/refresh.
			account.SessionVersion = fmt.Sprintf(
				"legacy-%x",
				sha256.Sum256(
					[]byte(account.ID+"\x00"+account.Issuer+"\x00"+account.Subject+"\x00"+account.OAuth.ClientID),
				),
			)
		}
	}
	return &store, nil
}

func SaveStore(store *AuthStore) error {
	unlock, err := lockStore()
	if err != nil {
		return err
	}
	defer unlock()
	return saveStore(store)
}

func saveStore(store *AuthStore) error {
	path := authFilePath()
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

func GetCredential(provider string) (*AuthCredential, error) {
	return GetCredentialContext(context.Background(), provider)
}

func GetCredentialContext(ctx context.Context, provider string) (*AuthCredential, error) {
	store, err := LoadStoreContext(ctx)
	if err != nil {
		return nil, err
	}
	cred, ok := store.Credentials[provider]
	if !ok {
		return nil, nil
	}
	return cred, nil
}

func SetCredential(provider string, cred *AuthCredential) error {
	return SetCredentialContext(context.Background(), provider, cred)
}

func SetCredentialContext(ctx context.Context, provider string, cred *AuthCredential) error {
	unlock, err := lockStoreContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return err
	}
	store.Credentials[provider] = cred
	if provider == "openai" {
		account := store.Accounts[config.LegacyOpenAIAccountID]
		if account == nil {
			account = &AccountConnection{
				ID:       config.LegacyOpenAIAccountID,
				Label:    "Existing Codex account",
				Provider: "openai",
				Enabled:  true,
				OAuth:    OpenAIOAuthConfig(),
			}
			store.Accounts[account.ID] = account
		}
		account.SessionVersion = uuid.NewString()
		account.Credential = cred
		if cred.Email != "" && account.Label == "Existing Codex account" {
			account.Label = cred.Email
		}
	}
	return saveStore(store)
}

func DeleteCredential(provider string) error {
	unlock, err := lockStore()
	if err != nil {
		return err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return err
	}
	delete(store.Credentials, provider)
	if provider == "openai" {
		if account := store.Accounts[config.LegacyOpenAIAccountID]; account != nil {
			account.Credential = nil
			account.IDToken = ""
		}
	}
	return saveStore(store)
}

func DeleteAllCredentials() error {
	unlock, err := lockStore()
	if err != nil {
		return err
	}
	defer unlock()
	store, err := loadStore()
	if err != nil {
		return err
	}
	store.Credentials = make(map[string]*AuthCredential)
	for _, account := range store.Accounts {
		account.Credential = nil
		account.IDToken = ""
	}
	return saveStore(store)
}
