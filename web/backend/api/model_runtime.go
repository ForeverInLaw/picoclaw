package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/modelruntime"
)

// gatewayRuntime reads the gateway's runtime snapshot and whether it describes
// the gateway running now: "running", "stale", "offline", or "unknown" when
// no snapshot exists. Only a running snapshot reflects in-memory state.
func (h *Handler) gatewayRuntime() (modelruntime.Snapshot, string) {
	snapshot, err := modelruntime.Read(h.configPath)
	if err != nil {
		return snapshot, "unknown"
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return snapshot, "stale"
	}
	health, status, healthErr := h.getGatewayHealth(cfg, time.Second)
	switch {
	case !snapshot.StoppedAt.IsZero() || healthErr != nil || status != http.StatusOK:
		return snapshot, "offline"
	case health.Pid == snapshot.PID && time.Since(snapshot.UpdatedAt) < modelruntime.StaleAfter:
		return snapshot, "running"
	}
	return snapshot, "stale"
}

func (h *Handler) handleModelRuntime(w http.ResponseWriter, r *http.Request) {
	snapshot, state := h.gatewayRuntime()
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
		item.Authorization = string(auth.AccountReauthorizationRequired)
		if loadErr == nil {
			item.Authorization = string(account.LocalState().Authorization)
		}
		if item.Reason == "auth" && item.Authorization == "authorized" {
			item.Authorization = "reauthorization_required"
		}
		if item.Authorization == "authorized" && state == "running" && !item.NextEligibleAt.IsZero() &&
			!time.Now().Before(item.NextEligibleAt) {
			item.Available = true
			item.Reason = ""
			item.NextEligibleAt = time.Time{}
		}
		if item.Authorization != "authorized" {
			item.Available = false
			item.Reason = item.Authorization
			item.NextEligibleAt = time.Time{}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		State   string                `json:"state"`
		Runtime modelruntime.Snapshot `json:"runtime"`
	}{state, snapshot})
}
