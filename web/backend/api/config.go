package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

// registerConfigRoutes binds configuration management endpoints to the ServeMux.
func (h *Handler) registerConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", h.handleGetConfig)
	mux.HandleFunc("PUT /api/config", h.handleUpdateConfig)
	mux.HandleFunc("PATCH /api/config", h.handlePatchConfig)
}

// handleGetConfig returns the complete system configuration.
//
//	GET /api/config
func (h *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if cfg.Tools.Skills.Registries.ClawHub.AuthToken != "" {
		cfg.Tools.Skills.Registries.ClawHub.AuthToken = registryCredentialMask
	}
	if cfg.Tools.Skills.Github.Token != "" {
		cfg.Tools.Skills.Github.Token = registryCredentialMask
	}
	if err := json.NewEncoder(w).Encode(cfg); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

// handleUpdateConfig updates the complete system configuration.
//
//	PUT /api/config
func (h *Handler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var cfg config.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if execAllowRemoteOmitted(body) {
		cfg.Tools.Exec.AllowRemote = config.DefaultConfig().Tools.Exec.AllowRemote
	}
	current, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "Failed to load stored registry credentials", http.StatusInternalServerError)
		return
	}
	preserveRegistryCredentials(&cfg, current, body)

	if errs := validateConfig(&cfg); len(errs) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "validation_error",
			"errors": errs,
		})
		return
	}

	if err := config.SaveConfig(h.configPath, &cfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func execAllowRemoteOmitted(body []byte) bool {
	var raw struct {
		Tools *struct {
			Exec *struct {
				AllowRemote *bool `json:"allow_remote"`
			} `json:"exec"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	return raw.Tools == nil || raw.Tools.Exec == nil || raw.Tools.Exec.AllowRemote == nil
}

// handlePatchConfig partially updates the system configuration using JSON Merge Patch (RFC 7396).
// Only the fields present in the request body will be updated; all other fields remain unchanged.
//
//	PATCH /api/config
func (h *Handler) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	patchBody, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Validate the patch is valid JSON
	var patch map[string]any
	if err = json.Unmarshal(patchBody, &patch); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Load existing config and marshal to a map for merging
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	existing, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, "Failed to serialize current config", http.StatusInternalServerError)
		return
	}

	var base map[string]any
	if err = json.Unmarshal(existing, &base); err != nil {
		http.Error(w, "Failed to parse current config", http.StatusInternalServerError)
		return
	}

	// Recursively merge patch into base
	// Existing channel forms submit flat adapter fields; translate them at this
	// boundary while the HTTP configuration document remains canonical v3.
	if legacy, ok := patch["channels"].(map[string]any); ok {
		converted := map[string]any{}
		for name, value := range legacy {
			if value == nil {
				converted[name] = nil
				continue
			}
			fields, ok := value.(map[string]any)
			if !ok {
				http.Error(w, "Channel patch must be an object", http.StatusBadRequest)
				return
			}
			entry := map[string]any{}
			settings := map[string]any{}
			for key, value := range fields {
				if _, common := config.BaseFieldNames[key]; common {
					entry[key] = value
				} else {
					settings[key] = value
				}
			}
			if cfg.EffectiveChannelList()[name] == nil {
				entry["type"] = name
			}
			entry["settings"] = settings
			converted[name] = entry
		}
		delete(patch, "channels")
		if _, mixed := patch["channel_list"]; mixed {
			http.Error(w, "Use only one channel patch schema", http.StatusBadRequest)
			return
		}
		patch["channel_list"] = converted
	}
	mergeMap(base, patch)

	// Convert merged map back to Config struct
	merged, err := json.Marshal(base)
	if err != nil {
		http.Error(w, "Failed to serialize merged config", http.StatusInternalServerError)
		return
	}

	var newCfg config.Config
	if err := json.Unmarshal(merged, &newCfg); err != nil {
		http.Error(w, fmt.Sprintf("Merged config is invalid: %v", err), http.StatusBadRequest)
		return
	}

	preserveRegistryCredentials(&newCfg, cfg, patchBody)
	if errs := validateConfig(&newCfg); len(errs) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "validation_error",
			"errors": errs,
		})
		return
	}

	if err := config.SaveConfig(h.configPath, &newCfg); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

const registryCredentialMask = "****"

// Public config uses a sentinel for registry credentials. Unchanged/missing
// values retain the stored secret only while its configured endpoint matches.
// An explicit value replaces it; an explicit empty value removes it.
func preserveRegistryCredentials(next, current *config.Config, body []byte) {
	var fields struct {
		Tools struct {
			Skills struct {
				Registries struct {
					ClawHub struct {
						AuthToken json.RawMessage `json:"auth_token"`
					} `json:"clawhub"`
				} `json:"registries"`
				Github struct {
					Token json.RawMessage `json:"token"`
				} `json:"github"`
			} `json:"skills"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(body, &fields)
	preserve := func(raw json.RawMessage) bool {
		var value string
		_ = json.Unmarshal(raw, &value)
		return len(raw) == 0 || value == registryCredentialMask
	}
	if preserve(fields.Tools.Skills.Registries.ClawHub.AuthToken) {
		next.Tools.Skills.Registries.ClawHub.AuthToken = ""
		base := func(value string) string {
			if value == "" {
				value = "https://clawhub.ai"
			}
			return strings.TrimRight(value, "/")
		}
		if base(next.Tools.Skills.Registries.ClawHub.BaseURL) == base(current.Tools.Skills.Registries.ClawHub.BaseURL) {
			next.Tools.Skills.Registries.ClawHub.AuthToken = current.Tools.Skills.Registries.ClawHub.AuthToken
		}
	}
	if preserve(fields.Tools.Skills.Github.Token) {
		next.Tools.Skills.Github.Token = current.Tools.Skills.Github.Token
	}
}

// validateConfig checks the config for common errors before saving.
// Returns a list of human-readable error strings; empty means valid.
func validateConfig(cfg *config.Config) []string {
	var errs []string
	if err := cfg.ValidateChannels(); err != nil {
		errs = append(errs, err.Error())
		return errs
	}

	// Validate model_list entries
	if err := cfg.ValidateModelList(); err != nil {
		errs = append(errs, err.Error())
	}

	// Gateway port range
	if cfg.Gateway.Port != 0 && (cfg.Gateway.Port < 1 || cfg.Gateway.Port > 65535) {
		errs = append(errs, fmt.Sprintf("gateway.port %d is out of valid range (1-65535)", cfg.Gateway.Port))
	}

	for name, ch := range cfg.EffectiveChannelList() {
		if !ch.Enabled {
			continue
		}
		instance, err := cfg.ChannelConfig(name)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		missingToken := ch.Type == "pico" && instance.Channels.Pico.Token == "" ||
			ch.Type == "telegram" && instance.Channels.Telegram.Token == "" ||
			ch.Type == "discord" && instance.Channels.Discord.Token == ""
		if missingToken {
			errs = append(errs, fmt.Sprintf("channel_list.%s.settings.token is required when enabled", name))
		}
	}

	if cfg.Tools.Exec.Enabled {
		if cfg.Tools.Exec.EnableDenyPatterns {
			errs = append(
				errs,
				validateRegexPatterns("tools.exec.custom_deny_patterns", cfg.Tools.Exec.CustomDenyPatterns)...)
		}
		errs = append(
			errs,
			validateRegexPatterns("tools.exec.custom_allow_patterns", cfg.Tools.Exec.CustomAllowPatterns)...)
	}

	return errs
}

func validateRegexPatterns(field string, patterns []string) []string {
	var errs []string
	for index, pattern := range patterns {
		if _, err := regexp.Compile(pattern); err != nil {
			errs = append(errs, fmt.Sprintf("%s[%d] is not a valid regular expression: %v", field, index, err))
		}
	}
	return errs
}

// mergeMap recursively merges src into dst (JSON Merge Patch semantics).
// - If a key in src has a null value, it is deleted from dst.
// - If both dst and src have a nested object for the same key, merge recursively.
// - Otherwise the value from src overwrites dst.
func mergeMap(dst, src map[string]any) {
	for key, srcVal := range src {
		if srcVal == nil {
			delete(dst, key)
			continue
		}
		srcMap, srcIsMap := srcVal.(map[string]any)
		dstMap, dstIsMap := dst[key].(map[string]any)
		if srcIsMap && dstIsMap {
			mergeMap(dstMap, srcMap)
		} else {
			dst[key] = srcVal
		}
	}
}
