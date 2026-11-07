package api

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// keyTestResult is Test model's outcome on one Key pool key, which it names
// by label only.
type keyTestResult struct {
	Label     string `json:"label"`
	Status    string `json:"status"` // "success" or "error"
	Message   string `json:"message"`
	LatencyMS int64  `json:"latency_ms"`
}

// runKeyPoolModelTest sends Test model's request through every available key
// of mc's Key pool at once and answers with one result per key. Each request
// uses its key alone, so neither another key, a Routing chain reserve nor a
// server-side refusal fallback can answer for it. The overall status is
// "success" only when every tested key replied.
func (h *Handler) runKeyPoolModelTest(w http.ResponseWriter, r *http.Request, mc config.ModelConfig) {
	protocol, modelID := providers.ExtractProtocol(mc.Model)
	if protocol != "anthropic" || mc.AuthMethod == "oauth" || mc.AuthMethod == "token" {
		modelTestError(w, http.StatusBadRequest, "Key pools serve anthropic/ models with API keys only.")
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		modelTestError(w, http.StatusInternalServerError, "Could not load configuration.")
		return
	}
	pool, err := findKeyPool(cfg, mc.KeyPoolID)
	if err != nil {
		modelTestError(w, errPoolNotFound.status, errPoolNotFound.message)
		return
	}
	keys := h.testableKeys(cfg, *pool)
	if len(keys) == 0 {
		modelTestError(w, http.StatusConflict,
			"No key of this pool is available to test. Enable a key or return one to service on the Credentials page.")
		return
	}
	results := make([]keyTestResult, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Go(func() { results[i] = testPoolKey(r.Context(), key, mc, modelID) })
	}
	wg.Wait()
	replied := 0
	for _, result := range results {
		if result.Status == "success" {
			replied++
		}
	}
	status := "success"
	if replied < len(results) {
		status = "failed"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  status,
		"message": fmt.Sprintf("%d of %d keys replied.", replied, len(results)),
		"keys":    results,
	})
}

// testableKeys are the pool's enabled keys that the running gateway does not
// report as exhausted or rejected. Without a running gateway their state is
// unknown, so every enabled key is tested.
func (h *Handler) testableKeys(cfg *config.Config, pool config.KeyPool) []config.KeyPoolKey {
	snapshot, runtime := h.gatewayRuntime()
	view := newKeyPoolView(cfg, pool, reportedKeyStates(snapshot)[pool.ID], runtime == "running")
	keys := []config.KeyPoolKey{}
	for i, key := range pool.Keys {
		switch view.Keys[i].State {
		case providers.KeyDisabled, providers.KeyExhausted, providers.KeyRejected:
		default:
			keys = append(keys, key)
		}
	}
	return keys
}

func testPoolKey(ctx context.Context, key config.KeyPoolKey, mc config.ModelConfig, modelID string) keyTestResult {
	ctx, cancel := context.WithTimeout(ctx, modelTestTimeout(mc))
	defer cancel()
	provider := providers.NewKeyPoolKeyProbe(key, &mc)
	started := time.Now()
	response, err := providers.RunModelAttempt(ctx, mc.InactivityTimeout(),
		func(attemptCtx context.Context) (*providers.LLMResponse, error) {
			return sendModelTest(attemptCtx, provider, modelID)
		})
	result := keyTestResult{
		Label:     key.Label,
		Status:    "success",
		Message:   "The key replied successfully.",
		LatencyMS: time.Since(started).Milliseconds(),
	}
	if failure := modelTestFailure(response, err); failure != "" {
		result.Status, result.Message = "error", failure
	}
	return result
}
