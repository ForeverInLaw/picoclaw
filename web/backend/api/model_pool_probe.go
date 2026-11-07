package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
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
	_, modelID := providers.ExtractProtocol(mc.Model)
	if !mc.IsAnthropicAPIKeyModel() {
		writeAPIError(w, http.StatusBadRequest, "Key pools serve anthropic/ models with API keys only.")
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "Could not load configuration.")
		return
	}
	pool, err := findKeyPool(cfg, mc.KeyPoolID)
	if err != nil {
		writeAPIError(w, errPoolNotFound.status, errPoolNotFound.message)
		return
	}
	keys := h.testableKeys(*pool, modelID)
	if len(keys) == 0 {
		writeAPIError(w, http.StatusConflict,
			"No key of this pool is available to test. Enable a key or return one to service on the Credentials page.")
		return
	}
	results := make([]keyTestResult, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Go(func() { results[i] = testPoolKey(r.Context(), pool, key, mc, modelID) })
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
// report as exhausted, rejected or rate limited for modelID. Without a running
// gateway their state is unknown, so every enabled key is tested.
func (h *Handler) testableKeys(pool config.KeyPool, modelID string) []config.KeyPoolKey {
	snapshot, runtime := h.gatewayRuntime()
	reported := reportedKeyStates(snapshot)[pool.ID]
	keys := []config.KeyPoolKey{}
	for _, key := range pool.Keys {
		state := keyState(key, reported, runtime == "running")
		switch {
		case state.State == providers.KeyDisabled, state.State == providers.KeyExhausted,
			state.State == providers.KeyRejected:
		case slices.ContainsFunc(state.RateLimitedModels, func(limit providers.ModelRateLimit) bool {
			return limit.Model == modelID && limit.Until.After(time.Now())
		}):
		default:
			keys = append(keys, key)
		}
	}
	return keys
}

func testPoolKey(
	ctx context.Context,
	pool *config.KeyPool,
	key config.KeyPoolKey,
	mc config.ModelConfig,
	modelID string,
) keyTestResult {
	ctx, cancel := context.WithTimeout(ctx, modelTestTimeout(mc))
	defer cancel()
	provider := providers.NewKeyPoolKeyProbe(pool, key, &mc)
	started := time.Now()
	response, err := providers.RunModelAttempt(ctx, mc.InactivityTimeout(),
		func(attemptCtx context.Context) (*providers.LLMResponse, error) {
			// Models that always think, such as Opus 5.5, think at the lowest
			// effort with room left for the reply.
			return sendModelTest(attemptCtx, provider, modelID,
				map[string]any{"max_tokens": 1024, "thinking_level": "off"})
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
