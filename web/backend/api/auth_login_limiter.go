package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	loginAttemptsPerIP = 10
	loginAttemptWindow = time.Minute
	maxLoginClients    = 4096
	logoutBodyMaxBytes = 4096
)

// loginRateLimiter limits POST /api/auth/login attempts per IP per minute.
type loginRateLimiter struct {
	mu         sync.Mutex
	now        func() time.Time
	byIP       map[string][]time.Time
	pruneAfter time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{
		now:  time.Now,
		byIP: make(map[string][]time.Time),
	}
}

// allow reserves a slot for this request; false means rate limit exceeded.
func (l *loginRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-loginAttemptWindow)
	if !now.Before(l.pruneAfter) {
		for client, attempts := range l.byIP {
			if len(attempts) == 0 || !attempts[len(attempts)-1].After(cutoff) {
				delete(l.byIP, client)
			}
		}
		l.pruneAfter = now.Add(loginAttemptWindow)
	}
	times := l.byIP[ip]
	if len(times) == 0 && len(l.byIP) >= maxLoginClients {
		return false
	}
	var kept []time.Time
	for _, ts := range times {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= loginAttemptsPerIP {
		l.byIP[ip] = kept
		return false
	}
	kept = append(kept, now)
	l.byIP[ip] = kept
	return true
}

func clientIPForLimiter(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}
