package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type routingChoice config.AgentModelConfig

type modelRouting struct {
	Defaults *routingChoice            `json:"defaults"`
	Agents   map[string]*routingChoice `json:"agents"`
}

func (h *Handler) withConfigMutation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := config.WithConfigMutationContext(
			r.Context(),
			h.configPath,
			func() error { next(w, r); return nil },
		); err != nil {
			http.Error(w, "failed to acquire configuration writer", 500)
		}
	}
}

func (h *Handler) handleGetModelRouting(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load configuration", 500)
		return
	}
	result := modelRouting{
		Defaults: &routingChoice{
			Primary:   cfg.Agents.Defaults.GetModelName(),
			Fallbacks: cfg.Agents.Defaults.ModelFallbacks,
		},
		Agents: map[string]*routingChoice{},
	}
	for _, a := range cfg.Agents.List {
		if a.Model != nil {
			choice := routingChoice(*a.Model)
			result.Agents[a.ID] = &choice
		} else {
			result.Agents[a.ID] = nil
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *Handler) handlePutModelRouting(w http.ResponseWriter, r *http.Request) {
	var input modelRouting
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid routing configuration", 400)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid routing configuration", http.StatusBadRequest)
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load configuration", 500)
		return
	}
	validate := func(route *routingChoice, inherited bool) error {
		if route == nil {
			return nil
		}
		if !inherited && route.Primary == "" {
			return fmt.Errorf("default primary is required")
		}
		refs := append([]string{route.Primary}, route.Fallbacks...)
		for _, ref := range refs {
			if ref == "" && inherited {
				continue
			}
			if !configuredRoutingReference(cfg, ref) {
				return fmt.Errorf("unknown configured model %q", ref)
			}
		}
		return nil
	}
	if err = validate(input.Defaults, false); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	agents := make(map[string]int)
	for i, a := range cfg.Agents.List {
		agents[a.ID] = i
	}
	for id, route := range input.Agents {
		index, ok := agents[id]
		if !ok {
			http.Error(w, "unknown agent "+id, 400)
			return
		}
		if err = validate(route, true); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if route == nil {
			cfg.Agents.List[index].Model = nil
		} else {
			choice := config.AgentModelConfig(*route)
			cfg.Agents.List[index].Model = &choice
		}
	}
	if input.Defaults != nil {
		cfg.Agents.Defaults.ModelName = input.Defaults.Primary
		cfg.Agents.Defaults.Model = ""
		cfg.Agents.Defaults.ModelFallbacks = input.Defaults.Fallbacks
	}
	if err = saveValidatedModels(h.configPath, cfg); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	h.handleGetModelRouting(w, r)
}

// Match the runtime's legacy names without selecting or advancing a round-robin
// entry. Explicit IDs must always identify a configured entry.
func configuredRoutingReference(cfg *config.Config, ref string) bool {
	ref = strings.TrimSpace(ref)
	for _, model := range cfg.ModelList {
		if strings.HasPrefix(ref, config.ModelIDPrefix) {
			if ref == config.ModelIDPrefix+model.ID {
				return true
			}
			continue
		}
		_, slug := providers.ExtractProtocol(model.Model)
		if ref != "" && (model.ModelName == ref || model.Model == ref || slug == ref) {
			return true
		}
	}
	return false
}

// All model writers validate saved connections while holding the same config
// guard as account deletion, preventing a new dangling binding during deletion.
func saveValidatedModels(path string, cfg *config.Config) error {
	cfg.EnsureModelIDs()
	if err := cfg.ValidateModelList(); err != nil {
		return err
	}
	if err := cfg.ValidateRoutingReferences(); err != nil {
		return err
	}
	for _, m := range cfg.ModelList {
		if m.AccountID != "" {
			if _, err := auth.GetAccount(m.AccountID); err != nil {
				// Loading a legacy OAuth model can synthesize codex-default even
				// before sign-in. Keep that binding writable, but never create one.
				previous, loadErr := config.LoadConfig(path)
				if loadErr == nil && unchangedAccountBinding(previous, m) {
					continue
				}
				return fmt.Errorf("model %q references unavailable account connection", m.ModelName)
			}
		}
	}
	return config.SaveConfig(path, cfg)
}

func unchangedAccountBinding(previous *config.Config, model config.ModelConfig) bool {
	for _, old := range previous.ModelList {
		if old.ID == model.ID && old.AccountID == model.AccountID && old.Model == model.Model &&
			old.AuthMethod == model.AuthMethod {
			return true
		}
	}
	return false
}

func rejectReferencedModel(w http.ResponseWriter, cfg *config.Config, model config.ModelConfig) bool {
	refs := cfg.ModelReferences(model)
	if len(refs) == 0 {
		return false
	}
	sort.Strings(refs)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).
		Encode(map[string]any{"error": "configured model is referenced by routing settings", "references": refs})
	return true
}

func newModelID() string { return uuid.NewString() }

// Resolve durable model URLs inside the writer guard before invoking legacy
// indexed handlers. A reordered list cannot redirect the edit or deletion.
func (h *Handler) modelByID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.LoadConfig(h.configPath)
		if err != nil {
			http.Error(w, "failed to load configuration", 500)
			return
		}
		for index, model := range cfg.ConfiguredModels() {
			if model.ID == r.PathValue("id") {
				r.SetPathValue("index", strconv.Itoa(index))
				next(w, r)
				return
			}
		}
		http.Error(w, "configured model not found", http.StatusNotFound)
	}
}
