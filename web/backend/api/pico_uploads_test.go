package api

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

func TestDashboardImageProxyRequiresSessionAndGatewayToken(t *testing.T) {
	requests := make(chan string, 4)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gateway-image-token" {
			t.Error("proxy omitted gateway token")
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("launcher cookie leaked upstream")
		}
		body, _ := io.ReadAll(r.Body)
		requests <- r.Method + " " + r.URL.RequestURI() + " " + string(body)
		_, _ = w.Write([]byte("image response"))
	}))
	defer gateway.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(gateway.URL, "http://"))
	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port, _ = strconv.Atoi(port)
	cfg.Channels.Pico.Token = "gateway-image-token"
	cfg.Channels.Pico.Enabled = true
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	sessions := middleware.NewLauncherDashboardSessions()
	h := middleware.LauncherDashboardAuth(middleware.LauncherDashboardAuthConfig{Sessions: sessions}, mux)
	w := httptest.NewRecorder()
	if err := sessions.Issue(w, httptest.NewRequest(http.MethodGet, "/", nil), nil); err != nil {
		t.Fatal(err)
	}
	cookie := w.Result().Cookies()[0]
	for _, route := range []string{"/pico/uploads?session_id=owner", "/pico/media/image?session_id=owner"} {
		r := httptest.NewRequest(http.MethodGet, route, nil)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s=%d", route, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/pico/uploads?session_id=owner", strings.NewReader("PNG"))
	r.AddCookie(cookie)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site upload=%d", w.Code)
	}
	r.Header.Del("Sec-Fetch-Site")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "image response" {
		t.Fatalf("proxy upload=%d %s", w.Code, w.Body.String())
	}
	if got := <-requests; got != "POST /pico/uploads?session_id=owner PNG" {
		t.Fatalf("proxy request=%q", got)
	}
	r = httptest.NewRequest(http.MethodGet, "/pico/media/image?session_id=owner", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("proxy media=%d", w.Code)
	}
	<-requests
	sessions.Revoke(r)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked media=%d", w.Code)
	}
}
