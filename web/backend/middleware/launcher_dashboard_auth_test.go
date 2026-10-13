package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDashboardProtectsHTTPWebSocketAndMedia(t *testing.T) {
	next := http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { t.Fatal("unauthenticated request reached dashboard") },
	)
	h := LauncherDashboardAuth(LauncherDashboardAuthConfig{Sessions: NewLauncherDashboardSessions()}, next)
	for _, route := range []string{"/api/config", "/pico/ws", "/media/file.png", "/api/media/file.png", "/assets/../api/config"} {
		t.Run(route, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", route, nil)
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDashboardBootstrapIsOneTimeAndRejectsCrossSite(t *testing.T) {
	grant, err := NewLauncherDashboardLocalAutoLogin(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h := LauncherDashboardAuth(
		LauncherDashboardAuthConfig{Sessions: NewLauncherDashboardSessions(), LocalAutoLogin: grant},
		http.NotFoundHandler(),
	)
	r := httptest.NewRequest("GET", grant.URLPath(), nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("cross-site status = %d", w.Code)
	}
	r = httptest.NewRequest("GET", grant.URLPath(), nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 303 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("bootstrap: %d %v", w.Code, w.Result().Cookies())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("reused grant status = %d", w.Code)
	}
}

func TestDashboardAllowlistStillAppliesToAuthenticatedRequests(t *testing.T) {
	sessions := NewLauncherDashboardSessions()
	auth := LauncherDashboardAuth(
		LauncherDashboardAuthConfig{Sessions: sessions},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }),
	)
	h, err := IPAllowlist([]string{"192.0.2.0/24"}, auth)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := sessions.Issue(w, httptest.NewRequest("GET", "/", nil), nil); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	for _, tc := range []struct {
		remote string
		want   int
	}{{"203.0.113.5:1234", 403}, {"127.0.0.1:1234", 200}, {"192.0.2.5:1234", 200}} {
		r := httptest.NewRequest("GET", "/api/config", nil)
		r.RemoteAddr = tc.remote
		r.AddCookie(cookie)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.remote, w.Code, tc.want)
		}
	}
}
