package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
)

// registerModelRoutes binds model list management endpoints to the ServeMux.
func (h *Handler) registerModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/models", h.handleListModels)
	mux.HandleFunc("GET /api/models/routing", h.handleGetModelRouting)
	mux.HandleFunc("PUT /api/models/routing", h.withConfigMutation(h.handlePutModelRouting))
	mux.HandleFunc("POST /api/models", h.withConfigMutation(h.handleAddModel))
	mux.HandleFunc("POST /api/models/default", h.withConfigMutation(h.handleSetDefaultModel))
	mux.HandleFunc("POST /api/models/{index}/test", h.handleTestSavedModel)
	mux.HandleFunc("POST /api/models/test-inline", h.handleTestInlineModel)
	mux.HandleFunc("POST /api/models/validate", h.handleValidateModel)
	mux.HandleFunc("PUT /api/models/by-id/{id}", h.withConfigMutation(h.modelByID(h.handleUpdateModel)))
	mux.HandleFunc("DELETE /api/models/by-id/{id}", h.withConfigMutation(h.modelByID(h.handleDeleteModel)))
	mux.HandleFunc("PUT /api/models/{index}", h.withConfigMutation(h.handleUpdateModel))
	mux.HandleFunc("DELETE /api/models/{index}", h.withConfigMutation(h.handleDeleteModel))
}

// modelResponse is the JSON structure returned for each model in the list.
// All ModelConfig fields are included so the frontend can display and edit them.
type modelResponse struct {
	ID           string                    `json:"id"`
	Fallbacks    []string                  `json:"fallbacks"`
	Index        int                       `json:"index"`
	ModelName    string                    `json:"model_name"`
	Model        string                    `json:"model"`
	APIBase      string                    `json:"api_base,omitempty"`
	APIKey       string                    `json:"api_key"`
	Proxy        string                    `json:"proxy,omitempty"`
	AuthMethod   string                    `json:"auth_method,omitempty"`
	AccountID    string                    `json:"account_id,omitempty"`
	Capabilities *config.ModelCapabilities `json:"capabilities,omitempty"`
	// Advanced fields
	ConnectMode    string         `json:"connect_mode,omitempty"`
	Workspace      string         `json:"workspace,omitempty"`
	RPM            int            `json:"rpm,omitempty"`
	MaxTokensField string         `json:"max_tokens_field,omitempty"`
	RequestTimeout int            `json:"request_timeout,omitempty"`
	ThinkingLevel  string         `json:"thinking_level,omitempty"`
	ExtraBody      map[string]any `json:"extra_body,omitempty"`
	// Meta
	Configured bool `json:"configured"`
	IsDefault  bool `json:"is_default"`
}

// handleListModels returns all model_list entries with masked API keys.
//
//	GET /api/models
func (h *Handler) handleListModels(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	cfg.ModelList = cfg.ConfiguredModels()
	defaultModel := cfg.Agents.Defaults.GetModelName()
	configured := make([]bool, len(cfg.ModelList))

	var wg sync.WaitGroup
	wg.Add(len(cfg.ModelList))
	for i, m := range cfg.ModelList {
		go func(i int, m config.ModelConfig) {
			defer wg.Done()
			if keys := config.MergeAPIKeys(m.APIKey, m.APIKeys); len(keys) > 0 {
				m.APIKey = keys[0]
			}
			configured[i] = isModelConfigured(m)
		}(i, m)
	}
	wg.Wait()

	models := make([]modelResponse, 0, len(cfg.ModelList))
	for i, m := range cfg.ModelList {
		if keys := config.MergeAPIKeys(m.APIKey, m.APIKeys); len(keys) > 0 {
			m.APIKey = keys[0]
		}
		models = append(models, modelResponse{
			ID:             m.ID,
			Fallbacks:      m.Fallbacks,
			Index:          i,
			ModelName:      m.ModelName,
			Model:          m.Model,
			APIBase:        m.APIBase,
			APIKey:         maskAPIKey(m.APIKey),
			Proxy:          m.Proxy,
			AuthMethod:     m.AuthMethod,
			AccountID:      m.AccountID,
			Capabilities:   m.Capabilities,
			ConnectMode:    m.ConnectMode,
			Workspace:      m.Workspace,
			RPM:            m.RPM,
			MaxTokensField: m.MaxTokensField,
			RequestTimeout: m.RequestTimeout,
			ThinkingLevel:  m.ThinkingLevel,
			ExtraBody:      m.ExtraBody,
			Configured:     configured[i],
			IsDefault:      m.ModelName == defaultModel || config.ModelIDPrefix+m.ID == defaultModel,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"models":        models,
		"total":         len(models),
		"default_model": defaultModel,
	})
}

// handleAddModel appends a new model configuration entry.
//
//	POST /api/models
func (h *Handler) handleAddModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var mc config.ModelConfig
	if err = json.Unmarshal(body, &mc); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err = mc.Validate(); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	cfg.ModelList = cfg.ConfiguredModels()
	mc.ID = newModelID()
	cfg.ModelList = append(cfg.ModelList, mc)

	if err := saveValidatedModels(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"index":  len(cfg.ModelList) - 1,
		"id":     mc.ID,
	})
}

// handleUpdateModel replaces a model configuration entry at the given index.
// Omitted or masked keys are retained only for the same credential destination.
//
//	PUT /api/models/{index}
func (h *Handler) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var mc config.ModelConfig
	if err = json.Unmarshal(body, &mc); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if err = mc.Validate(); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	cfg.ModelList = cfg.ConfiguredModels()
	if idx < 0 || idx >= len(cfg.ModelList) {
		http.Error(w, fmt.Sprintf("Index %d out of range (0-%d)", idx, len(cfg.ModelList)-1), http.StatusNotFound)
		return
	}

	// Apply only supplied settings to the selected entry. Preserve advanced fields
	// that this editor does not expose, while validating any credential destination edit.
	saved := cfg.ModelList[idx]
	var supplied struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &supplied)
	if supplied.ID != "" && supplied.ID != saved.ID {
		http.Error(w, "configured model ID cannot change", 400)
		return
	}
	mc = saved
	mc.APIKey = ""
	mc.APIKeys = nil
	if err := json.Unmarshal(body, &mc); err != nil {
		http.Error(w, "Invalid model configuration", http.StatusBadRequest)
		return
	}
	saved.ModelName = mc.ModelName // Renaming the selected saved entry keeps its credential destination.
	if err = retainModelCredential(&mc, saved); err != nil {
		modelTestError(w, http.StatusBadRequest, err.Error())
		return
	}
	if mc.AccountID == "" && mc.AuthMethod == cfg.ModelList[idx].AuthMethod && mc.Model == cfg.ModelList[idx].Model {
		mc.AccountID = cfg.ModelList[idx].AccountID
	}

	mc.ID = cfg.ModelList[idx].ID
	cfg.ModelList[idx] = mc

	if err := saveValidatedModels(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDeleteModel removes a model configuration entry at the given index.
//
//	DELETE /api/models/{index}
func (h *Handler) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	cfg.ModelList = cfg.ConfiguredModels()
	if idx < 0 || idx >= len(cfg.ModelList) {
		http.Error(w, fmt.Sprintf("Index %d out of range (0-%d)", idx, len(cfg.ModelList)-1), http.StatusNotFound)
		return
	}

	if rejectReferencedModel(w, cfg, cfg.ModelList[idx]) {
		return
	}
	cfg.ModelList = append(cfg.ModelList[:idx], cfg.ModelList[idx+1:]...)

	if err := saveValidatedModels(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleSetDefaultModel sets the default model for all agents.
//
//	POST /api/models/default
func (h *Handler) handleSetDefaultModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req struct {
		ModelName string `json:"model_name"`
	}
	if err = json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if req.ModelName == "" {
		http.Error(w, "model_name is required", http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	// Verify the model_name exists in model_list
	found := false
	for _, m := range cfg.ModelList {
		if m.ModelName == req.ModelName || config.ModelIDPrefix+m.ID == req.ModelName {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, fmt.Sprintf("Model %q not found in model_list", req.ModelName), http.StatusNotFound)
		return
	}

	cfg.Agents.Defaults.ModelName = req.ModelName

	if err := saveValidatedModels(h.configPath, cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":        "ok",
		"default_model": req.ModelName,
	})
}

// maskAPIKey returns a masked version of an API key for safe display.
// Keys longer than 8 chars show prefix + last 4 chars: "sk-****abcd"
// Shorter keys are fully masked as "****".
// Empty keys return empty string.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	// Show first 3 chars and last 4 chars
	return key[:3] + "****" + key[len(key)-4:]
}
