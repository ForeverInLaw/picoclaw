package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const maxHubSearchResults = 200

type hubSearchItem struct {
	skills.SearchResult
	Installed bool `json:"installed"`
}
type hubSearchResponse struct {
	Results    []hubSearchItem `json:"results"`
	HasMore    bool            `json:"has_more"`
	NextOffset int             `json:"next_offset,omitempty"`
}

func (h *Handler) registerSkillHubRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/skills/hub/search", h.handleSkillHubSearch)
	mux.HandleFunc("GET /api/skills/hub/{registry}/{slug}", h.handleSkillHubDetail)
	mux.HandleFunc("POST /api/skills/hub/install", h.handleSkillHubInstall)
}

func (h *Handler) skillHubConfig(w http.ResponseWriter, tool string) (*config.Config, *skills.RegistryManager) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "Unable to load Skill Hub configuration.", http.StatusInternalServerError)
		return nil, nil
	}
	if !cfg.Tools.IsToolEnabled("skills") || !cfg.Tools.IsToolEnabled(tool) {
		http.Error(w, "Skill Hub access is disabled by tool settings.", http.StatusForbidden)
		return nil, nil
	}
	return cfg, skills.NewRegistryManagerFromConfig(
		skills.RegistryConfig{
			MaxConcurrentSearches: cfg.Tools.Skills.MaxConcurrentSearches,
			ClawHub:               skills.ClawHubConfig(cfg.Tools.Skills.Registries.ClawHub),
		},
	)
}

func (h *Handler) handleSkillHubSearch(w http.ResponseWriter, r *http.Request) {
	cfg, manager := h.skillHubConfig(w, "find_skills")
	if cfg == nil {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 512 {
		http.Error(w, "Search query is too long.", http.StatusBadRequest)
		return
	}
	limit, offset := 20, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			http.Error(w, "limit must be between 1 and 50", http.StatusBadRequest)
			return
		}
		limit = value
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value >= maxHubSearchResults {
			http.Error(w, "offset must be between 0 and 199", http.StatusBadRequest)
			return
		}
		offset = value
	}
	response := hubSearchResponse{Results: []hubSearchItem{}}
	if query != "" {
		results, err := manager.SearchAll(r.Context(), query, min(offset+limit+1, maxHubSearchResults))
		if err != nil {
			http.Error(
				w,
				"Registry search failed. Check registry availability, credentials and settings.",
				http.StatusBadGateway,
			)
			return
		}
		start, end := min(offset, len(results)), min(offset+limit, len(results))
		for _, result := range results[start:end] {
			installed := false
			if utils.ValidateSkillIdentifier(result.Slug) == nil {
				_, err := os.Lstat(filepath.Join(cfg.WorkspacePath(), "skills", result.Slug))
				installed = err == nil
			}
			response.Results = append(response.Results, hubSearchItem{SearchResult: result, Installed: installed})
		}
		response.HasMore = len(results) > end && end < maxHubSearchResults
		if response.HasMore {
			response.NextOffset = end
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (h *Handler) handleSkillHubDetail(w http.ResponseWriter, r *http.Request) {
	cfg, manager := h.skillHubConfig(w, "find_skills")
	if cfg == nil {
		return
	}
	slug := r.PathValue("slug")
	if utils.ValidateSkillIdentifier(slug) != nil {
		http.Error(w, "Invalid skill identifier.", http.StatusBadRequest)
		return
	}
	registry := manager.GetRegistry(r.PathValue("registry"))
	if registry == nil {
		http.Error(w, "Registry is not enabled.", http.StatusBadRequest)
		return
	}
	meta, err := registry.GetSkillMeta(r.Context(), slug)
	if err != nil {
		http.Error(
			w,
			"Registry details could not be loaded. Check registry availability and credentials.",
			http.StatusBadGateway,
		)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}

func (h *Handler) handleSkillHubInstall(w http.ResponseWriter, r *http.Request) {
	cfg, manager := h.skillHubConfig(w, "install_skill")
	if cfg == nil {
		return
	}
	var request struct {
		Slug     string `json:"slug"`
		Registry string `json:"registry"`
		Version  string `json:"version"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	if json.NewDecoder(r.Body).Decode(&request) != nil || utils.ValidateSkillIdentifier(request.Slug) != nil ||
		utils.ValidateSkillIdentifier(request.Registry) != nil ||
		len(request.Version) > 128 {
		http.Error(w, "Invalid install request.", http.StatusBadRequest)
		return
	}
	registry := manager.GetRegistry(request.Registry)
	if registry == nil {
		http.Error(w, "Registry is not enabled.", http.StatusBadRequest)
		return
	}
	result, err := skills.InstallRegistrySkill(
		r.Context(),
		cfg.WorkspacePath(),
		registry,
		request.Slug,
		request.Version,
		false,
	)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, skills.ErrSkillAlreadyInstalled), errors.Is(err, skills.ErrSkillActivation):
			status = http.StatusConflict
		case errors.Is(err, skills.ErrSkillUnsafeDirectory), errors.Is(err, skills.ErrSkillMalware):
			status = http.StatusForbidden
		case errors.Is(err, skills.ErrSkillDownload), errors.Is(err, skills.ErrSkillInvalidArchive):
			status = http.StatusBadGateway
		}
		message := err.Error()
		if errors.Is(err, skills.ErrSkillAlreadyInstalled) {
			message = "Skill is already installed. Remove it first to install another version."
		}
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).
		Encode(map[string]any{"status": "ok", "slug": request.Slug, "registry": registry.Name(), "version": result.Version, "is_suspicious": result.IsSuspicious})
}
