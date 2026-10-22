package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

func dashboardFlowOwner(r *http.Request) string {
	cookie, err := r.Cookie(middleware.LauncherDashboardCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(cookie.Value))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) startCodexAccountLogin(w http.ResponseWriter, r *http.Request, accountID, label string) {
	owner := dashboardFlowOwner(r)
	if owner == "" {
		http.Error(w, "Sign in to the dashboard before authorizing an account", 401)
		return
	}
	cfg := auth.OpenAIRegistrationConfig()
	var previous *auth.AccountConnection
	if accountID != "" {
		var err error
		previous, err = auth.GetAccount(accountID)
		if err != nil {
			http.Error(w, "Selected account connection was not found", 404)
			return
		}
		if previous.Resource == "" {
			http.Error(
				w,
				"This legacy connection must be renewed with the CLI; add a new ChatGPT connection to use browser sign-in",
				400,
			)
			return
		}
		cfg = previous.OAuth
	}
	pkce, err := oauthGeneratePKCE()
	if err != nil {
		http.Error(w, "Could not prepare sign-in", 500)
		return
	}
	state, err := oauthGenerateState()
	if err != nil {
		http.Error(w, "Could not prepare sign-in", 500)
		return
	}
	nonce, err := auth.GenerateState()
	if err != nil {
		http.Error(w, "Could not prepare sign-in", 500)
		return
	}
	hostID, err := auth.InstanceHostIDContext(r.Context())
	if err != nil {
		http.Error(w, "Could not prepare host identity", 500)
		return
	}
	values := url.Values{
		"client_id":             {cfg.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {auth.OpenAIRegistrationRedirect},
		"scope":                 {cfg.Scopes},
		"resource":              {cfg.Resource},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {pkce.CodeChallenge},
		"ext_agent_host_id":     {hostID},
	}
	if previous == nil {
		values.Set("agent_name_hint", "Picoclaw")
	} else {
		if !previous.CanUsePlan() {
			values.Set("prompt", "consent")
		}
		if previous.IDToken != "" {
			values.Set("id_token_hint", previous.IDToken)
		}
		if previous.Credential != nil && previous.Credential.Email != "" {
			values.Set("login_hint", previous.Credential.Email)
		}
	}
	now := oauthNow()
	flow := &oauthFlow{
		ID:              newOAuthFlowID(),
		Provider:        oauthProviderOpenAI,
		Method:          oauthMethodBrowser,
		Status:          oauthFlowPending,
		CreatedAt:       now,
		UpdatedAt:       now,
		ExpiresAt:       now.Add(oauthBrowserFlowTTL),
		OAuthState:      state,
		Nonce:           nonce,
		CodeVerifier:    pkce.CodeVerifier,
		RedirectURI:     auth.OpenAIRegistrationRedirect,
		Manual:          true,
		Owner:           owner,
		OAuthConfig:     cfg,
		PreviousAccount: previous,
		HostID:          hostID,
		Label:           strings.TrimSpace(label),
	}
	h.storeOAuthFlow(flow)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).
		Encode(map[string]any{"status": "ok", "provider": oauthProviderOpenAI, "method": oauthMethodBrowser, "flow_id": flow.ID, "auth_url": cfg.Issuer + "/api/accounts/authorize?" + values.Encode(), "manual_callback": true, "expires_at": flow.ExpiresAt.Format(time.RFC3339)})
}

func parseManualCallback(raw string, flow *oauthFlow) (code, clientID string, denied bool, err error) {
	invalid := func() (string, string, bool, error) {
		return "", "", false, errors.New("Paste the complete callback URL from this sign-in attempt")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Scheme != "http" ||
		parsed.Host != "127.0.0.1:1455" ||
		parsed.Path != "/auth/callback" ||
		parsed.RawPath != "" {
		return invalid()
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return invalid()
	}
	for _, name := range []string{"code", "state", "client_id", "error", "error_description", "scope"} {
		if len(values[name]) > 1 {
			return invalid()
		}
	}
	if values.Get("state") == "" || values.Get("state") != flow.OAuthState {
		return invalid()
	}
	if values.Get("error") != "" {
		return "", "", true, nil
	}
	code = values.Get("code")
	clientID = values.Get("client_id")
	if flow.PreviousAccount != nil {
		expected := flow.OAuthConfig.ClientID
		if clientID != "" && clientID != expected {
			return invalid()
		}
		clientID = expected
	}
	if code == "" || clientID == "" || clientID == auth.OpenAIRegistrationEntryClient || len(clientID) > 1024 ||
		strings.ContainsAny(clientID, " \t\r\n") {
		return invalid()
	}
	return code, clientID, false, nil
}

func (h *Handler) handleCompleteCodexAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CallbackURL string `json:"callback_url"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "Invalid callback request", 400)
		return
	}
	owner := dashboardFlowOwner(r)
	h.oauthMu.Lock()
	h.gcOAuthFlowsLocked(oauthNow())
	flow := h.oauthFlows[r.PathValue("id")]
	if flow == nil || !flow.Manual || owner == "" || flow.Owner != owner {
		h.oauthMu.Unlock()
		http.Error(w, "Sign-in attempt was not found in this dashboard session", 404)
		return
	}
	if flow.Status != oauthFlowPending || flow.Consumed {
		h.oauthMu.Unlock()
		http.Error(w, "Sign-in attempt has expired or already been used; start a fresh sign-in", 409)
		return
	}
	code, clientID, denied, err := parseManualCallback(request.CallbackURL, flow)
	if err != nil {
		h.oauthMu.Unlock()
		http.Error(w, err.Error(), 400)
		return
	}
	flow.Consumed = true
	consumed := *flow
	// Remove pending secrets before network I/O; only this request retains them.
	flow.CodeVerifier = ""
	flow.Nonce = ""
	flow.OAuthState = ""
	flow.PreviousAccount = nil
	delete(h.oauthState, consumed.OAuthState)
	h.oauthMu.Unlock()
	if denied {
		h.setOAuthFlowError(consumed.ID, "OpenAI sign-in was declined")
		http.Error(w, "OpenAI sign-in was declined", 400)
		return
	}
	consumed.OAuthConfig.ClientID = clientID
	account, err := auth.ExchangeRegisteredAccount(
		r.Context(),
		consumed.OAuthConfig,
		code,
		consumed.CodeVerifier,
		consumed.RedirectURI,
		consumed.Nonce,
		consumed.HostID,
		consumed.PreviousAccount,
	)
	if err != nil {
		h.setOAuthFlowError(consumed.ID, "OpenAI account verification failed; start a fresh sign-in")
		http.Error(w, "OpenAI account verification failed; start a fresh sign-in", 400)
		return
	}
	if consumed.PreviousAccount == nil && consumed.Label != "" {
		account.Label = consumed.Label
	}
	saved, err := auth.SaveRegisteredAccountContext(r.Context(), account, consumed.PreviousAccount)
	if err != nil {
		h.setOAuthFlowError(consumed.ID, "Could not save the selected account registration")
		http.Error(w, "Could not save the selected account registration", 409)
		return
	}
	h.oauthMu.Lock()
	if current := h.oauthFlows[consumed.ID]; current != nil {
		current.AccountID = saved.ID
	}
	h.oauthMu.Unlock()
	h.setOAuthFlowSuccess(consumed.ID)
	updated, _ := h.getOAuthFlow(consumed.ID)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(flowToResponse(updated))
}
