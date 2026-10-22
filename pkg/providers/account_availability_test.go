package providers

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func savedSchedulerAccount(t *testing.T) *auth.AccountConnection {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	account := &auth.AccountConnection{
		ID: "account", Provider: "openai", Enabled: true, Scopes: auth.OpenAIPlanScope,
		Credential: &auth.AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "initial"},
	}
	require.NoError(t, auth.SaveAccount(account))
	return account
}

func TestQuotaRecoveryRequiresSuccessfulProbeAfterFailureAcrossReconnect(t *testing.T) {
	for _, knownReset := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown-reset", true: "known-reset"}[knownReset], func(t *testing.T) {
			account := savedSchedulerAccount(t)
			ct := NewCooldownTracker()
			now := time.Now()
			ct.nowFunc = func() time.Time { return now }
			failure := &FailoverError{Reason: FailoverBilling, Status: 429}
			if knownReset {
				failure.RetryAfter = time.Hour
			}
			candidate := FallbackCandidate{AccountID: account.ID}
			require.True(t, ct.recordAccountFailure(t.Context(), candidate, account.SessionVersion, failure))
			oldVersion := account.SessionVersion
			account.Credential.AccessToken = "reconnected"
			require.NoError(t, auth.SaveAccount(account))
			require.NotEqual(t, oldVersion, account.SessionVersion)
			require.False(t, ct.AccountAvailability(account.ID).Available, "reconnect alone must not clear quota")
			probe := auth.AccountProbe{Status: "success", At: now.Add(time.Second)}
			require.NoError(t, auth.RecordAccountProbe(account.ID, oldVersion, probe))
			require.False(t, ct.AccountAvailability(account.ID).Available, "stale session probe must be ignored")
			require.NoError(t, auth.RecordAccountProbe(account.ID, account.SessionVersion, probe))
			require.Equal(t, !knownReset, ct.AccountAvailability(account.ID).Available)
			now = now.Add(time.Hour)
			require.True(t, ct.AccountAvailability(account.ID).Available)
		})
	}
}

func TestSuccessfulInferenceResetsAccountRateLimitBackoffAcrossAliases(t *testing.T) {
	account := savedSchedulerAccount(t)
	ct := NewCooldownTracker()
	now := time.Now()
	ct.nowFunc = func() time.Time { return now }
	chain := NewFallbackChain(ct)
	a := FallbackCandidate{AccountID: account.ID, ConfigName: "one", Model: "shared"}
	b := FallbackCandidate{AccountID: account.ID, ConfigName: "alias", Model: "shared"}
	limited := func(context.Context, FallbackCandidate) (*LLMResponse, error) {
		return nil, &FailoverError{Reason: FailoverRateLimit, Status: 429}
	}
	_, err := chain.ExecuteCandidate(t.Context(), []FallbackCandidate{a}, limited)
	require.Error(t, err)
	require.Equal(t, time.Minute, ct.AccountAvailability(account.ID).NextEligibleAt.Sub(now))
	now = now.Add(time.Minute)
	_, err = chain.ExecuteCandidate(t.Context(), []FallbackCandidate{b}, limited)
	require.Error(t, err)
	require.Equal(t, 5*time.Minute, ct.AccountAvailability(account.ID).NextEligibleAt.Sub(now))
	now = now.Add(5 * time.Minute)
	_, err = chain.ExecuteCandidate(
		t.Context(),
		[]FallbackCandidate{b},
		func(context.Context, FallbackCandidate) (*LLMResponse, error) {
			return &LLMResponse{Content: "success"}, nil
		},
	)
	require.NoError(t, err)
	_, err = chain.ExecuteCandidate(t.Context(), []FallbackCandidate{a}, limited)
	require.Error(t, err)
	require.Equal(t, time.Minute, ct.AccountAvailability(account.ID).NextEligibleAt.Sub(now))
	until := ct.AccountAvailability(account.ID).NextEligibleAt
	ct.recordAccountSuccess(t.Context(), account.ID, "superseded")
	require.Equal(t, 1, ct.accounts[account.ID].rateLimitFailures)
	ct.recordAccountSuccess(t.Context(), account.ID, account.SessionVersion)
	require.Equal(
		t,
		until,
		ct.AccountAvailability(account.ID).NextEligibleAt,
		"concurrent Retry-After must survive success",
	)
}

func TestRecoveryExcludesRawIncompatibleCandidateAndWaitsForService(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "cancel"}[cancelWait], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				chain := NewFallbackChain(NewCooldownTracker())
				candidates := []FallbackCandidate{{Model: "primary"}, {Model: "incompatible"}}
				var calls []string
				waits := 0
				result, err := chain.ExecuteRecoveryCandidate(
					ctx,
					candidates,
					func(_ context.Context, c FallbackCandidate) (*LLMResponse, error) {
						calls = append(calls, c.Model)
						if len(calls) > 3 {
							t.Fatal("recovery spun without waiting")
						}
						if c.Model == "incompatible" {
							return nil, &IncompatibleCandidateError{Reason: "image unsupported"}
						}
						if len(calls) == 1 {
							return nil, &FailoverError{Reason: FailoverTimeout, Status: 503}
						}
						return &LLMResponse{Content: "recovered"}, nil
					},
					func(delay time.Duration, _ error) {
						waits++
						require.Positive(t, delay)
						if cancelWait {
							cancel()
						}
					},
				)
				require.Equal(t, 1, waits)
				if cancelWait {
					require.ErrorIs(t, err, context.Canceled)
					require.Equal(t, []string{"primary", "incompatible"}, calls)
				} else {
					require.NoError(t, err)
					require.Equal(t, "recovered", result.Response.Content)
					require.Equal(t, []string{"primary", "incompatible", "primary"}, calls)
				}
			})
		})
	}
}
