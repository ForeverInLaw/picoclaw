package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

type accountResponse struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Provider  string   `json:"provider"`
	Enabled   bool     `json:"enabled"`
	AccountID string   `json:"account_id,omitempty"`
	Email     string   `json:"email,omitempty"`
	Status    string   `json:"status"`
	ExpiresAt string   `json:"expires_at,omitempty"`
	Models    []string `json:"models"`
}

func (h *Handler) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := auth.ListAccounts()
	if err != nil {
		http.Error(w, "failed to load account connections", 500)
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load configuration", 500)
		return
	}
	result := make([]accountResponse, 0, len(accounts))
	for _, account := range accounts {
		item := accountResponse{ID: account.ID, Label: account.Label, Provider: account.Provider, Enabled: account.Enabled, Status: "reauthorization_required", Models: []string{}}
		if cred := account.Credential; cred != nil {
			item.AccountID = cred.AccountID
			item.Email = cred.Email
			if cred.AccessToken != "" && (!cred.IsExpired() || cred.RefreshToken != "") {
				item.Status = "authenticated"
			}
			if !cred.ExpiresAt.IsZero() {
				item.ExpiresAt = cred.ExpiresAt.Format(time.RFC3339)
			}
		}
		if !account.Enabled {
			item.Status = "disabled"
		}
		for _, model := range cfg.ModelList {
			if model.AccountID == account.ID {
				item.Models = append(item.Models, model.ModelName)
			}
		}
		result = append(result, item)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"accounts": result})
}
