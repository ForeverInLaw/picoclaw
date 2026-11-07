package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/modelruntime"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestKeyPoolModelsComeFromAnthropicThroughTheNextWorkingKey(t *testing.T) {
	requests := fakeAnthropicModels(t, secondPoolSecret)
	mux := http.NewServeMux()
	NewHandler(keyPoolConfigPath(t)).RegisterRoutes(mux)

	rec := serve(mux, http.MethodGet, "/api/key-pools/orgs/models", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET pool models = %d %s", rec.Code, rec.Body.String())
	}
	assertNoPoolSecret(t, "GET pool models", rec.Body.String())
	var listing struct {
		Models []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Models) != 1 || listing.Models[0].ID != "claude-opus-5-5" ||
		listing.Models[0].DisplayName != "Claude Opus 5.5" {
		t.Fatalf("pool models = %+v", listing.Models)
	}
	want := []string{"GET /v1/models " + firstPoolSecret, "GET /v1/models " + secondPoolSecret}
	if !slices.Equal(*requests, want) {
		t.Fatalf("Anthropic received %q, want %q", *requests, want)
	}
}

func TestKeyPoolModelsFailWhenNoKeyCanListThem(t *testing.T) {
	fakeAnthropicModels(t, "sk-ant-api03-not-in-this-pool")
	mux := http.NewServeMux()
	NewHandler(keyPoolConfigPath(t)).RegisterRoutes(mux)

	rec := serve(mux, http.MethodGet, "/api/key-pools/orgs/models", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"status":"error"`) {
		t.Fatalf("GET pool models with no working key = %d %s", rec.Code, rec.Body.String())
	}
	assertNoPoolSecret(t, "GET pool models", rec.Body.String())
	if rec := serve(mux, http.MethodGet, "/api/key-pools/missing/models", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET models of a missing pool = %d %s", rec.Code, rec.Body.String())
	}
}

const thirdPoolSecret = "sk-ant-api03-third-organization-3333"

// keyPoolTestResult is Test model's outcome for a pool-backed model.
type keyPoolTestResult struct {
	Status string `json:"status"`
	Keys   []struct {
		Label   string `json:"label"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"keys"`
}

// fakeAnthropicMessages plays Anthropic's Messages API: the third key is out
// of credit and every other key answers. It records each request's key, model,
// beta header and whether it asked for server-side fallbacks.
func fakeAnthropicMessages(t *testing.T) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, fallbacks := body["fallbacks"]
		key := r.Header.Get("X-Api-Key")
		mu.Lock()
		requests = append(requests, fmt.Sprintf("%s %s %s model=%v beta=%q fallbacks=%t",
			r.Method, r.URL.Path, key, body["model"], r.Header.Get("Anthropic-Beta"), fallbacks))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if key == thirdPoolSecret {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":`+
				`"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",`+
			`"content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(server.Close)
	return server.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		recorded := slices.Clone(requests)
		requests = nil
		slices.Sort(recorded)
		return recorded
	}
}

func TestTestModelAnswersPerAvailablePoolKeyWithoutFallbacks(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	anthropicBase, received := fakeAnthropicMessages(t)
	var reserveCalls atomic.Int32
	reserve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reserveCalls.Add(1)
		http.Error(w, "reserve must not be used", http.StatusInternalServerError)
	}))
	t.Cleanup(reserve.Close)

	cfg := config.DefaultConfig()
	cfg.KeyPools = []config.KeyPool{{ID: "orgs", Name: "Organizations", Keys: []config.KeyPoolKey{
		{ID: "first", Label: "First org", Enabled: true, APIKey: firstPoolSecret},
		{ID: "second", Label: "Second org", Enabled: true, APIKey: secondPoolSecret},
		{ID: "third", Label: "Third org", Enabled: true, APIKey: thirdPoolSecret},
		{ID: "fourth", Label: "Fourth org", Enabled: false, APIKey: "sk-ant-api03-disabled-organization-4444"},
	}}}
	cfg.ModelList = []config.ModelConfig{
		{
			ID: "claude", ModelName: "claude", Model: "anthropic/claude-opus-5-5", APIBase: anthropicBase,
			KeyPoolID: "orgs", Fallbacks: []string{"reserve"},
		},
		{ID: "reserve", ModelName: "reserve", Model: "openai/reserve", APIBase: reserve.URL, APIKey: "reserve-key"},
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)
	testModel := func(target, body string) keyPoolTestResult {
		t.Helper()
		rec := serve(mux, http.MethodPost, target, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d %s", target, rec.Code, rec.Body.String())
		}
		assertNoPoolSecret(t, target, rec.Body.String())
		if strings.Contains(rec.Body.String(), thirdPoolSecret) {
			t.Fatalf("%s exposed a Key pool secret: %s", target, rec.Body.String())
		}
		var result keyPoolTestResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	outcomes := func(result keyPoolTestResult) []string {
		got := []string{result.Status}
		for _, key := range result.Keys {
			got = append(got, key.Label+": "+key.Status)
		}
		return got
	}
	probe := func(key string) string {
		return "POST /v1/messages " + key + ` model=claude-opus-5-5 beta="" fallbacks=false`
	}

	gatewayHealth(t, false)
	result := testModel("/api/models/by-id/claude/test", "")
	want := []string{"failed", "First org: success", "Second org: success", "Third org: error"}
	if got := outcomes(result); !slices.Equal(got, want) {
		t.Fatalf("without a gateway every enabled key is tested: %q, want %q", got, want)
	}
	if !strings.Contains(result.Keys[2].Message, "credit") {
		t.Errorf("out-of-credit key message = %q", result.Keys[2].Message)
	}
	if got, want := received(), []string{
		probe(firstPoolSecret), probe(secondPoolSecret), probe(thirdPoolSecret),
	}; !slices.Equal(got, want) {
		t.Fatalf("Anthropic received\n%q\nwant one request per key without fallbacks\n%q", got, want)
	}
	if reserveCalls.Load() != 0 {
		t.Fatalf("Test model used the Routing chain reserve %d times", reserveCalls.Load())
	}

	gatewayHealth(t, true)
	status, err := modelruntime.New(path, func() ([]providers.RoutingAvailability, []modelruntime.KeyPoolStatus) {
		return nil, []modelruntime.KeyPoolStatus{{ID: "orgs", Keys: []providers.KeyAvailability{
			{KeyID: "second", State: providers.KeyExhausted, Until: time.Now().Add(time.Hour)},
		}}}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(status.Close)
	result = testModel("/api/models/test-inline",
		`{"model_name":"unsaved","model":"anthropic/claude-opus-5-5","api_base":"`+anthropicBase+`","key_pool_id":"orgs"}`)
	want = []string{"failed", "First org: success", "Third org: error"}
	if got := outcomes(result); !slices.Equal(got, want) {
		t.Fatalf("an exhausted key is not tested: %q, want %q", got, want)
	}
	if got, want := received(), []string{probe(firstPoolSecret), probe(thirdPoolSecret)}; !slices.Equal(got, want) {
		t.Fatalf("Anthropic received %q, want %q", got, want)
	}
}

func TestSavedPoolBackedModelsReferenceThePoolAndAnswer(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	anthropicBase, _ := fakeAnthropicMessages(t)
	gatewayHealth(t, false)
	path := keyPoolConfigPath(t)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelList = append(cfg.ModelList, config.ModelConfig{
		ID: "own-key", ModelName: "own-key", Model: "anthropic/claude-haiku-5-5", APIBase: anthropicBase,
		APIKey: "sk-ant-api03-own-key-5555",
	})
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandler(path).RegisterRoutes(mux)

	added := serve(mux, http.MethodPost, "/api/models",
		`{"model_name":"opus","model":"anthropic/claude-opus-5-5","api_base":"`+anthropicBase+`","key_pool_id":"orgs"}`)
	if added.Code != http.StatusOK {
		t.Fatalf("adding a pool-backed model = %d %s", added.Code, added.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(added.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	moved := serve(mux, http.MethodPut, "/api/models/by-id/own-key",
		`{"model_name":"own-key","model":"anthropic/claude-haiku-5-5","api_base":"`+anthropicBase+`","key_pool_id":"orgs"}`)
	if moved.Code != http.StatusOK {
		t.Fatalf("moving a single-key model onto the pool = %d %s", moved.Code, moved.Body.String())
	}

	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{created.ID, "own-key"} {
		model, err := loaded.GetModelConfig(config.ModelIDPrefix + id)
		if err != nil {
			t.Fatal(err)
		}
		if model.KeyPoolID != "orgs" || model.APIKey != "" || len(model.APIKeys) != 0 {
			t.Fatalf("saved model %s = pool %q, own keys %q %q", id, model.KeyPoolID, model.APIKey, model.APIKeys)
		}
		rec := serve(mux, http.MethodPost, "/api/models/by-id/"+id+"/test", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"success"`) {
			t.Fatalf("testing saved model %s = %d %s", id, rec.Code, rec.Body.String())
		}
	}
}
