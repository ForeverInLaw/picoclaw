package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/modelruntime"
)

func (h *Handler) handleModelRuntime(w http.ResponseWriter, r *http.Request) {
	snapshot, err := modelruntime.Read(h.configPath)
	state := "unknown"
	if err == nil {
		state = "stale"
		cfg, configErr := config.LoadConfig(h.configPath)
		if configErr == nil {
			health, status, healthErr := h.getGatewayHealth(cfg, time.Second)
			if healthErr != nil || status != http.StatusOK {
				state = "offline"
			} else if health.Pid == snapshot.PID && snapshot.StoppedAt.IsZero() && time.Since(snapshot.UpdatedAt) < modelruntime.StaleAfter {
				state = "running"
			}
		}
	}
	for i := range snapshot.Availability {
		item := &snapshot.Availability[i]
		if state != "running" {
			item.Available = false
			item.Reason = "runtime_unknown"
			item.NextEligibleAt = time.Time{}
		}
		if item.Candidate.AccountID == "" {
			continue
		}
		account, loadErr := auth.GetAccountContext(r.Context(), item.Candidate.AccountID)
		item.Authorization = "authorized"
		if loadErr != nil || account.Credential == nil {
			item.Authorization = "reauthorization_required"
		} else if !account.CanUsePlan() {
			item.Authorization = "plan_permission_required"
		} else if account.Credential.AccessToken == "" || (account.Credential.IsExpired() && account.Credential.RefreshToken == "") {
			item.Authorization = "reauthorization_required"
		}
		if loadErr == nil && !account.Enabled {
			item.Authorization = "disabled"
		}
		if item.Authorization != "authorized" {
			item.Available = false
			item.Reason = item.Authorization
			item.NextEligibleAt = time.Time{}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		State   string                `json:"state"`
		Runtime modelruntime.Snapshot `json:"runtime"`
	}{state, snapshot})
}
