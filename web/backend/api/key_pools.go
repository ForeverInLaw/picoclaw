package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/keyspend"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/modelruntime"
	"github.com/sipeed/picoclaw/pkg/providers"
	anthropicprovider "github.com/sipeed/picoclaw/pkg/providers/anthropic"
)

// anthropicAPIBase is where Key pool keys are validated; tests point it at a
// fake Anthropic.
var anthropicAPIBase = "https://api.anthropic.com"

const anthropicKeyCheckTimeout = 15 * time.Second

// keyStateUnknown is reported for enabled keys while no running gateway
// describes them: their state lives only in the gateway's memory.
const keyStateUnknown providers.KeyState = "unknown"

func (h *Handler) registerKeyPoolRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/key-pools", h.handleListKeyPools)
	mux.HandleFunc("POST /api/key-pools", h.handleCreateKeyPool)
	mux.HandleFunc("PATCH /api/key-pools/{pool}", h.handlePatchKeyPool)
	mux.HandleFunc("DELETE /api/key-pools/{pool}", h.handleDeleteKeyPool)
	mux.HandleFunc("GET /api/key-pools/{pool}/models", h.handleListPoolModels)
	mux.HandleFunc("POST /api/key-pools/{pool}/keys", h.handleAddPoolKey)
	mux.HandleFunc("PATCH /api/key-pools/{pool}/keys/{key}", h.handlePatchPoolKey)
	mux.HandleFunc("DELETE /api/key-pools/{pool}/keys/{key}", h.handleDeletePoolKey)
	mux.HandleFunc("POST /api/key-pools/{pool}/keys/{key}/return", h.handleReturnPoolKey)
}

type keyPoolView struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Models []string         `json:"models"`
	Keys   []keyPoolKeyView `json:"keys"`
}

// keyPoolKeyView is one key with its secret masked, its availability as the
// running gateway reports it and its spend in the current budget period.
type keyPoolKeyView struct {
	ID                string                     `json:"id"`
	Label             string                     `json:"label"`
	Enabled           bool                       `json:"enabled"`
	APIKey            string                     `json:"api_key"`
	State             providers.KeyState         `json:"state"`
	Until             time.Time                  `json:"until,omitzero"`
	RateLimitedModels []providers.ModelRateLimit `json:"rate_limited_models,omitempty"`
	MonthlyBudgetUSD  float64                    `json:"monthly_budget_usd,omitempty"`
	ResetDay          int                        `json:"reset_day"`
	Spend             keySpendView               `json:"spend"`
}

// keySpendView is a key's usage since the start of its budget period.
type keySpendView struct {
	Since time.Time `json:"since"`
	keyspend.Totals
	CacheHitRate float64 `json:"cache_hit_rate"`
}

func newKeyPoolKeyView(
	poolID string,
	key config.KeyPoolKey,
	state providers.KeyAvailability,
	spent keyspend.Ledger,
) keyPoolKeyView {
	since := keyspend.PeriodStart(key.ResetDay, time.Now())
	totals := spent.Since(poolID, key.ID, since)
	return keyPoolKeyView{
		ID: key.ID, Label: key.Label, Enabled: key.Enabled, APIKey: maskAPIKey(key.APIKey),
		State: state.State, Until: state.Until, RateLimitedModels: state.RateLimitedModels,
		MonthlyBudgetUSD: key.MonthlyBudgetUSD, ResetDay: max(key.ResetDay, 1),
		Spend: keySpendView{Since: since, Totals: totals, CacheHitRate: totals.CacheHitRate()},
	}
}

// keyState is a key's availability as the dashboard knows it: disabled, unknown
// while no gateway runs, available once a saved return to service is newer
// than the gateway's report, else what the running gateway reports, where a
// key it has not reported has had no trouble.
func keyState(
	key config.KeyPoolKey,
	reported map[string]providers.KeyAvailability,
	running bool,
) providers.KeyAvailability {
	state, ok := reported[key.ID]
	switch {
	case !key.Enabled:
		return providers.KeyAvailability{KeyID: key.ID, State: providers.KeyDisabled}
	case !running:
		return providers.KeyAvailability{KeyID: key.ID, State: keyStateUnknown}
	case !ok:
		return providers.KeyAvailability{KeyID: key.ID, State: providers.KeyAvailable}
	case !key.ReturnedAt.IsZero() && !key.ReturnedAt.Equal(state.ReturnedAt):
		// The gateway applies a saved return before the key next serves or is reported.
		return providers.KeyAvailability{KeyID: key.ID, State: providers.KeyAvailable}
	}
	return state
}

// newKeyPoolView shows pool with the key states a running gateway reported and
// the keys' spend.
func newKeyPoolView(
	cfg *config.Config,
	pool config.KeyPool,
	reported map[string]providers.KeyAvailability,
	running bool,
	spent keyspend.Ledger,
) keyPoolView {
	view := keyPoolView{ID: pool.ID, Name: pool.Name, Models: keyPoolModels(cfg, pool.ID), Keys: []keyPoolKeyView{}}
	for _, key := range pool.Keys {
		view.Keys = append(view.Keys, newKeyPoolKeyView(pool.ID, key, keyState(key, reported, running), spent))
	}
	return view
}

// keyPoolModels names the configured models that use the pool.
func keyPoolModels(cfg *config.Config, poolID string) []string {
	models := []string{}
	for _, model := range cfg.ModelList {
		if model.KeyPoolID == poolID {
			models = append(models, model.ModelName)
		}
	}
	return models
}

// reportedKeyStates indexes the gateway's reported key states by pool and key ID.
func reportedKeyStates(snapshot modelruntime.Snapshot) map[string]map[string]providers.KeyAvailability {
	states := map[string]map[string]providers.KeyAvailability{}
	for _, pool := range snapshot.KeyPools {
		states[pool.ID] = map[string]providers.KeyAvailability{}
		for _, key := range pool.Keys {
			states[pool.ID][key.KeyID] = key
		}
	}
	return states
}

func (h *Handler) handleListKeyPools(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		modelTestError(w, http.StatusInternalServerError, "Could not load configuration.")
		return
	}
	snapshot, runtime := h.gatewayRuntime()
	reported := reportedKeyStates(snapshot)
	spent, err := keyspend.Store{}.Read()
	if err != nil {
		logger.WarnCF("key-pools", "Key pool spend unreadable", map[string]any{"error": err.Error()})
	}
	pools := make([]keyPoolView, 0, len(cfg.KeyPools))
	for _, pool := range cfg.KeyPools {
		pools = append(pools, newKeyPoolView(cfg, pool, reported[pool.ID], runtime == "running", spent))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"runtime": runtime, "pools": pools})
}

func (h *Handler) handleCreateKeyPool(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if !decodeJSONObject(r, &request) || !validDisplayName(&request.Name) {
		modelTestError(w, http.StatusBadRequest, "Use a pool name between 1 and 200 characters.")
		return
	}
	pool := config.KeyPool{ID: uuid.NewString(), Name: request.Name, Keys: []config.KeyPoolKey{}}
	h.respondKeyPoolEdit(w, r, http.StatusCreated, func(cfg *config.Config) (any, error) {
		cfg.KeyPools = append(cfg.KeyPools, pool)
		return newKeyPoolView(cfg, pool, nil, false, keyspend.Ledger{}), nil
	})
}

func (h *Handler) handlePatchKeyPool(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if !decodeJSONObject(r, &request) || !validDisplayName(&request.Name) {
		modelTestError(w, http.StatusBadRequest, "Use a pool name between 1 and 200 characters.")
		return
	}
	h.respondKeyPoolEdit(w, r, http.StatusOK, func(cfg *config.Config) (any, error) {
		pool, err := findKeyPool(cfg, r.PathValue("pool"))
		if err != nil {
			return nil, err
		}
		pool.Name = request.Name
		return statusSuccess, nil
	})
}

func (h *Handler) handleDeleteKeyPool(w http.ResponseWriter, r *http.Request) {
	h.respondKeyPoolEdit(w, r, http.StatusOK, func(cfg *config.Config) (any, error) {
		id := r.PathValue("pool")
		if _, err := findKeyPool(cfg, id); err != nil {
			return nil, err
		}
		if models := keyPoolModels(cfg, id); len(models) > 0 {
			return nil, &keyPoolError{
				status:  http.StatusConflict,
				message: "Choose other credentials for the listed models before deleting this pool.",
				models:  models,
			}
		}
		cfg.KeyPools = slices.DeleteFunc(cfg.KeyPools, func(pool config.KeyPool) bool { return pool.ID == id })
		return statusSuccess, nil
	})
}

// handleAddPoolKey saves a key only after Anthropic lists models for it,
// which costs no credit and rejects a wrong or revoked key.
func (h *Handler) handleAddPoolKey(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Label  string `json:"label"`
		APIKey string `json:"api_key"`
	}
	if !decodeJSONObject(r, &request) || !validDisplayName(&request.Label) {
		modelTestError(w, http.StatusBadRequest, "Use a key label between 1 and 200 characters.")
		return
	}
	request.APIKey = strings.TrimSpace(request.APIKey)
	if request.APIKey == "" || modelCredentialPlaceholder(request.APIKey) {
		modelTestError(w, http.StatusBadRequest, "Paste the complete Anthropic API key.")
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		modelTestError(w, http.StatusInternalServerError, "Could not load configuration.")
		return
	}
	if status, message := keyPoolEndpoint(cfg, r.PathValue("pool")).checkKey(r.Context(), request.APIKey); status != 0 {
		modelTestError(w, status, message)
		return
	}
	key := config.KeyPoolKey{ID: uuid.NewString(), Label: request.Label, Enabled: true, APIKey: request.APIKey}
	h.respondKeyPoolEdit(w, r, http.StatusCreated, func(cfg *config.Config) (any, error) {
		pool, err := findKeyPool(cfg, r.PathValue("pool"))
		if err != nil {
			return nil, err
		}
		pool.Keys = append(pool.Keys, key)
		return newKeyPoolKeyView(pool.ID, key, keyState(key, nil, false), keyspend.Ledger{}), nil
	})
}

// anthropicEndpoint is the API base and proxy that a Key pool's requests
// outside answers, key checks and model lists, travel.
type anthropicEndpoint struct{ apiBase, proxy string }

// keyPoolEndpoint is the network path of the first configured model on the
// pool, so a pool reached through a gateway or proxy is checked the same way;
// without one it is Anthropic's API through the environment's proxy.
func keyPoolEndpoint(cfg *config.Config, poolID string) anthropicEndpoint {
	for _, model := range cfg.ModelList {
		if model.KeyPoolID == poolID {
			return anthropicEndpoint{apiBase: cmp.Or(model.APIBase, anthropicAPIBase), proxy: model.Proxy}
		}
	}
	return anthropicEndpoint{apiBase: anthropicAPIBase}
}

// checkKey lists models with apiKey. It returns the HTTP status and message
// for a key that cannot be saved, or 0 for a valid key.
func (e anthropicEndpoint) checkKey(ctx context.Context, apiKey string) (int, string) {
	_, err := e.listModels(ctx, apiKey)
	var apiErr *anthropic.Error
	switch {
	case err == nil:
		return 0, ""
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized:
		return http.StatusBadRequest, "Anthropic rejected this key. Check that it is complete and has not been revoked."
	case errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden:
		return http.StatusBadRequest, "Anthropic does not allow this key to list models."
	}
	return http.StatusBadGateway, "Could not check the key with Anthropic. Try again."
}

// listModels lists the models Anthropic offers to apiKey, which costs no credit.
func (e anthropicEndpoint) listModels(ctx context.Context, apiKey string) ([]anthropicprovider.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, anthropicKeyCheckTimeout)
	defer cancel()
	return anthropicprovider.NewProviderWithAPIKey(apiKey, e.apiBase, e.proxy, anthropicKeyCheckTimeout).
		ListModels(ctx)
}

// checkNewKeyPoolSecrets checks every Key pool secret of next that current
// does not hold for the same pool and key. It returns the HTTP status and
// message for the first one Anthropic refuses, or 0 when all can be saved.
func checkNewKeyPoolSecrets(ctx context.Context, next, current *config.Config) (int, string) {
	stored := storedKeyPoolSecrets(current)
	for _, pool := range next.KeyPools {
		endpoint := keyPoolEndpoint(next, pool.ID)
		for _, key := range pool.Keys {
			if key.APIKey == "" || key.APIKey == stored[[2]string{pool.ID, key.ID}] {
				continue
			}
			if status, message := endpoint.checkKey(ctx, key.APIKey); status != 0 {
				return status, fmt.Sprintf("Key pool %q, key %q: %s", pool.Name, key.Label, message)
			}
		}
	}
	return 0, ""
}

// handleListPoolModels lists the models Anthropic offers to the pool through
// its first enabled key that can list them, so the Models page offers models
// released after this build.
func (h *Handler) handleListPoolModels(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		modelTestError(w, http.StatusInternalServerError, "Could not load configuration.")
		return
	}
	pool, err := findKeyPool(cfg, r.PathValue("pool"))
	if err != nil {
		modelTestError(w, errPoolNotFound.status, errPoolNotFound.message)
		return
	}
	keys := pool.EnabledKeys()
	if len(keys) == 0 {
		modelTestError(w, http.StatusConflict, "Enable a key in this pool to list its models.")
		return
	}
	endpoint := keyPoolEndpoint(cfg, pool.ID)
	for _, key := range keys {
		if models, err := endpoint.listModels(r.Context(), key.APIKey); err == nil {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"models": models})
			return
		}
	}
	modelTestError(w, http.StatusBadGateway, "Anthropic did not list models for any enabled key of this pool.")
}

func (h *Handler) handlePatchPoolKey(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		Label            *string  `json:"label"`
		Enabled          *bool    `json:"enabled"`
		MonthlyBudgetUSD *float64 `json:"monthly_budget_usd"` // 0 clears the budget
		ResetDay         *int     `json:"reset_day"`
	}
	if !decodeJSONObject(r, &patch) ||
		(patch.Label == nil && patch.Enabled == nil && patch.MonthlyBudgetUSD == nil && patch.ResetDay == nil) {
		modelTestError(w, http.StatusBadRequest, "Provide a label, enabled state, monthly budget or reset day.")
		return
	}
	if patch.Label != nil && !validDisplayName(patch.Label) {
		modelTestError(w, http.StatusBadRequest, "Use a key label between 1 and 200 characters.")
		return
	}
	requested := config.KeyPoolKey{ResetDay: 1}
	if patch.MonthlyBudgetUSD != nil {
		requested.MonthlyBudgetUSD = *patch.MonthlyBudgetUSD
	}
	if patch.ResetDay != nil {
		requested.ResetDay = *patch.ResetDay
	}
	if !requested.ValidBudget() || requested.ResetDay < 1 {
		modelTestError(w, http.StatusBadRequest, "Use a monthly budget of $0 to $1,000,000 and a reset day of 1 to 31.")
		return
	}
	h.editPoolKey(w, r, func(key *config.KeyPoolKey) {
		if patch.Label != nil {
			key.Label = *patch.Label
		}
		if patch.Enabled != nil {
			key.Enabled = *patch.Enabled
		}
		if patch.MonthlyBudgetUSD != nil {
			key.MonthlyBudgetUSD = *patch.MonthlyBudgetUSD
		}
		if patch.ResetDay != nil {
			key.ResetDay = *patch.ResetDay
		}
	})
}

// handleReturnPoolKey saves the moment an administrator returned the key to
// service; the gateway clears its exhausted, rejected or rate-limited state.
func (h *Handler) handleReturnPoolKey(w http.ResponseWriter, r *http.Request) {
	h.editPoolKey(w, r, func(key *config.KeyPoolKey) { key.ReturnedAt = time.Now().UTC() })
}

func (h *Handler) handleDeletePoolKey(w http.ResponseWriter, r *http.Request) {
	h.respondKeyPoolEdit(w, r, http.StatusOK, func(cfg *config.Config) (any, error) {
		pool, err := findKeyPool(cfg, r.PathValue("pool"))
		if err != nil {
			return nil, err
		}
		id := r.PathValue("key")
		if !slices.ContainsFunc(pool.Keys, func(key config.KeyPoolKey) bool { return key.ID == id }) {
			return nil, errKeyNotFound
		}
		pool.Keys = slices.DeleteFunc(pool.Keys, func(key config.KeyPoolKey) bool { return key.ID == id })
		return statusSuccess, nil
	})
}

func (h *Handler) editPoolKey(w http.ResponseWriter, r *http.Request, edit func(*config.KeyPoolKey)) {
	h.respondKeyPoolEdit(w, r, http.StatusOK, func(cfg *config.Config) (any, error) {
		pool, err := findKeyPool(cfg, r.PathValue("pool"))
		if err != nil {
			return nil, err
		}
		for i := range pool.Keys {
			if pool.Keys[i].ID == r.PathValue("key") {
				edit(&pool.Keys[i])
				return statusSuccess, nil
			}
		}
		return nil, errKeyNotFound
	})
}

// keyPoolError is an edit refused with an HTTP status, a message and, for a
// pool still in use, the models that reference it.
type keyPoolError struct {
	status  int
	message string
	models  []string
}

func (e *keyPoolError) Error() string { return e.message }

var (
	errPoolNotFound = &keyPoolError{status: http.StatusNotFound, message: "Key pool no longer exists."}
	errKeyNotFound  = &keyPoolError{status: http.StatusNotFound, message: "Key no longer exists in this pool."}
	statusSuccess   = map[string]string{"status": "success"}
)

func findKeyPool(cfg *config.Config, id string) (*config.KeyPool, error) {
	for i := range cfg.KeyPools {
		if cfg.KeyPools[i].ID == id {
			return &cfg.KeyPools[i], nil
		}
	}
	return nil, errPoolNotFound
}

// respondKeyPoolEdit applies edit to the saved configuration under the
// configuration writer guard, saves it through the protected-credential path
// and answers with edit's result.
func (h *Handler) respondKeyPoolEdit(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	edit func(*config.Config) (any, error),
) {
	var result any
	err := config.WithConfigMutationContext(r.Context(), h.configPath, func() error {
		cfg, err := config.LoadConfig(h.configPath)
		if err != nil {
			return err
		}
		if result, err = edit(cfg); err != nil {
			return err
		}
		return saveValidatedModels(h.configPath, cfg)
	})
	var refused *keyPoolError
	switch {
	case errors.As(err, &refused) && len(refused.models) > 0:
		writeJSON(
			w,
			refused.status,
			map[string]any{"status": "in_use", "message": refused.message, "models": refused.models},
		)
	case errors.As(err, &refused):
		modelTestError(w, refused.status, refused.message)
	case err != nil:
		modelTestError(w, http.StatusInternalServerError, "Could not save Key pools.")
	default:
		writeJSON(w, status, result)
	}
}

// validDisplayName trims a pool name or key label in place and reports
// whether it has 1 to 200 characters.
func validDisplayName(name *string) bool {
	*name = strings.TrimSpace(*name)
	return *name != "" && len([]rune(*name)) <= 200
}

// decodeJSONObject decodes exactly one JSON object with known fields only.
func decodeJSONObject(r *http.Request, into any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
