package api

import (
	"encoding/json"
	"net/http"
	"time"

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
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		State   string                `json:"state"`
		Runtime modelruntime.Snapshot `json:"runtime"`
	}{state, snapshot})
}
