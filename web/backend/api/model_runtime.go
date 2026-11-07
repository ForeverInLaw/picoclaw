package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/modelruntime"
)

// gatewayState says how far the gateway's runtime snapshot describes the
// gateway now. Only a running snapshot reflects in-memory state.
type gatewayState string

const (
	gatewayRunning gatewayState = "running"
	gatewayStale   gatewayState = "stale"
	gatewayOffline gatewayState = "offline"
	gatewayUnknown gatewayState = "unknown" // no snapshot exists
)

// snapshotClockSkew is how far in the future a snapshot's UpdatedAt may lie
// and still count as fresh; anything later means the clock moved back since it
// was written, so its age says nothing.
const snapshotClockSkew = 5 * time.Second

// Running reports whether the snapshot is the running gateway's current state.
func (s gatewayState) Running() bool { return s == gatewayRunning }

// gatewayRuntime reads the gateway's runtime snapshot and its state.
func (h *Handler) gatewayRuntime() (modelruntime.Snapshot, gatewayState) {
	snapshot, err := modelruntime.Read(h.configPath)
	if err != nil {
		return snapshot, gatewayUnknown
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return snapshot, gatewayStale
	}
	health, status, healthErr := h.getGatewayHealth(cfg, time.Second)
	age := time.Since(snapshot.UpdatedAt)
	switch {
	case !snapshot.StoppedAt.IsZero() || healthErr != nil || status != http.StatusOK:
		return snapshot, gatewayOffline
	case health.Pid == snapshot.PID && age > -snapshotClockSkew && age < modelruntime.StaleAfter:
		return snapshot, gatewayRunning
	}
	return snapshot, gatewayStale
}

func (h *Handler) handleModelRuntime(w http.ResponseWriter, r *http.Request) {
	snapshot, state := h.gatewayRuntime()
	for i := range snapshot.Availability {
		item := &snapshot.Availability[i]
		if !state.Running() {
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
		if item.Authorization == "authorized" && state.Running() && !item.NextEligibleAt.IsZero() &&
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
		State   gatewayState          `json:"state"`
		Runtime modelruntime.Snapshot `json:"runtime"`
	}{state, snapshot})
}
