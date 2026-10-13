package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/web/backend/dashboardauth"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

func dashboardTestHandler(t *testing.T, dbPath string) (http.Handler, func()) {
	t.Helper()
	store, err := dashboardauth.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sessions := middleware.NewLauncherDashboardSessions()
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{PasswordStore: store, Sessions: sessions})
	mux.HandleFunc("GET /api/private", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("private")) })
	return middleware.LauncherDashboardAuth(middleware.LauncherDashboardAuthConfig{Sessions: sessions}, mux), func() { store.Close() }
}

func TestDashboardLimitsLoginAttemptsWithoutTrustingForwardedIP(t *testing.T) {
	h, closeStore := dashboardTestHandler(t, filepath.Join(t.TempDir(), "auth.db"))
	defer closeStore()
	requireDashboardStatus(t, dashboardRequest(h, "POST", "/api/auth/setup", `{"password":"initial-password","confirm":"initial-password"}`, nil), 200)
	for i := range 11 {
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"wrong-password"}`))
		r.RemoteAddr = "192.0.2.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('a'+i)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 401
		if i == 10 {
			want = 429
		}
		requireDashboardStatus(t, w, want)
	}
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"initial-password"}`))
	r.RemoteAddr = "192.0.2.2:1234"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	requireDashboardStatus(t, w, 200)
}

func TestDashboardFailsClosedWhenPasswordStoreUnavailable(t *testing.T) {
	mux := http.NewServeMux()
	RegisterLauncherAuthRoutes(mux, LauncherAuthRouteOpts{Sessions: middleware.NewLauncherDashboardSessions(), StoreError: errors.New("unavailable")})
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/auth/status", ""},
		{"POST", "/api/auth/login", `{"password":"initial-password"}`},
		{"POST", "/api/auth/setup", `{"password":"initial-password","confirm":"initial-password"}`},
	} {
		w := dashboardRequest(mux, tc.method, tc.path, tc.body, nil)
		requireDashboardStatus(t, w, 503)
		if len(w.Result().Cookies()) != 0 {
			t.Fatal("store failure issued cookie")
		}
	}
}

func dashboardRequest(h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func requireDashboardStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d: %s", w.Code, status, w.Body.String())
	}
}

func TestDashboardPasswordSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	h, closeStore := dashboardTestHandler(t, path)
	requireDashboardStatus(t, dashboardRequest(h, "GET", "/api/private", "", nil), 401)
	requireDashboardStatus(t, dashboardRequest(h, "POST", "/api/auth/setup", `{"password":"initial-password","confirm":"initial-password"}`, nil), 200)
	closeStore()
	h, closeStore = dashboardTestHandler(t, path)
	defer closeStore()
	status := dashboardRequest(h, "GET", "/api/auth/status", "", nil)
	if status.Body.String() != `{"authenticated":false,"initialized":true}` {
		t.Fatalf("status: %s", status.Body.String())
	}
	w := dashboardRequest(h, "POST", "/api/auth/login", `{"password":"initial-password"}`, nil)
	requireDashboardStatus(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unsafe cookie: %v", cookie)
	}
	requireDashboardStatus(t, dashboardRequest(h, "GET", "/api/private", "", cookie), 200)
}

func TestDashboardLogoutAndPasswordChangeRevokeSessions(t *testing.T) {
	h, closeStore := dashboardTestHandler(t, filepath.Join(t.TempDir(), "auth.db"))
	defer closeStore()
	requireDashboardStatus(t, dashboardRequest(h, "POST", "/api/auth/setup", `{"password":"initial-password","confirm":"initial-password"}`, nil), 200)
	login := func(password string) *http.Cookie {
		w := dashboardRequest(h, "POST", "/api/auth/login", `{"password":"`+password+`"}`, nil)
		requireDashboardStatus(t, w, 200)
		return w.Result().Cookies()[0]
	}
	cookie := login("initial-password")
	requireDashboardStatus(t, dashboardRequest(h, "POST", "/api/auth/logout", `{}`, cookie), 200)
	requireDashboardStatus(t, dashboardRequest(h, "GET", "/api/private", "", cookie), 401)
	cookie = login("initial-password")
	otherCookie := login("initial-password")
	requireDashboardStatus(t, dashboardRequest(h, "POST", "/api/auth/setup", `{"password":"changed-password","confirm":"changed-password"}`, nil), 401)
	requireDashboardStatus(t, dashboardRequest(h, "POST", "/api/auth/setup", `{"password":"changed-password","confirm":"changed-password"}`, cookie), 200)
	requireDashboardStatus(t, dashboardRequest(h, "GET", "/api/private", "", otherCookie), 401)
	requireDashboardStatus(t, dashboardRequest(h, "POST", "/api/auth/login", `{"password":"initial-password"}`, nil), 401)
	requireDashboardStatus(t, dashboardRequest(h, "GET", "/api/private", "", login("changed-password")), 200)
}

func TestDashboardRejectsCrossSiteAuthRequests(t *testing.T) {
	h, closeStore := dashboardTestHandler(t, filepath.Join(t.TempDir(), "auth.db"))
	defer closeStore()
	for _, endpoint := range []string{"setup", "login", "logout"} {
		for _, header := range []string{"Origin", "Referer", "Sec-Fetch-Site"} {
			t.Run(endpoint+"/"+header, func(t *testing.T) {
				r := httptest.NewRequest("POST", "http://launcher.test/api/auth/"+endpoint, strings.NewReader(`{"password":"initial-password","confirm":"initial-password"}`))
				r.Header.Set("Content-Type", "application/json")
				value := "https://foreign.test"
				if header == "Sec-Fetch-Site" {
					value = "cross-site"
				}
				r.Header.Set(header, value)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				requireDashboardStatus(t, w, 403)
			})
		}
	}
	status := dashboardRequest(h, "GET", "/api/auth/status", "", nil)
	if strings.Contains(status.Body.String(), `"initialized":true`) {
		t.Fatal("cross-site request initialized password")
	}
}
