package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

func namedPicoDashboard(t *testing.T, port int, enabled bool) (http.Handler, *http.Cookie, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	document := map[string]any{
		"version":    3,
		"gateway":    map[string]any{"host": "127.0.0.1", "port": port},
		"agents":     map[string]any{"defaults": map[string]any{"model_name": "fixture", "workspace": t.TempDir()}},
		"model_list": []config.ModelConfig{{ModelName: "fixture", Model: "openai/fixture", APIKey: "fixture-key"}},
		"channel_list": map[string]any{
			"webchat": map[string]any{
				"type": "pico", "enabled": enabled, "allow_from": []string{"owner"},
				"settings": map[string]any{
					"token": "named-pico-token", "ping_interval": 17, "max_connections": 9,
					"allowed_image_types": []string{"image/png"},
				},
			},
			"telegram": map[string]any{
				"type":     "telegram",
				"enabled":  false,
				"settings": map[string]any{"token": "telegram-token"},
			},
		},
	}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	_, err = config.LoadConfig(path)
	require.NoError(t, err)
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	sessions := middleware.NewLauncherDashboardSessions()
	t.Cleanup(sessions.RevokeAll)
	h := middleware.LauncherDashboardAuth(middleware.LauncherDashboardAuthConfig{Sessions: sessions}, mux)
	issued := httptest.NewRecorder()
	require.NoError(t, sessions.Issue(issued, httptest.NewRequest(http.MethodGet, "/", nil), nil))
	return h, issued.Result().Cookies()[0], path
}

func namedPicoRequest(h http.Handler, cookie *http.Cookie, method, route string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, route, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNamedPicoDashboardTokenAndMediaUseSelectedInstance(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer named-pico-token", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		_, _ = w.Write([]byte("named media"))
	}))
	defer gateway.Close()
	h, cookie, _ := namedPicoDashboard(t, mustGatewayTestPort(t, gateway.URL), true)
	token := namedPicoRequest(h, cookie, http.MethodGet, "/api/pico/token")
	require.Equal(t, http.StatusOK, token.Code)
	var response struct {
		Token   string `json:"token"`
		Enabled bool   `json:"enabled"`
	}
	require.NoError(t, json.Unmarshal(token.Body.Bytes(), &response))
	require.True(t, response.Enabled)
	require.Equal(t, "named-pico-token", response.Token)
	for _, route := range []string{"/pico/uploads?session_id=owner", "/pico/media/image?session_id=owner"} {
		media := namedPicoRequest(h, cookie, http.MethodGet, route)
		require.Equal(t, http.StatusOK, media.Code)
		require.Equal(t, "named media", media.Body.String())
	}
}

func TestNamedPicoSetupReopensSameSingletonAndPreservesSettings(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			h, cookie, path := namedPicoDashboard(t, 18790, enabled)
			r := httptest.NewRequest(http.MethodPost, "/api/pico/setup", nil)
			r.AddCookie(cookie)
			r.Header.Set("Origin", "http://example.com")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			cfg, err := config.LoadConfig(path)
			require.NoError(t, err)
			require.Len(t, cfg.ChannelList, 2)
			require.NotContains(t, cfg.ChannelList, "pico")
			instance, err := cfg.ChannelConfig("webchat")
			require.NoError(t, err)
			require.True(t, instance.Channels.Pico.Enabled)
			require.Equal(t, "named-pico-token", instance.Channels.Pico.Token)
			require.Equal(t, []string{"http://example.com"}, instance.Channels.Pico.AllowOrigins)
			require.Equal(t, 17, instance.Channels.Pico.PingInterval)
			require.Equal(t, 9, instance.Channels.Pico.MaxConnections)
			require.Equal(t, []string{"image/png"}, instance.Channels.Pico.AllowedImageTypes)
			require.Equal(t, config.FlexibleStringSlice{"owner"}, instance.Channels.Pico.AllowFrom)
			telegram, err := cfg.ChannelConfig("telegram")
			require.NoError(t, err)
			require.Equal(t, "telegram-token", telegram.Channels.Telegram.Token)
			repeated := namedPicoRequest(h, cookie, http.MethodPost, "/api/pico/setup")
			require.Equal(t, http.StatusOK, repeated.Code, repeated.Body.String())
			var response struct {
				Token   string `json:"token"`
				Changed bool   `json:"changed"`
			}
			require.NoError(t, json.Unmarshal(repeated.Body.Bytes(), &response))
			require.Equal(t, "named-pico-token", response.Token)
			require.False(t, response.Changed)
		})
	}
}

func TestNamedPicoRegeneratePrefersEnabledInstanceOverDisabledDefault(t *testing.T) {
	h, cookie, path := namedPicoDashboard(t, 18790, true)
	cfg, err := config.LoadConfig(path)
	require.NoError(t, err)
	defaultData, err := json.Marshal(config.PicoConfig{Token: "disabled-default-token"})
	require.NoError(t, err)
	cfg.ChannelList["pico"], err = config.ChannelFromLegacy("pico", defaultData)
	require.NoError(t, err)
	require.NoError(t, config.SaveConfig(path, cfg))
	result := namedPicoRequest(h, cookie, http.MethodPost, "/api/pico/token")
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
	var response struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(result.Body.Bytes(), &response))
	require.NotEmpty(t, response.Token)
	require.NotEqual(t, "named-pico-token", response.Token)
	cfg, err = config.LoadConfig(path)
	require.NoError(t, err)
	webchat, err := cfg.ChannelConfig("webchat")
	require.NoError(t, err)
	require.Equal(t, response.Token, webchat.Channels.Pico.Token)
	require.True(t, webchat.Channels.Pico.Enabled)
	require.Equal(t, 17, webchat.Channels.Pico.PingInterval)
	require.Equal(t, "disabled-default-token", cfg.Channels.Pico.Token)
	require.False(t, cfg.Channels.Pico.Enabled)
}

func TestNamedPicoGatewayStartKeepsValidSingleton(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gateway executable uses a POSIX shell")
	}
	resetGatewayTestState(t)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer endpoint.Close()
	h, cookie, path := namedPicoDashboard(t, mustGatewayTestPort(t, endpoint.URL), true)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "picoclaw")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700))
	t.Setenv(config.EnvBinary, binary)
	t.Cleanup(func() { _ = namedPicoRequest(h, cookie, http.MethodPost, "/api/gateway/stop") })
	started := namedPicoRequest(h, cookie, http.MethodPost, "/api/gateway/start")
	require.Equal(t, http.StatusOK, started.Code, started.Body.String())
	cfg, err := config.LoadConfig(path)
	require.NoError(t, err)
	require.Len(t, cfg.ChannelList, 2)
	require.NotContains(t, cfg.ChannelList, "pico")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "fully configured named Pico must not be rewritten at startup")
}
