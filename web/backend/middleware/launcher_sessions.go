package middleware

import (
	"errors"
	"net/http"
	"sync"
	"time"
)

// LauncherDashboardSessions stores revocable sessions for this launcher process.
// Restart requires password login again; password hashes remain persistent.
type LauncherDashboardSessions struct {
	mu     sync.Mutex
	tokens map[string]time.Time
}

func NewLauncherDashboardSessions() *LauncherDashboardSessions {
	return &LauncherDashboardSessions{tokens: make(map[string]time.Time)}
}

func (s *LauncherDashboardSessions) Authenticated(r *http.Request) bool {
	if s == nil {
		return false
	}
	cookie, err := r.Cookie(LauncherDashboardCookieName)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.tokens[cookie.Value]
	return ok && time.Now().Before(expires)
}

// Issue creates a fresh session rather than a reusable process-wide credential.
func (s *LauncherDashboardSessions) Issue(w http.ResponseWriter, r *http.Request, secure func(*http.Request) bool) error {
	if s == nil {
		return errors.New("sessions unavailable")
	}
	token, err := NewLauncherDashboardSessionCookie()
	if err != nil {
		return err
	}
	now := time.Now()
	s.mu.Lock()
	for key, expires := range s.tokens {
		if !now.Before(expires) {
			delete(s.tokens, key)
		}
	}
	s.tokens[token] = now.Add(launcherDashboardSessionMaxAgeSec * time.Second)
	s.mu.Unlock()
	SetLauncherDashboardSessionCookie(w, r, token, secure)
	return nil
}

func (s *LauncherDashboardSessions) Revoke(r *http.Request) {
	if s == nil {
		return
	}
	cookie, err := r.Cookie(LauncherDashboardCookieName)
	if err != nil {
		return
	}
	s.mu.Lock()
	delete(s.tokens, cookie.Value)
	s.mu.Unlock()
}

func (s *LauncherDashboardSessions) RevokeAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.tokens)
	s.mu.Unlock()
}
