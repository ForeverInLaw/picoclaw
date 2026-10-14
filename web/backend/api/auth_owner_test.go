package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/web/backend/dashboardauth"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

const dashboardTestSetupToken = "owner-setup-token"

func TestDashboardInitialSetupRequiresOwnerProof(t *testing.T) {
	for _, remote := range []string{"198.51.100.2:4000", "127.0.0.1:4000", "[::1]:4000", "172.18.0.1:4000"} {
		t.Run(remote, func(t *testing.T) {
			h, closeStore := dashboardTestHandler(t, filepath.Join(t.TempDir(), "auth.db"))
			defer closeStore()
			for _, token := range []string{"", "wrong-token"} {
				r := httptest.NewRequest(
					"POST",
					"/api/auth/setup",
					strings.NewReader(`{"password":"initial-password","confirm":"initial-password"}`),
				)
				r.RemoteAddr = remote
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Forwarded-For", "127.0.0.1")
				r.Header.Set("Forwarded", "for=127.0.0.1")
				r.Header.Set("X-Picoclaw-Setup-Token", token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
				status := dashboardRequest(h, "GET", "/api/auth/status", "", nil)
				require.JSONEq(t, `{"authenticated":false,"initialized":false}`, status.Body.String())
				require.NotContains(t, status.Body.String(), dashboardTestSetupToken)
			}
			valid := dashboardRequest(
				h,
				"POST",
				"/api/auth/setup",
				`{"password":"initial-password","confirm":"initial-password"}`,
				nil,
			)
			require.Equal(t, http.StatusOK, valid.Code, valid.Body.String())
			reused := dashboardRequest(
				h,
				"POST",
				"/api/auth/setup",
				`{"password":"attacker-password","confirm":"attacker-password"}`,
				nil,
			)
			require.Equal(t, http.StatusUnauthorized, reused.Code)
		})
	}
}

func TestDashboardSetupFailsClosedWithoutConfiguredOwnerToken(t *testing.T) {
	store, err := dashboardauth.Open(filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)
	defer store.Close()
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(
		mux,
		LauncherAuthRouteOpts{PasswordStore: store, Sessions: middleware.NewLauncherDashboardSessions()},
	)
	w := dashboardRequest(
		mux,
		"POST",
		"/api/auth/setup",
		`{"password":"initial-password","confirm":"initial-password"}`,
		nil,
	)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	initialized, err := store.IsInitialized(t.Context())
	require.NoError(t, err)
	require.False(t, initialized)
}
