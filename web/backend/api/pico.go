package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"sort"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

// Pico has one enabled instance. Prefer it over disabled instances; setup
// otherwise reuses the default name or the first configured named instance.
func selectedPicoChannel(cfg *config.Config) (*config.Channel, config.PicoConfig, error) {
	channels := cfg.EffectiveChannelList()
	var names []string
	for name, ch := range channels {
		if ch != nil && ch.Type == "pico" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	selected := ""
	for _, name := range names {
		if selected == "" || name == "pico" {
			selected = name
		}
		if channels[name].Enabled {
			selected = name
			break
		}
	}
	if selected == "" {
		if channels["pico"] != nil {
			return nil, config.PicoConfig{}, fmt.Errorf("channel name pico is already used by another type")
		}
		data, err := json.Marshal(config.DefaultConfig().Channels.Pico)
		if err != nil {
			return nil, config.PicoConfig{}, err
		}
		channel, err := config.ChannelFromLegacy("pico", data)
		return channel, config.DefaultConfig().Channels.Pico, err
	}
	var pico config.PicoConfig
	channel := channels[selected]
	err := channel.Decode(&pico)
	return channel, pico, err
}

// Patch only managed settings so the existing settings and instance policy survive.
func (h *Handler) savePicoChannel(cfg *config.Config, channel *config.Channel, updates map[string]any) error {
	settings := map[string]json.RawMessage{}
	if len(channel.Settings) > 0 {
		if err := json.Unmarshal(channel.Settings, &settings); err != nil {
			return err
		}
	}
	for field, value := range updates {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		settings[field] = data
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	channel.Settings = data
	if cfg.ChannelList == nil {
		cfg.ChannelList = config.ChannelList{}
	}
	cfg.ChannelList[channel.Name()] = channel
	return config.SaveConfig(h.configPath, cfg)
}

// registerPicoRoutes binds Pico Channel management endpoints to the ServeMux.
func (h *Handler) registerPicoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pico/token", h.handleGetPicoToken)
	mux.HandleFunc("POST /api/pico/token", h.handleRegenPicoToken)
	mux.HandleFunc("POST /api/pico/setup", h.handlePicoSetup)

	// WebSocket proxy: forward /pico/ws to gateway
	// This allows the frontend to connect via the same port as the web UI,
	// avoiding the need to expose extra ports for WebSocket communication.
	mux.HandleFunc("GET /pico/ws", h.handleWebSocketProxy())
	mux.HandleFunc("GET /pico/uploads", h.handlePicoMediaProxy())
	mux.HandleFunc("POST /pico/uploads", h.handlePicoMediaProxy())
	mux.HandleFunc("GET /pico/media/{id}", h.handlePicoMediaProxy())
	mux.HandleFunc("HEAD /pico/media/{id}", h.handlePicoMediaProxy())
	mux.HandleFunc("DELETE /pico/media/{id}", h.handlePicoMediaProxy())
}

// Cookie authentication is enforced by the launcher middleware. The gateway
// independently verifies its Pico token and upload/session ownership.
func (h *Handler) handlePicoMediaProxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.LoadConfig(h.configPath)
		var pico config.PicoConfig
		if err == nil {
			_, pico, err = selectedPicoChannel(cfg)
		}
		if err != nil || pico.Token == "" || !pico.Enabled {
			http.Error(
				w,
				"Chat media is unavailable. Start the gateway and enable Pico.",
				http.StatusServiceUnavailable,
			)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(h.gatewayProxyURL())
		director := proxy.Director
		proxy.Director = func(out *http.Request) {
			director(out)
			out.Header.Del("Cookie")
			out.Header.Set("Authorization", "Bearer "+pico.Token)
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Gateway unavailable. Start it and try the image upload again.", http.StatusBadGateway)
		}
		proxy.ServeHTTP(w, r)
	}
}

// createWsProxy creates a reverse proxy to the current gateway WebSocket endpoint.
// The gateway bind host and port are resolved from the latest configuration.
func (h *Handler) createWsProxy() *httputil.ReverseProxy {
	wsProxy := httputil.NewSingleHostReverseProxy(h.gatewayProxyURL())
	wsProxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "Gateway unavailable: "+err.Error(), http.StatusBadGateway)
	}
	return wsProxy
}

// handleWebSocketProxy wraps a reverse proxy to handle WebSocket connections.
// The reverse proxy forwards the incoming upgrade handshake as-is.
func (h *Handler) handleWebSocketProxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxy := h.createWsProxy()
		proxy.ServeHTTP(w, r)
	}
}

// handleGetPicoToken returns the current WS token and URL for the frontend.
//
//	GET /api/pico/token
func (h *Handler) handleGetPicoToken(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	_, pico, err := selectedPicoChannel(cfg)
	if err != nil {
		http.Error(w, "Failed to select Pico channel", http.StatusInternalServerError)
		return
	}

	wsURL := h.buildWsURL(r, cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":   pico.Token,
		"ws_url":  wsURL,
		"enabled": pico.Enabled,
	})
}

// handleRegenPicoToken generates a new Pico WebSocket token and saves it.
//
//	POST /api/pico/token
func (h *Handler) handleRegenPicoToken(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	channel, _, err := selectedPicoChannel(cfg)
	if err != nil {
		http.Error(w, "Failed to select Pico channel", http.StatusInternalServerError)
		return
	}

	token := generateSecureToken()

	if err := h.savePicoChannel(cfg, channel, map[string]any{"token": token}); err != nil {
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	wsURL := h.buildWsURL(r, cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":  token,
		"ws_url": wsURL,
	})
}

// ensurePicoChannel enables the Pico channel with sane defaults if it isn't
// already configured. Returns true when the config was modified.
//
// callerOrigin is the Origin header from the setup request. If non-empty and
// no origins are configured yet, it's written as the allowed origin so the
// WebSocket handshake works for whatever host the caller is on (LAN, custom
// port, etc.). Pass "" when there's no request context.
func (h *Handler) ensurePicoChannel(callerOrigin string) (bool, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return false, fmt.Errorf("failed to load config: %w", err)
	}
	channel, pico, err := selectedPicoChannel(cfg)
	if err != nil {
		return false, fmt.Errorf("failed to select Pico channel: %w", err)
	}

	changed := false
	updates := map[string]any{}

	if !pico.Enabled {
		channel.Enabled = true
		changed = true
	}

	if pico.Token == "" {
		updates["token"] = generateSecureToken()
		changed = true
	}

	// Seed origins from the request instead of hardcoding ports.
	if len(pico.AllowOrigins) == 0 && callerOrigin != "" {
		updates["allow_origins"] = []string{callerOrigin}
		changed = true
	}

	if changed {
		if err := h.savePicoChannel(cfg, channel, updates); err != nil {
			return false, fmt.Errorf("failed to save config: %w", err)
		}
	}

	return changed, nil
}

// handlePicoSetup automatically configures everything needed for the Pico Channel to work.
//
//	POST /api/pico/setup
func (h *Handler) handlePicoSetup(w http.ResponseWriter, r *http.Request) {
	changed, err := h.ensurePicoChannel(r.Header.Get("Origin"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	_, pico, err := selectedPicoChannel(cfg)
	if err != nil {
		http.Error(w, "Failed to select Pico channel", http.StatusInternalServerError)
		return
	}

	wsURL := h.buildWsURL(r, cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"token":   pico.Token,
		"ws_url":  wsURL,
		"enabled": true,
		"changed": changed,
	})
}

// generateSecureToken creates a random 32-character hex string.
func generateSecureToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to something pseudo-random if crypto/rand fails
		return fmt.Sprintf("pico_%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
