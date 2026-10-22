package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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
		if !account.CanUsePlan() {
			item.Status = "plan_permission_required"
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

func (h *Handler) handlePatchAccount(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		Label   *string `json:"label"`
		Enabled *bool   `json:"enabled"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil || (patch.Label == nil && patch.Enabled == nil) {
		modelTestError(w, http.StatusBadRequest, "Provide a label or enabled state.")
		return
	}
	if patch.Label != nil {
		*patch.Label = strings.TrimSpace(*patch.Label)
		if *patch.Label == "" || len(*patch.Label) > 200 {
			modelTestError(w, 400, "Use an account name between 1 and 200 characters.")
			return
		}
	}
	if err := auth.PatchAccount(r.PathValue("id"), patch.Label, patch.Enabled); err != nil {
		if errors.Is(err, auth.ErrAccountNotFound) {
			modelTestError(w, 404, "Account connection no longer exists.")
		} else {
			modelTestError(w, 500, "Could not update account connection.")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}
