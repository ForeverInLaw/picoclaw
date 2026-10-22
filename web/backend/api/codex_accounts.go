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
	ID            string             `json:"id"`
	Label         string             `json:"label"`
	Provider      string             `json:"provider"`
	Enabled       bool               `json:"enabled"`
	AccountID     string             `json:"account_id,omitempty"`
	Email         string             `json:"email,omitempty"`
	Status        string             `json:"status"`
	LastProbe     *auth.AccountProbe `json:"last_probe,omitempty"`
	LastSuccessAt *time.Time         `json:"last_success_at,omitempty"`
	ExpiresAt     string             `json:"expires_at,omitempty"`
	Models        []string           `json:"models"`
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
		item.LastProbe = account.LastProbe
		if !account.LastSuccessAt.IsZero() {
			item.LastSuccessAt = &account.LastSuccessAt
		}
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

func (h *Handler) handleTestAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model string `json:"model"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.Model) == "" || len(request.Model) > 200 || strings.ContainsAny(request.Model, "/ \t\n\r") {
		modelTestError(w, 400, "Enter the exact OpenAI model identifier.")
		return
	}
	h.runModelInference(w, r, config.ModelConfig{ModelName: "Account test", Model: "openai/" + request.Model, AuthMethod: "oauth", AccountID: r.PathValue("id")})
}

// Delete shares the configuration writer guard with every model binding save.
// The order is config guard -> auth store, matching model mutation validation.
func (h *Handler) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	var references []string
	err := config.WithConfigMutationContext(r.Context(), h.configPath, func() error {
		cfg, err := config.LoadConfig(h.configPath)
		if err != nil {
			return err
		}
		for _, model := range cfg.ModelList {
			if model.AccountID == r.PathValue("id") {
				references = append(references, model.ModelName)
			}
		}
		if len(references) > 0 {
			return nil
		}
		return auth.DeleteAccount(r.PathValue("id"))
	})
	if err != nil {
		if errors.Is(err, auth.ErrAccountNotFound) {
			modelTestError(w, 404, "Account connection no longer exists.")
		} else {
			modelTestError(w, 500, "Could not delete account connection.")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(references) > 0 {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "in_use", "message": "Replace this account in the listed models before deleting it.", "models": references})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}
