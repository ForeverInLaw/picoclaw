package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// modelCaps is the part of a Models API entry that decides the thinking mapping.
type modelCaps struct {
	adaptive bool
	disabled bool
	efforts  []string // supported effort levels; none means effort is unsupported
}

var (
	// Opus 5.5 always thinks: disabling is rejected.
	opus55Caps = modelCaps{adaptive: true, efforts: []string{"low", "medium", "high", "xhigh", "max"}}
	// Sonnet 4.6 accepts disabled thinking and has no xhigh effort.
	sonnet46Caps = modelCaps{adaptive: true, disabled: true, efforts: []string{"low", "medium", "high", "max"}}
	// Haiku 4.5 has neither adaptive thinking nor effort.
	haiku45Caps = modelCaps{disabled: true}
)

func (c modelCaps) json(id string) string {
	support := func(ok bool) string { return fmt.Sprintf(`{"supported":%t}`, ok) }
	efforts := make([]string, 0, 5)
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		efforts = append(efforts, fmt.Sprintf("%q:%s", level, support(slices.Contains(c.efforts, level))))
	}
	return fmt.Sprintf(`{"type":"model","id":%q,"display_name":%q,"created_at":"2026-01-01T00:00:00Z",
		"capabilities":{
			"effort":{"supported":%t,%s},
			"thinking":{"supported":%t,"types":{"adaptive":%s,"between_tools":%s,"disabled":%s,"enabled":%s}}}}`,
		id, id, len(c.efforts) > 0, strings.Join(efforts, ","),
		c.adaptive, support(c.adaptive), support(false), support(c.disabled), support(false))
}

// fakeAnthropicModels plays the Messages API and the Models API. A model
// missing from caps makes its capability lookup fail with a 500.
type fakeAnthropicModels struct {
	*httptest.Server
	mu         sync.Mutex
	caps       map[string]modelCaps
	lookups    map[string]int
	lookupAuth []string
	bodies     []map[string]any
}

func newFakeAnthropicModels(t *testing.T, caps map[string]modelCaps) *fakeAnthropicModels {
	t.Helper()
	fake := &fakeAnthropicModels{caps: caps, lookups: map[string]int{}}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if id, ok := strings.CutPrefix(r.URL.Path, "/v1/models/"); ok {
			fake.lookups[id]++
			fake.lookupAuth = append(fake.lookupAuth, r.Header.Get("X-Api-Key")+r.Header.Get("Authorization"))
			c, known := fake.caps[id]
			if !known {
				http.Error(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`,
					http.StatusInternalServerError)
				return
			}
			writeJSON(w, c.json(id))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		fake.bodies = append(fake.bodies, body)
		writeJSON(w, `{"id":"msg_t","type":"message","role":"assistant","model":"m","stop_reason":"end_turn",
			"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	t.Cleanup(fake.Close)
	return fake
}

func (f *fakeAnthropicModels) lastBody(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("fake Anthropic received no Messages request")
	}
	return f.bodies[len(f.bodies)-1]
}

func (f *fakeAnthropicModels) lookupCount(model string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookups[model]
}

// chatWithThinking sends one answer request for model with the given thinking_level.
func chatWithThinking(t *testing.T, provider LLMProvider, model, level string) {
	t.Helper()
	_, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "Hi"}}, nil, model,
		map[string]any{"max_tokens": 1024, "thinking_level": level})
	if err != nil {
		t.Fatalf("Chat(thinking_level=%q) error = %v", level, err)
	}
}

func anthropicModelProvider(t *testing.T, apiBase, model string) LLMProvider {
	t.Helper()
	mc := anthropicAPIKeyModel(apiBase)
	mc.Model = "anthropic/" + model
	provider, _, err := CreateProviderFromConfig(mc)
	if err != nil {
		t.Fatalf("CreateProviderFromConfig() error = %v", err)
	}
	return provider
}

func TestAnthropicAPIKeyModelAcceptsThinkingLevel(t *testing.T) {
	provider := anthropicModelProvider(t, "http://127.0.0.1:0", "claude-opus-5-5")
	if capable, ok := provider.(ThinkingCapable); !ok || !capable.SupportsThinking() {
		t.Fatalf("provider %T does not report thinking support, so the agent never sends thinking_level", provider)
	}
}

func TestAnthropicThinkingLevelMapsToAdaptiveThinkingWithEffort(t *testing.T) {
	tests := []struct {
		model, level     string
		thinking, effort string // expected JSON; "" means the field is absent
	}{
		{"claude-opus-5-5", "low", `{"type":"adaptive"}`, "low"},
		{"claude-opus-5-5", "medium", `{"type":"adaptive"}`, "medium"},
		{"claude-opus-5-5", "high", `{"type":"adaptive"}`, "high"},
		{"claude-opus-5-5", "xhigh", `{"type":"adaptive"}`, "xhigh"},
		{"claude-opus-5-5", "max", `{"type":"adaptive"}`, "max"},
		{"claude-opus-5-5", "adaptive", `{"type":"adaptive"}`, "high"},
		// off: disabled where the model allows it, the lowest effort elsewhere.
		{"claude-opus-5-5", "off", `{"type":"adaptive"}`, "low"},
		{"claude-sonnet-4-6", "off", `{"type":"disabled"}`, ""},
		// An effort the model lacks moves to the nearest one it has.
		{"claude-sonnet-4-6", "xhigh", `{"type":"adaptive"}`, "high"},
		// Without adaptive thinking or effort, neither is sent.
		{"claude-haiku-4-5", "high", "", ""},
	}
	fake := newFakeAnthropicModels(t, map[string]modelCaps{
		"claude-opus-5-5":   opus55Caps,
		"claude-sonnet-4-6": sonnet46Caps,
		"claude-haiku-4-5":  haiku45Caps,
	})
	for _, tt := range tests {
		t.Run(tt.model+"/"+tt.level, func(t *testing.T) {
			chatWithThinking(t, anthropicModelProvider(t, fake.URL, tt.model), tt.model, tt.level)

			body := fake.lastBody(t)
			assertOptionalJSON(t, "thinking", body["thinking"], tt.thinking)
			wantOutput := ""
			if tt.effort != "" {
				wantOutput = fmt.Sprintf(`{"effort":%q}`, tt.effort)
			}
			assertOptionalJSON(t, "output_config", body["output_config"], wantOutput)
			if raw, _ := json.Marshal(body); strings.Contains(string(raw), "budget_tokens") {
				t.Errorf("request carried budget_tokens: %s", raw)
			}
		})
	}
}

// assertOptionalJSON checks a request field against want, where "" means absent.
func assertOptionalJSON(t *testing.T, field string, got any, want string) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Errorf("%s = %v, want absent", field, got)
		}
		return
	}
	assertJSON(t, field, got, want)
}

func TestAnthropicCapabilitiesAreLookedUpOncePerModel(t *testing.T) {
	fake := newFakeAnthropicModels(t, map[string]modelCaps{"claude-opus-5-5": opus55Caps})

	// Two providers stand for separate configured models or a rebuilt provider.
	for range 2 {
		provider := anthropicModelProvider(t, fake.URL, "claude-opus-5-5")
		for _, level := range []string{"off", "high", "max"} {
			chatWithThinking(t, provider, "claude-opus-5-5", level)
		}
	}

	if got := fake.lookupCount("claude-opus-5-5"); got != 1 {
		t.Errorf("capability lookups = %d, want 1", got)
	}
	if got := fake.lookupAuth[0]; got != "sk-ant-test" {
		t.Errorf("capability lookup credentials = %q, want the model's API key", got)
	}
}

func TestAnthropicCapabilityLookupFailureFallsBackToLowestEffort(t *testing.T) {
	fake := newFakeAnthropicModels(t, nil) // every lookup fails
	provider := anthropicModelProvider(t, fake.URL, "claude-opus-5-5")

	chatWithThinking(t, provider, "claude-opus-5-5", "off")
	body := fake.lastBody(t)
	assertJSON(t, "thinking", body["thinking"], `{"type":"adaptive"}`)
	assertJSON(t, "output_config", body["output_config"], `{"effort":"low"}`)

	chatWithThinking(t, provider, "claude-opus-5-5", "max")
	assertJSON(t, "output_config", fake.lastBody(t)["output_config"], `{"effort":"max"}`)

	if got := fake.lookupCount("claude-opus-5-5"); got != 1 {
		t.Errorf("capability lookups = %d, want 1 (a failure is not retried per answer)", got)
	}
}
