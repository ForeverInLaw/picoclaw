package middleware

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// LauncherDashboardSessions stores revocable sessions for this launcher process.
// Restart requires password login again; password hashes remain persistent.
type LauncherDashboardSessions struct {
	mu         sync.Mutex
	tokens     map[string]launcherDashboardSession
	generation uint64
}

func NewLauncherDashboardSessions() *LauncherDashboardSessions {
	return &LauncherDashboardSessions{tokens: make(map[string]launcherDashboardSession)}
}

type launcherDashboardSession struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *LauncherDashboardSessions) Authenticated(r *http.Request) bool {
	_, ok := s.sessionContext(r)
	return ok
}

func (s *LauncherDashboardSessions) sessionContext(r *http.Request) (context.Context, bool) {
	if s == nil {
		return nil, false
	}
	cookie, err := r.Cookie(LauncherDashboardCookieName)
	if err != nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.tokens[cookie.Value]
	if !ok || session.ctx.Err() != nil {
		return nil, false
	}
	return session.ctx, true
}

// Issue creates a fresh session rather than a reusable process-wide credential.
func (s *LauncherDashboardSessions) Issue(
	w http.ResponseWriter,
	r *http.Request,
	secure func(*http.Request) bool,
) error {
	return s.issue(w, r, secure, nil)
}

func (s *LauncherDashboardSessions) currentGeneration() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}

func (s *LauncherDashboardSessions) issue(
	w http.ResponseWriter,
	r *http.Request,
	secure func(*http.Request) bool,
	generation *uint64,
) error {
	if s == nil {
		return errors.New("sessions unavailable")
	}
	token, err := NewLauncherDashboardSessionCookie()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != nil && *generation != s.generation {
		return ErrInvalidLauncherDashboardGrant
	}
	for key, session := range s.tokens {
		if session.ctx.Err() != nil {
			session.cancel()
			delete(s.tokens, key)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), launcherDashboardSessionMaxAgeSec*time.Second)
	s.tokens[token] = launcherDashboardSession{ctx: ctx, cancel: cancel}
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
	if session, ok := s.tokens[cookie.Value]; ok {
		session.cancel()
	}
	delete(s.tokens, cookie.Value)
	s.mu.Unlock()
}

func (s *LauncherDashboardSessions) RevokeAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, session := range s.tokens {
		session.cancel()
	}
	clear(s.tokens)
	s.generation++
	s.mu.Unlock()
}
