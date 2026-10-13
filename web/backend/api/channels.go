package api

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/sipeed/picoclaw/pkg/config"
)

type channelCatalogItem struct {
	Name      string `json:"name"`
	ConfigKey string `json:"config_key"`
	Variant   string `json:"variant,omitempty"`
	Type      string `json:"type,omitempty"`
}

var channelCatalog = []channelCatalogItem{
	{Name: "telegram", ConfigKey: "telegram"},
	{Name: "discord", ConfigKey: "discord"},
	{Name: "slack", ConfigKey: "slack"},
	{Name: "feishu", ConfigKey: "feishu"},
	{Name: "dingtalk", ConfigKey: "dingtalk"},
	{Name: "line", ConfigKey: "line"},
	{Name: "qq", ConfigKey: "qq"},
	{Name: "onebot", ConfigKey: "onebot"},
	{Name: "wecom", ConfigKey: "wecom"},
	{Name: "wecom_app", ConfigKey: "wecom_app"},
	{Name: "wecom_aibot", ConfigKey: "wecom_aibot"},
	{Name: "whatsapp", ConfigKey: "whatsapp", Variant: "bridge"},
	{Name: "whatsapp_native", ConfigKey: "whatsapp_native", Variant: "native"},
	{Name: "pico", ConfigKey: "pico"},
	{Name: "maixcam", ConfigKey: "maixcam"},
	{Name: "matrix", ConfigKey: "matrix"},
	{Name: "irc", ConfigKey: "irc"},
	{Name: "email", ConfigKey: "email"},
	{Name: "weixin", ConfigKey: "weixin"},
}

// registerChannelRoutes binds read-only channel catalog endpoints to the ServeMux.
func (h *Handler) registerChannelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/channels/catalog", h.handleListChannelCatalog)
}

// handleListChannelCatalog returns the channels supported by backend.
//
//	GET /api/channels/catalog
func (h *Handler) handleListChannelCatalog(w http.ResponseWriter, r *http.Request) {
	catalog := append([]channelCatalogItem(nil), channelCatalog...)
	if cfg, err := config.LoadConfig(h.configPath); err == nil {
		var names []string
		for name, ch := range cfg.EffectiveChannelList() {
			if ch != nil && name != ch.Type {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			catalog = append(
				catalog,
				channelCatalogItem{Name: name, ConfigKey: name, Type: cfg.EffectiveChannelList()[name].Type},
			)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"channels": catalog,
	})
}
