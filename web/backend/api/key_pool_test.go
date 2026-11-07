package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	firstPoolSecret  = "sk-ant-api03-first-organization-1111"
	secondPoolSecret = "sk-ant-api03-second-organization-2222"
)

func keyPoolConfigPath(t *testing.T) string {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.KeyPools = []config.KeyPool{{ID: "orgs", Name: "Anthropic organizations", Keys: []config.KeyPoolKey{
		{ID: "first", Label: "First org", Enabled: true, APIKey: firstPoolSecret},
		{ID: "second", Label: "Second org", Enabled: true, APIKey: secondPoolSecret},
	}}}
	cfg.ModelList = []config.ModelConfig{{ModelName: "claude", Model: "anthropic/claude-opus-5-5", KeyPoolID: "orgs"}}
	cfg.Agents.Defaults.ModelName = "claude"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return path
}

func serve(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func assertNoPoolSecret(t *testing.T, listing, body string) {
	t.Helper()
	if strings.Contains(body, firstPoolSecret) || strings.Contains(body, secondPoolSecret) {
		t.Fatalf("%s exposed a Key pool secret: %s", listing, body)
	}
}

func TestKeyPoolSecretsStayMaskedInConfigurationListings(t *testing.T) {
	fakeAnthropicModels(t, "sk-ant-api03-replacement")
	path := keyPoolConfigPath(t)
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)

	models := serve(mux, http.MethodGet, "/api/models", "")
	assertNoPoolSecret(t, "GET /api/models", models.Body.String())
	if !strings.Contains(models.Body.String(), `"key_pool_id":"orgs"`) ||
		!strings.Contains(models.Body.String(), `"configured":true`) {
		t.Errorf("GET /api/models does not show the configured pool reference: %s", models.Body.String())
	}
	get := serve(mux, http.MethodGet, "/api/config", "")
	assertNoPoolSecret(t, "GET /api/config", get.Body.String())

	if put := serve(mux, http.MethodPut, "/api/config", get.Body.String()); put.Code != http.StatusOK {
		t.Fatalf("saving the listed config = %d %s", put.Code, put.Body.String())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if keys := loaded.KeyPools[0].Keys; keys[0].APIKey != firstPoolSecret || keys[1].APIKey != secondPoolSecret {
		t.Fatalf("saving masked keys replaced the stored secrets: %+v", keys)
	}

	replaced := strings.Replace(get.Body.String(), maskAPIKey(secondPoolSecret), "sk-ant-api03-replacement", 1)
	if put := serve(mux, http.MethodPut, "/api/config", replaced); put.Code != http.StatusOK {
		t.Fatalf("replacing a key = %d %s", put.Code, put.Body.String())
	}
	loaded, err = config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if keys := loaded.KeyPools[0].Keys; keys[0].APIKey != firstPoolSecret ||
		keys[1].APIKey != "sk-ant-api03-replacement" {
		t.Fatalf("explicit replacement was not saved alone: %+v", keys)
	}
}

// keyPoolListing is the dashboard's view of the Key pools.
type keyPoolListing struct {
	Runtime string `json:"runtime"`
	Pools   []struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Models []string `json:"models"`
		Keys   []struct {
			ID      string    `json:"id"`
			Label   string    `json:"label"`
			Enabled bool      `json:"enabled"`
			APIKey  string    `json:"api_key"`
			State   string    `json:"state"`
			Until   time.Time `json:"until"`
		} `json:"keys"`
	} `json:"pools"`
}

func listKeyPools(t *testing.T, mux *http.ServeMux) keyPoolListing {
	t.Helper()
	rec := serve(mux, http.MethodGet, "/api/key-pools", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/key-pools = %d %s", rec.Code, rec.Body.String())
	}
	assertNoPoolSecret(t, "GET /api/key-pools", rec.Body.String())
	var listing keyPoolListing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	return listing
}

func TestKeyPoolsAreCreatedRenamedAndDeletedOnlyWhenUnused(t *testing.T) {
	path := keyPoolConfigPath(t)
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)

	listing := listKeyPools(t, mux)
	if len(listing.Pools) != 1 || listing.Pools[0].Name != "Anthropic organizations" ||
		!slices.Equal(listing.Pools[0].Models, []string{"claude"}) || len(listing.Pools[0].Keys) != 2 ||
		listing.Pools[0].Keys[0].Label != "First org" || listing.Pools[0].Keys[0].APIKey != maskAPIKey(firstPoolSecret) {
		t.Fatalf("listing = %+v", listing)
	}

	created := serve(mux, http.MethodPost, "/api/key-pools", `{"name":"  Spare organizations "}`)
	var spare struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &spare) != nil ||
		spare.ID == "" || spare.Name != "Spare organizations" {
		t.Fatalf("POST /api/key-pools = %d %s", created.Code, created.Body.String())
	}
	if rec := serve(mux, http.MethodPost, "/api/key-pools", `{"name":" "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a pool without a name = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(
		mux,
		http.MethodPatch,
		"/api/key-pools/"+spare.ID,
		`{"name":"Reserve"}`,
	); rec.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body.String())
	}
	if pools := listKeyPools(t, mux).Pools; len(pools) != 2 || pools[1].Name != "Reserve" || len(pools[1].Keys) != 0 {
		t.Fatalf("after create and rename: %+v", pools)
	}

	refused := serve(mux, http.MethodDelete, "/api/key-pools/orgs", "")
	var conflict struct {
		Models []string `json:"models"`
	}
	if refused.Code != http.StatusConflict || json.Unmarshal(refused.Body.Bytes(), &conflict) != nil ||
		!slices.Equal(conflict.Models, []string{"claude"}) {
		t.Fatalf("deleting a referenced pool = %d %s", refused.Code, refused.Body.String())
	}
	if rec := serve(mux, http.MethodDelete, "/api/key-pools/"+spare.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("deleting an unused pool = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(mux, http.MethodDelete, "/api/key-pools/"+spare.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleting a missing pool = %d", rec.Code)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.KeyPools) != 1 || loaded.KeyPools[0].ID != "orgs" ||
		loaded.KeyPools[0].Keys[1].APIKey != secondPoolSecret {
		t.Fatalf("saved pools = %+v", loaded.KeyPools)
	}
}

// anthropicModelsHandler plays Anthropic's model listing: validKey lists
// models, any other key is refused as Anthropic refuses a revoked one. Each
// request's host, path and key are recorded.
func anthropicModelsHandler(validKey string, requests *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*requests = append(*requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("X-Api-Key"))
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") != validKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write(
				[]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`),
			)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5",` +
			`"created_at":"2026-09-21T00:00:00Z"}],"first_id":"claude-opus-5-5","last_id":"claude-opus-5-5","has_more":false}`))
	}
}

// fakeAnthropicModels serves anthropicModelsHandler as Anthropic's default API base.
func fakeAnthropicModels(t *testing.T, validKey string) *[]string {
	t.Helper()
	var requests []string
	server := httptest.NewServer(anthropicModelsHandler(validKey, &requests))
	t.Cleanup(server.Close)
	previous := anthropicAPIBase
	anthropicAPIBase = server.URL
	t.Cleanup(func() { anthropicAPIBase = previous })
	return &requests
}

// Key checks and model lists for a pool travel the api_base and proxy of a
// model on the pool, not Anthropic's default base.
func TestKeyPoolRequestsUseTheNetworkPathOfAModelOnThePool(t *testing.T) {
	const validSecret = "sk-ant-api03-third-organization-3333"
	direct := fakeAnthropicModels(t, validSecret)
	var proxied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = r.Host + r.URL.Path // a proxied request names its destination
		anthropicModelsHandler(validSecret, &proxied)(w, r)
	}))
	t.Cleanup(proxy.Close)
	path := keyPoolConfigPath(t)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelList[0].APIBase = "http://anthropic-gateway.internal/v1"
	cfg.ModelList[0].Proxy = proxy.URL
	if err = config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)

	if rec := serve(mux, http.MethodPost, "/api/key-pools/orgs/keys",
		`{"label":"Third org","api_key":"`+validSecret+`"}`); rec.Code != http.StatusCreated {
		t.Fatalf("adding a key = %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(mux, http.MethodGet, "/api/key-pools/orgs/models", ""); rec.Code != http.StatusOK {
		t.Fatalf("listing pool models = %d %s", rec.Code, rec.Body.String())
	}

	want := []string{
		"GET anthropic-gateway.internal/v1/models " + validSecret,
		"GET anthropic-gateway.internal/v1/models " + firstPoolSecret,
		"GET anthropic-gateway.internal/v1/models " + secondPoolSecret,
		"GET anthropic-gateway.internal/v1/models " + validSecret,
	}
	if !slices.Equal(proxied, want) {
		t.Fatalf("proxy received %q, want %q", proxied, want)
	}
	if len(*direct) != 0 {
		t.Fatalf("Anthropic's default base received %q, want nothing", *direct)
	}
}

// Raw configuration saves check new and changed Key pool secrets like the
// Credentials page does.
func TestRawConfigSaveRefusesAKeyPoolSecretAnthropicRejects(t *testing.T) {
	const validSecret = "sk-ant-api03-third-organization-3333"
	requests := fakeAnthropicModels(t, validSecret)
	path := keyPoolConfigPath(t)
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	listed := serve(mux, http.MethodGet, "/api/config", "").Body.String()

	revoked := strings.Replace(listed, maskAPIKey(secondPoolSecret), "sk-ant-api03-revoked-key-0000", 1)
	if rec := serve(mux, http.MethodPut, "/api/config", revoked); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "rejected") || strings.Contains(rec.Body.String(), "revoked-key") {
		t.Fatalf("saving a revoked key = %d %s", rec.Code, rec.Body.String())
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.KeyPools[0].Keys[1].APIKey; got != secondPoolSecret {
		t.Fatalf("a refused save replaced the key with %q", got)
	}

	if rec := serve(mux, http.MethodPut, "/api/config",
		strings.Replace(listed, maskAPIKey(secondPoolSecret), validSecret, 1)); rec.Code != http.StatusOK {
		t.Fatalf("saving a valid replacement = %d %s", rec.Code, rec.Body.String())
	}
	want := []string{"GET /v1/models sk-ant-api03-revoked-key-0000", "GET /v1/models " + validSecret}
	if !slices.Equal(*requests, want) {
		t.Fatalf("Anthropic received %q, want only the new secrets checked %q", *requests, want)
	}
}

func TestKeyPoolKeysAreValidatedWithAnthropicBeforeTheyAreSaved(t *testing.T) {
	const validSecret = "sk-ant-api03-third-organization-3333"
	requests := fakeAnthropicModels(t, validSecret)
	path := keyPoolConfigPath(t)
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)

	rejected := serve(mux, http.MethodPost, "/api/key-pools/orgs/keys",
		`{"label":"Third org","api_key":"sk-ant-api03-revoked-key-0000"}`)
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "rejected") {
		t.Fatalf("adding a revoked key = %d %s", rejected.Code, rejected.Body.String())
	}
	if keys := listKeyPools(t, mux).Pools[0].Keys; len(keys) != 2 {
		t.Fatalf("a rejected key was saved: %+v", keys)
	}

	added := serve(mux, http.MethodPost, "/api/key-pools/orgs/keys",
		`{"label":" Third org ","api_key":" `+validSecret+` "}`)
	if added.Code != http.StatusCreated || strings.Contains(added.Body.String(), validSecret) {
		t.Fatalf("adding a valid key = %d %s", added.Code, added.Body.String())
	}
	want := []string{
		"GET /v1/models sk-ant-api03-revoked-key-0000",
		"GET /v1/models " + validSecret,
	}
	if !slices.Equal(*requests, want) {
		t.Fatalf("Anthropic received %q, want only model listings %q", *requests, want)
	}
	keys := listKeyPools(t, mux).Pools[0].Keys
	if len(keys) != 3 || keys[2].Label != "Third org" || !keys[2].Enabled || keys[2].State == "" ||
		keys[2].APIKey != maskAPIKey(validSecret) {
		t.Fatalf("listing after adding = %+v", keys)
	}
	if strings.Contains(listKeyPools(t, mux).Pools[0].Keys[2].APIKey, validSecret) {
		t.Fatal("listing exposed the added secret")
	}

	third := "/api/key-pools/orgs/keys/" + keys[2].ID
	if rec := serve(mux, http.MethodPatch, third, `{"label":"Org three","enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH key = %d %s", rec.Code, rec.Body.String())
	}
	keys = listKeyPools(t, mux).Pools[0].Keys
	if keys[2].Label != "Org three" || keys[2].Enabled || keys[2].State != "disabled" {
		t.Fatalf("after disabling: %+v", keys[2])
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.KeyPools[0].Keys[2]; got.APIKey != validSecret || got.Enabled {
		t.Fatalf("saved key = %+v", got)
	}

	if rec := serve(mux, http.MethodDelete, third, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE key = %d %s", rec.Code, rec.Body.String())
	}
	if keys := listKeyPools(t, mux).Pools[0].Keys; len(keys) != 2 {
		t.Fatalf("after removing: %+v", keys)
	}
	if rec := serve(mux, http.MethodPatch, third, `{"enabled":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("PATCH removed key = %d", rec.Code)
	}
}

// gatewayHealth makes the dashboard see this test process as the running
// gateway, or no gateway at all when running is false.
func gatewayHealth(t *testing.T, running bool) {
	t.Helper()
	previous := gatewayHealthGet
	gatewayHealthGet = func(_ string, _ time.Duration) (*http.Response, error) {
		if !running {
			return nil, errors.New("connection refused")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"pid":%d,"status":"ok"}`, os.Getpid()))),
		}, nil
	}
	t.Cleanup(func() { gatewayHealthGet = previous })
}

func TestKeyPoolKeyStateComesFromTheGatewayAndReturnToServiceClearsExhaustion(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var mu sync.Mutex
	outOfCredit, servedBy := true, ""
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path != "/v1/messages":
			http.NotFound(w, r)
		case outOfCredit:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":`+
				`"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`)
		default:
			servedBy = r.Header.Get("X-Api-Key")
			_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",`+
				`"content":[{"type":"text","text":"ANSWER"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		}
	}))
	t.Cleanup(fake.Close)

	cfg := config.DefaultConfig()
	cfg.KeyPools = []config.KeyPool{{ID: "returning-orgs", Name: "Organizations", Keys: []config.KeyPoolKey{
		{ID: "first", Label: "First org", Enabled: true, APIKey: firstPoolSecret},
		{ID: "second", Label: "Second org", Enabled: false, APIKey: secondPoolSecret},
	}}}
	cfg.ModelList = []config.ModelConfig{
		{
			ID:        "claude",
			ModelName: "claude",
			Model:     "anthropic/claude-opus-5-5",
			APIBase:   fake.URL,
			KeyPoolID: "returning-orgs",
		},
	}
	cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + "claude"
	cfg.Agents.Defaults.Model = cfg.Agents.Defaults.ModelName
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.Workspace = t.TempDir()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	provider, _, err := providers.CreateProvider(loaded)
	if err != nil {
		t.Fatal(err)
	}
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	loop := agent.NewAgentLoop(loaded, mb, provider, func() (*config.Config, error) { return config.LoadConfig(path) })
	t.Cleanup(loop.Close)
	status, err := loop.EnableRuntimeStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(status.Close)
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	keyStates := func() []string {
		t.Helper()
		if err := status.Flush(); err != nil {
			t.Fatal(err)
		}
		listing := listKeyPools(t, mux)
		states := append(make([]string, 0, 1+len(listing.Pools[0].Keys)), listing.Runtime)
		for _, key := range listing.Pools[0].Keys {
			states = append(states, key.State)
		}
		return states
	}

	gatewayHealth(t, false)
	if got := keyStates(); !slices.Equal(got, []string{"offline", "unknown", "disabled"}) {
		t.Fatalf("without a running gateway: %q", got)
	}

	gatewayHealth(t, true)
	if _, err := loop.ProcessDirect(t.Context(), "hello", "chat-1"); err == nil {
		t.Fatal("a request with every key out of credit succeeded")
	}
	if got := keyStates(); !slices.Equal(got, []string{"running", "exhausted", "disabled"}) {
		t.Fatalf("after Anthropic reported no credit: %q", got)
	}
	if until := listKeyPools(t, mux).Pools[0].Keys[0].Until; !until.After(time.Now()) {
		t.Errorf("exhausted key shows no next recheck: %v", until)
	}

	mu.Lock()
	outOfCredit = false
	mu.Unlock()
	if rec := serve(
		mux,
		http.MethodPost,
		"/api/key-pools/returning-orgs/keys/first/return",
		"",
	); rec.Code != http.StatusOK {
		t.Fatalf("return to service = %d %s", rec.Code, rec.Body.String())
	}
	if got := listKeyPools(t, mux).Pools[0].Keys[0].State; got != "available" {
		t.Fatalf("returned key before the gateway applied the return = %q", got)
	}
	if got := keyStates(); !slices.Equal(got, []string{"running", "available", "disabled"}) {
		t.Fatalf("after the gateway applied the return: %q", got)
	}
	if answer, err := loop.ProcessDirect(t.Context(), "hello again", "chat-1"); err != nil || answer != "ANSWER" {
		t.Fatalf("request after return = %q, %v", answer, err)
	}
	if servedBy != firstPoolSecret {
		t.Fatalf("request after return was served by %q", servedBy)
	}
}

func TestPoolModelIsConfiguredOnlyWithAnEnabledKeyThatHasASecret(t *testing.T) {
	model := func(keys ...config.KeyPoolKey) config.ModelConfig {
		cfg := config.DefaultConfig()
		cfg.KeyPools = []config.KeyPool{{ID: "orgs", Name: "Organizations", Keys: keys}}
		cfg.ModelList = []config.ModelConfig{
			{ModelName: "claude", Model: "anthropic/claude-opus-5-5", KeyPoolID: "orgs"},
		}
		if err := cfg.ResolveKeyPools(); err != nil {
			t.Fatal(err)
		}
		return cfg.ModelList[0]
	}
	if hasModelConfiguration(model(config.KeyPoolKey{ID: "empty", Label: "Empty", Enabled: true})) {
		t.Error("a pool whose only enabled key has no secret counts as configured")
	}
	if !hasModelConfiguration(
		model(config.KeyPoolKey{ID: "org", Label: "Org", Enabled: true, APIKey: firstPoolSecret}),
	) {
		t.Error("a pool with an enabled key that has a secret is not configured")
	}
}

func TestAddingAKeyIsRefusedWhenThePoolEndpointChangedDuringItsCheck(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.KeyPools = []config.KeyPool{{ID: "orgs", Name: "Organizations"}}
	cfg.ModelList = []config.ModelConfig{
		{ID: "claude", ModelName: "claude", Model: "anthropic/claude-opus-5-5", KeyPoolID: "orgs"},
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the pool's new endpoint received %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(moved.Close)
	// The fake Anthropic accepts the key, and while it answers the pool's model
	// moves to another endpoint.
	checked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		edited, err := config.LoadConfig(path)
		if err != nil {
			t.Error(err)
			return
		}
		edited.ModelList[0].APIBase = moved.URL
		if err := config.SaveConfig(path, edited); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"has_more":false}`))
	}))
	t.Cleanup(checked.Close)
	cfg.ModelList[0].APIBase = checked.URL
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)

	rec := serve(mux, http.MethodPost, "/api/key-pools/orgs/keys", `{"label":"Org","api_key":"`+firstPoolSecret+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("adding a key checked over an endpoint the pool left = %d %s, want 409", rec.Code, rec.Body.String())
	}
	saved, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.KeyPools[0].Keys) != 0 {
		t.Fatalf("the key was saved for the moved pool: %+v", saved.KeyPools[0].Keys)
	}
}
