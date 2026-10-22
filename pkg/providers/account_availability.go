package providers

import (
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
)

type accountHealth struct {
	sessionVersion    string
	reauthorization   bool
	quota             bool
	quotaReset        time.Time
	rateLimitUntil    time.Time
	rateLimitFailures int
}

// AccountAvailability is a sanitized view of the scheduler's live connection
// state. A zero NextEligibleAt means that no reset time is known.
type AccountAvailability struct {
	AccountID      string         `json:"account_id"`
	Available      bool           `json:"available"`
	Reason         FailoverReason `json:"reason,omitempty"`
	NextEligibleAt time.Time      `json:"next_eligible_at,omitempty"`
	SessionVersion string         `json:"-"`
}

func (ct *CooldownTracker) AccountAvailability(id string) AccountAvailability {
	result := AccountAvailability{AccountID: id, Available: true}
	if id == "" {
		return result
	}
	state, err := auth.AccountState(id)
	if err != nil {
		result.Available = false
		result.Reason = FailoverAuth
		return result
	}
	result.SessionVersion = state.SessionVersion
	ct.mu.Lock()
	defer ct.mu.Unlock()
	health := ct.accounts[id]
	if health != nil && health.sessionVersion != state.SessionVersion {
		health.sessionVersion = state.SessionVersion
		health.reauthorization = false
	}
	if !state.Enabled || state.RequiresReauthorization || health != nil && health.reauthorization {
		result.Available = false
		result.Reason = FailoverAuth
		return result
	}
	now := ct.nowFunc()
	if health != nil && health.quota {
		if health.quotaReset.IsZero() || now.Before(health.quotaReset) {
			result.Available = false
			result.Reason = FailoverBilling
			result.NextEligibleAt = health.quotaReset
			return result
		}
		health.quota = false
		health.quotaReset = time.Time{}
	}
	if health != nil && now.Before(health.rateLimitUntil) {
		result.Available = false
		result.Reason = FailoverRateLimit
		result.NextEligibleAt = health.rateLimitUntil
	}
	return result
}

func (ct *CooldownTracker) recordAccountFailure(candidate FallbackCandidate, version string, failure *FailoverError) bool {
	if candidate.AccountID == "" || failure == nil {
		return false
	}
	// A model/policy-specific forbidden response permits fallback but does not
	// revoke a valid registration for every other model on that connection.
	if failure.Reason != FailoverBilling && failure.Reason != FailoverRateLimit && !(failure.Reason == FailoverAuth && failure.Status == 401) {
		return false
	}
	current, err := auth.AccountState(candidate.AccountID)
	if err != nil || current.SessionVersion != version {
		return true
	}
	ct.mu.Lock()
	defer ct.mu.Unlock()
	health := ct.accounts[candidate.AccountID]
	if health == nil {
		health = &accountHealth{}
		ct.accounts[candidate.AccountID] = health
	}
	health.sessionVersion = version
	switch failure.Reason {
	case FailoverAuth:
		health.reauthorization = true
	case FailoverBilling:
		health.quota = true
		health.quotaReset = time.Time{}
		if failure.RetryAfter > 0 {
			health.quotaReset = ct.nowFunc().Add(failure.RetryAfter)
		}
	case FailoverRateLimit:
		health.rateLimitFailures++
		delay := failure.RetryAfter
		if delay <= 0 {
			delay = calculateStandardCooldown(health.rateLimitFailures)
		}
		health.rateLimitUntil = ct.nowFunc().Add(delay)
	}
	return true
}
