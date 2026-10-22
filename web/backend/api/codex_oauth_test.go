package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

type openAIRegistrationFixture struct {
	t            *testing.T
	mu           sync.Mutex
	key          *rsa.PrivateKey
	nonce        string
	subject      string
	client       string
	scope        string
	overrides    map[string]any
	badSignature bool
	toolFlow     bool
	model        string
	exchanges    int
	inference    int
}

func fakeOpenAIRegistration(t *testing.T) *openAIRegistrationFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &openAIRegistrationFixture{t: t, key: key, subject: "subject-one", client: "oaiapp_one", scope: "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct", model: "gpt-6.1-sol"}
	original := http.DefaultTransport
	http.DefaultTransport = accountRecordingTransport{handler: fixture.serve}
	t.Cleanup(func() { http.DefaultTransport = original })
	return fixture
}

func (f *openAIRegistrationFixture) serve(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	response := func(status int, content string) *http.Response {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(content)), Request: r}
	}
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		return response(200, `{"issuer":"https://auth.openai.com","authorization_endpoint":"https://auth.openai.com/api/accounts/authorize","token_endpoint":"https://auth.openai.com/api/accounts/oauth/token","jwks_uri":"https://auth.openai.com/jwks","id_token_signing_alg_values_supported":["RS256"]}`), nil
	case "/jwks":
		data, _ := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "fixture-key", Algorithm: "RS256", Use: "sig"}}})
		return response(200, string(data)), nil
	case "/api/accounts/oauth/token":
		f.exchanges++
		_ = r.ParseForm()
		if r.Form.Get("client_id") != f.client || r.Form.Get("resource") != "https://api.openai.com/v1" || r.Form.Get("redirect_uri") != "http://127.0.0.1:1455/auth/callback" || r.Form.Get("code_verifier") == "" {
			f.t.Errorf("invalid exchange fields: client=%s resource=%s redirect=%s", r.Form.Get("client_id"), r.Form.Get("resource"), r.Form.Get("redirect_uri"))
		}
		signingKey := f.key
		if f.badSignature {
			signingKey, _ = rsa.GenerateKey(rand.Reader, 2048)
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: signingKey, KeyID: "fixture-key"}}, nil)
		if err != nil {
			return nil, err
		}
		identity := map[string]any{"iss": "https://auth.openai.com", "aud": f.client, "sub": f.subject, "nonce": f.nonce, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "email": "same@example.com"}
		for key, value := range f.overrides {
			identity[key] = value
		}
		claims, _ := json.Marshal(identity)
		signature, err := signer.Sign(claims)
		if err != nil {
			return nil, err
		}
		raw, err := signature.CompactSerialize()
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(map[string]any{"access_token": "access-" + f.client, "refresh_token": "refresh-" + f.client, "token_type": "Bearer", "expires_in": 3600, "scope": f.scope, "id_token": raw})
		return response(200, string(data)), nil
	case "/v1/responses":
		f.inference++
		if r.Header.Get("Authorization") != "Bearer access-"+f.client {
			f.t.Error("inference used wrong registration")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != f.model || body["store"] != false || body["stream"] != true {
			f.t.Errorf("wrong registered request options: %v", body)
		}
		if rawTools, ok := body["tools"].([]any); ok {
			for _, raw := range rawTools {
				tool, _ := raw.(map[string]any)
				if tool["type"] == "function" {
					f.t.Error("public route received unsupported top-level function")
				}
			}
		}
		for _, field := range []string{"max_output_tokens", "temperature", "top_p", "previous_response_id"} {
			if _, ok := body[field]; ok {
				f.t.Errorf("unsupported field %s", field)
			}
		}
		if f.toolFlow {
			namespace := false
			if requestTools, ok := body["tools"].([]any); ok {
				for _, raw := range requestTools {
					tool, _ := raw.(map[string]any)
					namespace = namespace || tool["type"] == "namespace"
				}
			}
			if !namespace {
				f.t.Error("ordinary account request lost namespaced local tools")
			}
			if f.inference == 2 {
				result := response(200, `data: {"type":"response.completed","response":{"id":"tool-request","status":"completed","output":[{"type":"function_call","call_id":"record-1","name":"record_connection","namespace":"picoclaw","arguments":"{}"}]}}`+"\n\n")
				result.Header.Set("Content-Type", "text/event-stream")
				return result, nil
			}
			history, _ := json.Marshal(body["input"])
			if !strings.Contains(string(history), "ACCOUNT_TOOL_RESULT") || !strings.Contains(string(history), `"namespace":"picoclaw"`) {
				f.t.Error("tool continuation lost namespaced call or completed result")
			}
		}
		result := response(200, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"registered-response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
		result.Header.Set("Content-Type", "text/event-stream")
		return result, nil
	default:
		f.t.Errorf("unexpected OpenAI request: %s %s", r.Method, r.URL.Path)
		return response(404, `{}`), nil
	}
}

func TestCodexManualCallbackConnectsDistinctAccountsAndSelectedInference(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	f := fakeOpenAIRegistration(t)
	h, cookie, path := modelInferenceDashboard(t, []config.ModelConfig{{ModelName: "paid", Model: "openai/gpt-5.4", APIKey: "untouched-key"}})
	var hostID string
	var lastAccount string
	for _, client := range []string{"oaiapp_one", "oaiapp_two"} {
		w := dashboardRequest(h, "POST", "/api/oauth/login", `{"provider":"openai","method":"browser","label":"Work account"}`, cookie)
		if w.Code != 200 {
			t.Fatalf("start = %d %s", w.Code, w.Body.String())
		}
		var start struct {
			FlowID  string `json:"flow_id"`
			AuthURL string `json:"auth_url"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &start)
		authorize, err := url.Parse(start.AuthURL)
		if err != nil {
			t.Fatal(err)
		}
		query := authorize.Query()
		if authorize.Path != "/api/accounts/authorize" || query.Get("client_id") != "dynamic_agent_client" || query.Get("nonce") == "" || query.Get("ext_agent_host_id") == "" || query.Get("redirect_uri") != "http://127.0.0.1:1455/auth/callback" {
			t.Fatalf("wrong registration request: %s", authorize.Path)
		}
		if hostID != "" && hostID != query.Get("ext_agent_host_id") {
			t.Fatal("host identity changed")
		}
		hostID = query.Get("ext_agent_host_id")
		f.client = client
		f.nonce = query.Get("nonce")
		callback := "http://127.0.0.1:1455/auth/callback?" + url.Values{"code": {"code-" + client}, "state": {query.Get("state")}, "client_id": {client}}.Encode()
		payload, _ := json.Marshal(map[string]string{"callback_url": callback})
		w = dashboardRequest(h, "POST", "/api/oauth/flows/"+start.FlowID+"/complete", string(payload), cookie)
		if w.Code != 200 {
			t.Fatalf("complete = %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "access-") || strings.Contains(w.Body.String(), "refresh-") || strings.Contains(w.Body.String(), "code-") {
			t.Fatal("completion response leaked credentials")
		}
		var completed struct {
			AccountID string `json:"account_id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &completed)
		lastAccount = completed.AccountID
		if lastAccount == "" {
			t.Fatal("missing saved connection reference")
		}
	}
	w := dashboardRequest(h, "GET", "/api/oauth/accounts", "", cookie)
	var accounts struct {
		Accounts []json.RawMessage `json:"accounts"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &accounts)
	if len(accounts.Accounts) != 2 {
		t.Fatalf("new registration overwrote account: %s", w.Body.String())
	}
	bind, _ := json.Marshal(map[string]any{"model_name": "selected", "model": "openai/gpt-6.1-sol", "auth_method": "oauth", "account_id": lastAccount})
	w = dashboardRequest(h, "POST", "/api/models", string(bind), cookie)
	if w.Code != 200 {
		t.Fatalf("save selected=%d %s", w.Code, w.Body.String())
	}
	w = dashboardRequest(h, "POST", "/api/models/1/test", "", cookie)
	if w.Code != 200 || f.inference != 1 {
		t.Fatalf("registered inference=%d %s", w.Code, w.Body.String())
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelList[0].APIKey != "untouched-key" || cfg.ModelList[0].AuthMethod != "" {
		t.Fatal("registration changed unrelated model")
	}
	cfg.Agents.Defaults.ModelName = "selected"
	cfg.Agents.Defaults.Model = "selected"
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	loop := agent.NewAgentLoop(cfg, messageBus, provider)
	defer loop.Close()
	recordTool := &accountRecordingTool{}
	loop.RegisterTool(recordTool)
	f.toolFlow = true
	answer, err := loop.ProcessDirect(t.Context(), "Reply with OK.", "registered-account-request")
	if err != nil || answer != "OK" || f.inference != 3 || recordTool.calls != 1 {
		t.Fatalf("ordinary selected-account request=%q error=%v calls=%d", answer, err, f.inference)
	}
}

func beginManualCodex(t *testing.T, h http.Handler, cookie *http.Cookie, f *openAIRegistrationFixture, accountID string) (string, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"provider": "openai", "method": "browser", "account_id": accountID})
	w := dashboardRequest(h, "POST", "/api/oauth/login", string(body), cookie)
	if w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body.String())
	}
	var start struct {
		FlowID  string `json:"flow_id"`
		AuthURL string `json:"auth_url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	authorize, _ := url.Parse(start.AuthURL)
	values := authorize.Query()
	f.nonce = values.Get("nonce")
	callback := "http://127.0.0.1:1455/auth/callback?" + url.Values{"code": {"authorization-secret"}, "state": {values.Get("state")}, "client_id": {f.client}}.Encode()
	return start.FlowID, callback
}

func completeManualCodex(h http.Handler, cookie *http.Cookie, id, callback string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"callback_url": callback})
	return dashboardRequest(h, "POST", "/api/oauth/flows/"+id+"/complete", string(body), cookie)
}

func TestCodexManualCallbackRejectsForeignMalformedExpiredAndReplayedFlows(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	f := fakeOpenAIRegistration(t)
	h, cookie, _ := modelInferenceDashboard(t, nil)
	id, callback := beginManualCodex(t, h, cookie, f, "")
	originalNonce := f.nonce
	for _, bad := range []string{
		strings.Replace(callback, "127.0.0.1", "localhost", 1),
		strings.Replace(callback, "/auth/callback", "/callback", 1),
		callback + "&state=duplicate",
		strings.Replace(callback, "state=", "state=foreign-", 1),
		strings.Replace(callback, "oaiapp_one", "dynamic_agent_client", 1),
		callback + "#fragment",
	} {
		w := completeManualCodex(h, cookie, id, bad)
		if w.Code != 400 {
			t.Fatalf("malformed callback accepted=%d", w.Code)
		}
		if strings.Contains(w.Body.String(), "authorization-secret") {
			t.Fatal("callback code leaked")
		}
	}
	if f.exchanges != 0 {
		t.Fatal("invalid callback reached token endpoint")
	}
	// A different valid dashboard session cannot read or consume this flow.
	sessions := middleware.NewLauncherDashboardSessions()
	mux := http.NewServeMux()
	NewHandler(filepath.Join(t.TempDir(), "config.json")).RegisterRoutes(mux)
	scoped := middleware.LauncherDashboardAuth(middleware.LauncherDashboardAuthConfig{Sessions: sessions}, mux)
	issue := func() *http.Cookie {
		w := httptest.NewRecorder()
		if err := sessions.Issue(w, httptest.NewRequest("GET", "/", nil), nil); err != nil {
			t.Fatal(err)
		}
		return w.Result().Cookies()[0]
	}
	owner, other := issue(), issue()
	foreignID, foreignCallback := beginManualCodex(t, scoped, owner, f, "")
	if w := dashboardRequest(scoped, "GET", "/api/oauth/flows/"+foreignID, "", other); w.Code != 404 {
		t.Fatalf("foreign status=%d", w.Code)
	}
	if w := completeManualCodex(scoped, other, foreignID, foreignCallback); w.Code != 404 {
		t.Fatalf("foreign callback=%d", w.Code)
	}
	if w := dashboardRequest(h, "GET", "/oauth/callback?"+strings.SplitN(callback, "?", 2)[1], "", cookie); w.Code != 400 {
		t.Fatalf("manual bypass=%d", w.Code)
	}
	f.nonce = originalNonce
	w := completeManualCodex(h, cookie, id, callback)
	if w.Code != 200 {
		t.Fatalf("valid callback=%d %s", w.Code, w.Body.String())
	}
	if w := completeManualCodex(h, cookie, id, callback); w.Code != 409 {
		t.Fatalf("replay=%d", w.Code)
	}
	if f.exchanges != 1 {
		t.Fatalf("replayed exchange count=%d", f.exchanges)
	}
	resetOAuthHooks(t)
	now := time.Now()
	oauthNow = func() time.Time { return now }
	expiredID, expiredCallback := beginManualCodex(t, h, cookie, f, "")
	now = now.Add(oauthBrowserFlowTTL + time.Second)
	if w := completeManualCodex(h, cookie, expiredID, expiredCallback); w.Code != 409 {
		t.Fatalf("expired callback=%d", w.Code)
	}
}

func TestCodexConcurrentCallbackConsumesAuthorizationOnce(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	f := fakeOpenAIRegistration(t)
	h, cookie, _ := modelInferenceDashboard(t, nil)
	id, callback := beginManualCodex(t, h, cookie, f, "")
	results := make(chan int, 2)
	for range 2 {
		go func() { results <- completeManualCodex(h, cookie, id, callback).Code }()
	}
	first, second := <-results, <-results
	if !((first == 200 && second == 409) || (first == 409 && second == 200)) {
		t.Fatalf("concurrent completion=%d/%d", first, second)
	}
	if f.exchanges != 1 {
		t.Fatalf("duplicate code exchanges=%d", f.exchanges)
	}
}

func TestCodexIDTokenNonceAndReturningRegistrationAreVerified(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	f := fakeOpenAIRegistration(t)
	h, cookie, _ := modelInferenceDashboard(t, nil)
	id, callback := beginManualCodex(t, h, cookie, f, "")
	f.nonce = "wrong-nonce"
	if w := completeManualCodex(h, cookie, id, callback); w.Code != 400 {
		t.Fatalf("wrong nonce accepted=%d", w.Code)
	}
	id, callback = beginManualCodex(t, h, cookie, f, "")
	w := completeManualCodex(h, cookie, id, callback)
	if w.Code != 200 {
		t.Fatalf("initial registration=%d %s", w.Code, w.Body.String())
	}
	var complete struct {
		AccountID string `json:"account_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &complete)
	id, callback = beginManualCodex(t, h, cookie, f, complete.AccountID)
	if w := completeManualCodex(h, cookie, id, strings.Replace(callback, "oaiapp_one", "oaiapp_other", 1)); w.Code != 400 {
		t.Fatalf("changed issued client accepted=%d", w.Code)
	}
	f.subject = "another-user"
	if w := completeManualCodex(h, cookie, id, callback); w.Code != 400 {
		t.Fatalf("changed subject accepted=%d", w.Code)
	}
	listing := dashboardRequest(h, "GET", "/api/oauth/accounts", "", cookie)
	if !strings.Contains(listing.Body.String(), complete.AccountID) {
		t.Fatal("invalid reconnect erased valid account")
	}
}

func TestCodexMissingPlanGrantRetainsSignInWithoutInference(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	f := fakeOpenAIRegistration(t)
	f.scope = "openid profile email"
	h, cookie, _ := modelInferenceDashboard(t, nil)
	id, callback := beginManualCodex(t, h, cookie, f, "")
	w := completeManualCodex(h, cookie, id, callback)
	if w.Code != 200 {
		t.Fatalf("identity-only sign-in=%d %s", w.Code, w.Body.String())
	}
	var complete struct {
		AccountID string `json:"account_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &complete)
	payload, _ := json.Marshal(map[string]string{"model_name": "no-plan", "model": "openai/gpt-6.1-sol", "auth_method": "oauth", "account_id": complete.AccountID})
	w = dashboardRequest(h, "POST", "/api/models/test-inline", string(payload), cookie)
	if w.Code != 502 || f.inference != 0 {
		t.Fatalf("missing plan permission inferred=%d calls=%d", w.Code, f.inference)
	}
	listing := dashboardRequest(h, "GET", "/api/oauth/accounts", "", cookie)
	if !strings.Contains(listing.Body.String(), "plan_permission_required") {
		t.Fatalf("missing plan permission hidden: %s", listing.Body.String())
	}
}

func TestCodexRegisteredStreamFailuresKeepRecoveryMetadata(t *testing.T) {
	for _, scenario := range []struct {
		code   string
		status int
	}{
		{"subscription_sharing_usage_limit_exceeded", 429},
		{"subscription_sharing_usage_unavailable", 503},
		{"subscription_sharing_invalid_user", 401},
	} {
		t.Run(scenario.code, func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			f := fakeOpenAIRegistration(t)
			h, cookie, _ := modelInferenceDashboard(t, nil)
			id, callback := beginManualCodex(t, h, cookie, f, "")
			w := completeManualCodex(h, cookie, id, callback)
			if w.Code != 200 {
				t.Fatalf("connect=%d", w.Code)
			}
			var complete struct {
				AccountID string `json:"account_id"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &complete)
			inner := http.DefaultTransport
			http.DefaultTransport = accountRecordingTransport{handler: func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/v1/responses" {
					return inner.RoundTrip(r)
				}
				body := `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"PARTIAL_SECRET"}` + "\n\n" + `data: {"type":"response.failed","response":{"id":"failure","status":"failed","error":{"code":"` + scenario.code + `","message":"NEVER_EXPOSE_PROVIDER_BODY"}}}` + "\n\n"
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			}}
			model := config.ModelConfig{ModelName: "selected", Model: "openai/gpt-6.1-sol", AuthMethod: "oauth", AccountID: complete.AccountID}
			provider, modelID, err := providers.CreateProviderFromConfig(&model)
			if err != nil {
				t.Fatal(err)
			}
			response, err := provider.Chat(t.Context(), []providers.Message{{Role: "user", Content: "hello"}}, nil, modelID, nil)
			var failure *protocoltypes.HTTPError
			if response != nil || !errors.As(err, &failure) || failure.StatusCode != scenario.status || failure.Code != scenario.code {
				t.Fatalf("failure metadata lost: %+v %v", response, err)
			}
			if strings.Contains(err.Error(), "PARTIAL_SECRET") || strings.Contains(err.Error(), "NEVER_EXPOSE") {
				t.Fatalf("failed stream leaked content: %v", err)
			}
		})
	}
}

func TestCodexManualCallbackVerifiesSignedIdentityClaims(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		claims       map[string]any
		badSignature bool
	}{
		{name: "audience", claims: map[string]any{"aud": "another-client"}},
		{name: "issuer", claims: map[string]any{"iss": "https://foreign.example"}},
		{name: "expired", claims: map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}},
		{name: "missing-subject", claims: map[string]any{"sub": ""}},
		{name: "signature", badSignature: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			f := fakeOpenAIRegistration(t)
			h, cookie, _ := modelInferenceDashboard(t, nil)
			id, callback := beginManualCodex(t, h, cookie, f, "")
			f.overrides = scenario.claims
			f.badSignature = scenario.badSignature
			if w := completeManualCodex(h, cookie, id, callback); w.Code != 400 {
				t.Fatalf("invalid signed identity accepted=%d", w.Code)
			}
			w := dashboardRequest(h, "GET", "/api/oauth/accounts", "", cookie)
			if !strings.Contains(w.Body.String(), `"accounts":[]`) {
				t.Fatalf("invalid identity persisted account=%s", w.Body.String())
			}
		})
	}
}

func TestCodexManualProviderDenialDoesNotExchangeOrExposeDescription(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	f := fakeOpenAIRegistration(t)
	h, cookie, _ := modelInferenceDashboard(t, nil)
	id, callback := beginManualCodex(t, h, cookie, f, "")
	parsed, _ := url.Parse(callback)
	query := parsed.Query()
	query.Del("code")
	query.Set("error", "access_denied")
	query.Set("error_description", "PRIVATE_PROVIDER_DETAIL")
	parsed.RawQuery = query.Encode()
	w := completeManualCodex(h, cookie, id, parsed.String())
	if w.Code != 400 || f.exchanges != 0 || strings.Contains(w.Body.String(), "PRIVATE_PROVIDER_DETAIL") {
		t.Fatalf("provider denial=%d %s exchanges=%d", w.Code, w.Body.String(), f.exchanges)
	}
	if w := completeManualCodex(h, cookie, id, callback); w.Code != 409 {
		t.Fatalf("denied flow replay=%d", w.Code)
	}
}

type accountRecordingTool struct{ calls int }

func (t *accountRecordingTool) Name() string        { return "record_connection" }
func (t *accountRecordingTool) Description() string { return "Record the selected account request" }
func (t *accountRecordingTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *accountRecordingTool) Execute(ctx context.Context, arguments map[string]any) *tools.ToolResult {
	t.calls++
	return tools.SilentResult("ACCOUNT_TOOL_RESULT")
}

func TestCodexRegisteredModelSlugIsNotSubstituted(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	f := fakeOpenAIRegistration(t)
	f.model = "custom-model-preview"
	h, cookie, _ := modelInferenceDashboard(t, nil)
	id, callback := beginManualCodex(t, h, cookie, f, "")
	w := completeManualCodex(h, cookie, id, callback)
	if w.Code != 200 {
		t.Fatalf("connect=%d", w.Code)
	}
	var complete struct {
		AccountID string `json:"account_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &complete)
	payload, _ := json.Marshal(map[string]string{"model_name": "exact", "model": "openai/custom-model-preview", "auth_method": "oauth", "account_id": complete.AccountID})
	w = dashboardRequest(h, "POST", "/api/models/test-inline", string(payload), cookie)
	if w.Code != 200 || f.inference != 1 {
		t.Fatalf("exact selected model probe=%d %s", w.Code, w.Body.String())
	}
}
