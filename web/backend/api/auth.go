package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/web/backend/middleware"
)

// PasswordStore is the interface for dashboard password persistence.
// Implemented by dashboardauth.Store.
type PasswordStore interface {
	IsInitialized(ctx context.Context) (bool, error)
	SetPassword(ctx context.Context, plain string) error
	VerifyPassword(ctx context.Context, plain string) (bool, error)
}

// LauncherAuthRouteOpts configures dashboard auth handlers.
type LauncherAuthRouteOpts struct {
	Sessions     *middleware.LauncherDashboardSessions
	SecureCookie func(*http.Request) bool
	// PasswordStore enables password login. It must be non-nil for auth to work.
	PasswordStore PasswordStore
	// StoreError holds the error returned when opening the password store. When
	// non-nil and PasswordStore is nil, auth endpoints fail closed with a
	// recovery message.
	StoreError error
}

type launcherAuthLoginBody struct {
	Password string `json:"password"`
}

type launcherAuthSetupBody struct {
	Password string `json:"password"`
	Confirm  string `json:"confirm"`
}

type launcherAuthStatusResponse struct {
	Authenticated bool `json:"authenticated"`
	Initialized   bool `json:"initialized"`
}

// RegisterLauncherAuthRoutes registers /api/auth/login|logout|status|setup.
func RegisterLauncherAuthRoutes(mux *http.ServeMux, opts LauncherAuthRouteOpts) {
	secure := opts.SecureCookie
	if secure == nil {
		secure = middleware.DefaultLauncherDashboardSecureCookie
	}
	h := &launcherAuthHandlers{
		sessions:     opts.Sessions,
		secureCookie: secure,
		store:        opts.PasswordStore,
		storeErr:     opts.StoreError,
		loginLimit:   newLoginRateLimiter(),
	}
	mux.Handle("POST /api/auth/login", launcherAuthGuard(http.HandlerFunc(h.handleLogin)))
	mux.Handle("POST /api/auth/logout", launcherAuthGuard(http.HandlerFunc(h.handleLogout)))
	mux.HandleFunc("GET /api/auth/status", h.handleStatus)
	mux.Handle("POST /api/auth/setup", launcherAuthGuard(http.HandlerFunc(h.handleSetup)))
}

type launcherAuthHandlers struct {
	sessions     *middleware.LauncherDashboardSessions
	passwordMu   sync.Mutex
	secureCookie func(*http.Request) bool
	store        PasswordStore
	storeErr     error // set when the store failed to open; drives recovery messages
	loginLimit   *loginRateLimiter
}

// isStoreInitialized safely queries the store.
// Returns (false, err) on store errors — callers must treat this as a 5xx, not as
// "uninitialized", to keep auth fail-closed.
func (h *launcherAuthHandlers) isStoreInitialized(ctx context.Context) (bool, error) {
	if h.store == nil {
		if h.storeErr != nil {
			return false, fmt.Errorf(
				"password store unavailable (%w); "+
					"to recover, stop the application, reset dashboard password storage, and restart",
				h.storeErr)
		}
		return false, fmt.Errorf("password store not configured")
	}
	return h.store.IsInitialized(ctx)
}

func (h *launcherAuthHandlers) handleLogin(w http.ResponseWriter, r *http.Request) {
	h.passwordMu.Lock()
	defer h.passwordMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	var body launcherAuthLoginBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid JSON"}`))
		return
	}
	ip := clientIPForLimiter(r)
	if !h.loginLimit.allow(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"too many login attempts"}`))
		return
	}
	in := body.Password

	initialized, initErr := h.isStoreInitialized(r.Context())
	if initErr != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeErrorf(w, "%v", initErr)
		return
	}
	if !initialized {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"password has not been set"}`))
		return
	}

	ok, err := h.store.VerifyPassword(r.Context(), in)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "password verification failed: %v", err)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid password"}`))
		return
	}

	if err := h.sessions.Issue(w, r, h.secureCookie); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "session creation failed")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *launcherAuthHandlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, logoutBodyMaxBytes))
	if err := dec.Decode(&struct{}{}); err != nil && err != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid JSON body"}`))
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid JSON body"}`))
		return
	}

	h.sessions.Revoke(r)
	middleware.ClearLauncherDashboardSessionCookie(w, r, h.secureCookie)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (h *launcherAuthHandlers) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	authed := h.sessions.Authenticated(r)
	initialized, initErr := h.isStoreInitialized(r.Context())
	if initErr != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeErrorf(w, "%v", initErr)
		return
	}
	resp := launcherAuthStatusResponse{
		Authenticated: authed,
		Initialized:   initialized,
	}
	enc, err := json.Marshal(resp)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "marshal response failed: %v", err)
		return
	}
	_, _ = w.Write(enc)
}

// handleSetup sets or changes the dashboard password.
//
// Rules:
//   - If the store has no password yet, anyone who can reach the setup endpoint
//     may initialize the password.
//   - If a password is already set, the caller must hold a valid session cookie.
func (h *launcherAuthHandlers) handleSetup(w http.ResponseWriter, r *http.Request) {
	h.passwordMu.Lock()
	defer h.passwordMu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	initialized, initErr := h.isStoreInitialized(r.Context())
	if initErr != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeErrorf(w, "%v", initErr)
		return
	}

	// If already initialized, require an active session (change-password flow).
	if initialized {
		authed := h.sessions.Authenticated(r)
		if !authed {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"must be authenticated to change password"}`))
			return
		}
	}

	var body launcherAuthSetupBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid JSON"}`))
		return
	}

	pw := body.Password
	if pw == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"password must not be empty"}`))
		return
	}
	if pw != body.Confirm {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"passwords do not match"}`))
		return
	}
	if len([]rune(pw)) < 8 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"password must be at least 8 characters"}`))
		return
	}

	if len([]byte(pw)) > 72 {
		w.WriteHeader(http.StatusBadRequest)
		writeErrorf(w, "password must be at most 72 bytes")
		return
	}

	if err := h.store.SetPassword(r.Context(), pw); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeErrorf(w, "failed to save password: %v", err)
		return
	}
	h.sessions.RevokeAll()
	middleware.ClearLauncherDashboardSessionCookie(w, r, h.secureCookie)

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// writeErrorf writes a JSON error response with a formatted message.
// json.Marshal is used to safely escape the message string.
func writeErrorf(w http.ResponseWriter, format string, args ...any) {
	msg, _ := json.Marshal(fmt.Sprintf(format, args...))
	_, _ = w.Write([]byte(`{"error":` + string(msg) + `}`))
}

// Auth mutations require JSON and a same-origin browser request. CLI clients
// without browser headers remain supported through the explicit JSON contract.
func launcherAuthGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if middleware.CrossSiteLauncherRequest(r) {
			w.WriteHeader(http.StatusForbidden)
			writeErrorf(w, "cross-site auth request rejected")
			return
		}
		ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
		if ct != "application/json" && !strings.HasPrefix(ct, "application/json;") {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			writeErrorf(w, "Content-Type must be application/json")
			return
		}
		next.ServeHTTP(w, r)
	})
}
