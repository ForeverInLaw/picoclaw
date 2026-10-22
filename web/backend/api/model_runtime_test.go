package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/modelruntime"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestRuntimeStatusReportsActualCapturedModelWithoutRequestData(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") == "Bearer FIRST_SECRET" {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":{"message":"PRIVATE_ERROR with FIRST_SECRET"}}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer SECOND_SECRET" {
			t.Error("wrong account identity")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(
			w,
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"selected\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"PRIVATE_ANSWER\"}]}]}}\n\n",
		)
	}))
	defer endpoint.Close()
	for _, saved := range []struct{ id, token string }{{"first", "FIRST_SECRET"}, {"second", "SECOND_SECRET"}} {
		if err := auth.SaveAccount(
			&auth.AccountConnection{
				ID:         saved.id,
				Label:      "Same label",
				Provider:   "openai",
				Enabled:    true,
				Resource:   endpoint.URL + "/v1",
				Scopes:     auth.OpenAIPlanScope,
				Credential: &auth.AuthCredential{AccessToken: saved.token, Provider: "openai", AuthMethod: "oauth"},
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{ID: "model-first", ModelName: "First", Model: "openai/selected", AuthMethod: "oauth", AccountID: "first"},
			{
				ID:         "model-second",
				ModelName:  "Second",
				Model:      "openai/selected",
				AuthMethod: "oauth",
				AccountID:  "second",
			},
		},
	)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + "model-first"
	cfg.Agents.Defaults.Model = cfg.Agents.Defaults.ModelName
	cfg.Agents.Defaults.ModelFallbacks = []string{config.ModelIDPrefix + "model-second"}
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.Workspace = t.TempDir()
	if writeErr := config.SaveConfig(path, cfg); writeErr != nil {
		t.Fatal(writeErr)
	}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mb := bus.NewMessageBus()
	defer mb.Close()
	loop := agent.NewAgentLoop(cfg, mb, provider, func() (*config.Config, error) { return config.LoadConfig(path) })
	defer loop.Close()
	status, err := loop.EnableRuntimeStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	defer status.Close()
	answer, err := loop.ProcessDirect(t.Context(), "PRIVATE_PROMPT", "PRIVATE_CHAT_ID")
	if err != nil || answer != "PRIVATE_ANSWER" {
		t.Fatalf("request=%q %v", answer, err)
	}
	if writeErr := status.Flush(); writeErr != nil {
		t.Fatal(writeErr)
	}
	previous := gatewayHealthGet
	gatewayHealthGet = func(_ string, _ time.Duration) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"pid":%d,"status":"ok"}`, os.Getpid()))),
		}, nil
	}
	defer func() { gatewayHealthGet = previous }()
	if w := dashboardRequest(h, "GET", "/api/models/runtime", "", nil); w.Code != 401 {
		t.Fatal("runtime status is public")
	}
	w := dashboardRequest(h, "GET", "/api/models/runtime", "", cookie)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	var actual struct {
		State   string `json:"state"`
		Runtime struct {
			Records []struct {
				State    string `json:"state"`
				Selected struct {
					ID        string `json:"id"`
					AccountID string `json:"account_id"`
				} `json:"selected"`
				Chain []struct {
					ID string `json:"id"`
				} `json:"chain"`
				Attempts []struct {
					Reason     string `json:"reason"`
					HTTPStatus int    `json:"http_status"`
				} `json:"attempts"`
			} `json:"records"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.State != "running" || len(actual.Runtime.Records) != 1 {
		t.Fatalf("missing actual runtime: %s", w.Body.String())
	}
	record := actual.Runtime.Records[0]
	if record.State != "completed" || record.Selected.ID != "model-second" || record.Selected.AccountID != "second" ||
		len(record.Chain) != 2 ||
		record.Chain[0].ID != "model-first" {
		t.Fatalf("wrong served/captured identity: %s", w.Body.String())
	}
	found503 := false
	for _, attempt := range record.Attempts {
		if attempt.Reason == "service_unavailable" && attempt.HTTPStatus == 503 {
			found503 = true
		}
	}
	if !found503 {
		t.Fatalf("503 switch reason lost: %s", w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("status sent inference: %d", calls.Load())
	}
	cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + "model-second"
	cfg.Agents.Defaults.Model = cfg.Agents.Defaults.ModelName
	cfg.Agents.Defaults.ModelFallbacks = []string{}
	if writeErr := config.SaveConfig(path, cfg); writeErr != nil {
		t.Fatal(writeErr)
	}
	_, captured := runtimeSnapshot(t, h, cookie, status)
	if captured.Records[0].Chain[0].ID != "model-first" {
		t.Fatal("saved priority edit rewrote served request")
	}
	if _, err := loop.ProcessDirect(t.Context(), "next", "PRIVATE_NEXT_CHAT"); err != nil {
		t.Fatal(err)
	}
	_, captured = runtimeSnapshot(t, h, cookie, status)
	if len(captured.Records) != 2 || captured.Records[0].Chain[0].ID != "model-first" ||
		captured.Records[1].Chain[0].ID != "model-second" ||
		calls.Load() != 3 {
		t.Fatalf("captured priorities=%+v", captured.Records)
	}
	artifact, readErr := os.ReadFile(modelruntime.Path(path))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range []string{"FIRST_SECRET", "SECOND_SECRET", "PRIVATE_ERROR", "PRIVATE_PROMPT", "PRIVATE_ANSWER", "PRIVATE_CHAT_ID", "config_key", "access_token"} {
		if strings.Contains(w.Body.String(), secret) || strings.Contains(string(artifact), secret) {
			t.Fatalf("status leaked %s", secret)
		}
	}
}

func TestRuntimeQuotaRecoversOnlyAfterExplicitSelectedProbe(t *testing.T) {
	previous := gatewayHealthGet
	gatewayHealthGet = func(_ string, _ time.Duration) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"pid":%d,"status":"ok"}`, os.Getpid()))),
		}, nil
	}
	defer func() { gatewayHealthGet = previous }()
	t.Setenv(config.EnvHome, t.TempDir())
	var quota atomic.Bool
	quota.Store(true)
	var firstCalls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selected := r.Header.Get("Authorization") == "Bearer first-token"
		answer := "second"
		if selected {
			firstCalls.Add(1)
			answer = "first"
			if quota.Load() {
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(
			w,
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n",
			answer,
		)
	}))
	defer endpoint.Close()
	for _, id := range []string{"first", "second"} {
		if err := auth.SaveAccount(
			&auth.AccountConnection{
				ID:         id,
				Provider:   "openai",
				Enabled:    true,
				Resource:   endpoint.URL + "/v1",
				Scopes:     auth.OpenAIPlanScope,
				Credential: &auth.AuthCredential{AccessToken: id + "-token", Provider: "openai", AuthMethod: "oauth"},
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{ID: "a", ModelName: "A", Model: "openai/same", AuthMethod: "oauth", AccountID: "first"},
			{ID: "b", ModelName: "B", Model: "openai/same", AuthMethod: "oauth", AccountID: "second"},
		},
	)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + "a"
	cfg.Agents.Defaults.Model = cfg.Agents.Defaults.ModelName
	cfg.Agents.Defaults.ModelFallbacks = []string{config.ModelIDPrefix + "b"}
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.Workspace = t.TempDir()
	if writeErr := config.SaveConfig(path, cfg); writeErr != nil {
		t.Fatal(writeErr)
	}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mb := bus.NewMessageBus()
	defer mb.Close()
	loop := agent.NewAgentLoop(cfg, mb, provider, func() (*config.Config, error) { return config.LoadConfig(path) })
	defer loop.Close()
	status, err := loop.EnableRuntimeStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	defer status.Close()
	// A successful test before the later quota failure is not recovery evidence.
	quota.Store(false)
	probeBefore := dashboardRequest(h, "POST", "/api/oauth/accounts/first/test", `{"model":"same"}`, cookie)
	if probeBefore.Code != 200 {
		t.Fatal(probeBefore.Body.String())
	}
	quota.Store(true)
	answer, err := loop.ProcessDirect(t.Context(), "hello", "quota")
	if err != nil || answer != "second" {
		t.Fatalf("initial=%q %v", answer, err)
	}
	if writeErr := status.Flush(); writeErr != nil {
		t.Fatal(writeErr)
	}
	w := dashboardRequest(h, "GET", "/api/models/runtime", "", cookie)
	if !strings.Contains(w.Body.String(), `"reason":"billing"`) ||
		strings.Contains(w.Body.String(), `"next_eligible_at"`) {
		t.Fatalf("unknown quota invented ETA: %s", w.Body.String())
	}
	quota.Store(false)
	answer, err = loop.ProcessDirect(t.Context(), "again", "quota")
	if err != nil || answer != "second" || firstCalls.Load() != 2 {
		t.Fatal("unknown quota silently retried")
	}
	w = dashboardRequest(h, "POST", "/api/oauth/accounts/first/test", `{"model":"same"}`, cookie)
	if w.Code != 200 {
		t.Fatalf("explicit selected proof=%d %s", w.Code, w.Body.String())
	}
	answer, err = loop.ProcessDirect(t.Context(), "after proof", "quota")
	if err != nil || answer != "first" || firstCalls.Load() != 4 {
		t.Fatalf("real selected success did not recover quota: %q %v first%d", answer, err, firstCalls.Load())
	}
	w = dashboardRequest(h, "PATCH", "/api/oauth/accounts/first", `{"enabled":false}`, cookie)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = dashboardRequest(h, "GET", "/api/models/runtime", "", cookie)
	if !strings.Contains(w.Body.String(), `"reason":"disabled"`) {
		t.Fatal("runtime view missed live disable")
	}
}

type runtimeChildTool struct {
	started  <-chan struct{}
	finished chan error
}

func (*runtimeChildTool) Name() string        { return "start_runtime_child" }
func (*runtimeChildTool) Description() string { return "Start a harmless background task" }
func (*runtimeChildTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (tool *runtimeChildTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	go func() {
		_, err := agent.SpawnSubTurn(
			ctx,
			agent.SubTurnConfig{
				Model:        "b",
				SystemPrompt: "PRIVATE_BACKGROUND_CHILD",
				Critical:     true,
				Timeout:      time.Minute,
			},
		)
		tool.finished <- err
	}()
	select {
	case <-tool.started:
		return tools.SilentResult("CHILD_STARTED")
	case <-ctx.Done():
		return tools.ErrorResult("canceled")
	}
}

func TestRuntimeLateSubturnCannotReplaceCompletedParent(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &request)
		child := strings.Contains(string(body), "PRIVATE_BACKGROUND_CHILD")
		if child && request.Model == "b" {
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":{"message":"unavailable"}}`)
			return
		}
		if !child && !strings.Contains(string(body), "CHILD_STARTED") {
			_, _ = io.WriteString(
				w,
				`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"spawn","type":"function","function":{"name":"start_runtime_child","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
			)
			return
		}
		_, _ = fmt.Fprintf(
			w,
			`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`,
			request.Model,
		)
	}))
	defer func() { releaseOnce.Do(func() { close(release) }); endpoint.Close() }()
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{ID: "b", ModelName: "B", Model: "openai/b", APIBase: endpoint.URL},
			{ID: "c", ModelName: "C", Model: "openai/c", APIBase: endpoint.URL},
		},
	)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + "b"
	cfg.Agents.Defaults.Model = cfg.Agents.Defaults.ModelName
	cfg.Agents.Defaults.ModelFallbacks = []string{config.ModelIDPrefix + "c"}
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.Workspace = t.TempDir()
	if writeErr := config.SaveConfig(path, cfg); writeErr != nil {
		t.Fatal(writeErr)
	}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mb := bus.NewMessageBus()
	defer mb.Close()
	loop := agent.NewAgentLoop(cfg, mb, provider, func() (*config.Config, error) { return config.LoadConfig(path) })
	defer loop.Close()
	status, err := loop.EnableRuntimeStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	defer status.Close()
	tool := &runtimeChildTool{started: started, finished: make(chan error, 1)}
	loop.RegisterTool(tool)
	answer, err := loop.ProcessDirect(t.Context(), "parent task", "PRIVATE_PARENT_CHAT")
	if err != nil || answer != "b" {
		t.Fatalf("parent=%q %v", answer, err)
	}
	read := func() modelruntime.Snapshot {
		t.Helper()
		if writeErr := status.Flush(); writeErr != nil {
			t.Fatal(writeErr)
		}
		w := dashboardRequest(h, "GET", "/api/models/runtime", "", cookie)
		var result struct {
			Runtime modelruntime.Snapshot `json:"runtime"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Runtime
	}
	before := read()
	if len(before.Records) != 2 || before.Records[0].State != "completed" || before.Records[0].Selected.ID != "b" ||
		before.Records[1].State != "running" {
		t.Fatalf("parent/child attribution wrong: %+v", before.Records)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-tool.finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not finish")
	}
	after := read()
	if len(after.Records) != 2 || after.Records[0].State != "completed" || after.Records[0].Selected.ID != "b" ||
		after.Records[1].Selected.ID != "c" ||
		after.Records[1].ParentID != after.Records[0].ID {
		t.Fatalf("late child rewrote parent identity: %+v", after.Records)
	}
}

func runtimeHTTPFixture(
	t *testing.T,
	serve http.HandlerFunc,
	configure ...func(*config.Config),
) (http.Handler, *http.Cookie, *agent.AgentLoop, *modelruntime.Collector) {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	endpoint := httptest.NewServer(serve)
	t.Cleanup(endpoint.Close)
	h, cookie, path := modelInferenceDashboard(
		t,
		[]config.ModelConfig{
			{ID: "a", ModelName: "A", Model: "openai/a", APIBase: endpoint.URL, InactivityTimeoutSeconds: 1},
			{ID: "b", ModelName: "B", Model: "openai/b", APIBase: endpoint.URL},
		},
	)
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.ModelName = config.ModelIDPrefix + "a"
	cfg.Agents.Defaults.Model = cfg.Agents.Defaults.ModelName
	cfg.Agents.Defaults.ModelFallbacks = []string{config.ModelIDPrefix + "b"}
	cfg.Agents.Defaults.ContextManager = "legacy"
	cfg.Agents.Defaults.Workspace = t.TempDir()
	for _, apply := range configure {
		apply(cfg)
	}
	if writeErr := config.SaveConfig(path, cfg); writeErr != nil {
		t.Fatal(writeErr)
	}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mb := bus.NewMessageBus()
	t.Cleanup(mb.Close)
	loop := agent.NewAgentLoop(cfg, mb, provider, func() (*config.Config, error) { return config.LoadConfig(path) })
	t.Cleanup(loop.Close)
	status, err := loop.EnableRuntimeStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(status.Close)
	return h, cookie, loop, status
}

func runtimeSnapshot(
	t *testing.T,
	h http.Handler,
	cookie *http.Cookie,
	status *modelruntime.Collector,
) (string, modelruntime.Snapshot) {
	t.Helper()
	if writeErr := status.Flush(); writeErr != nil {
		t.Fatal(writeErr)
	}
	w := dashboardRequest(h, "GET", "/api/models/runtime", "", cookie)
	if w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	var response struct {
		State   string                `json:"state"`
		Runtime modelruntime.Snapshot `json:"runtime"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.State, response.Runtime
}

func TestRuntimeShowsInactivityAndCancelableCapacityWait(t *testing.T) {
	for _, mode := range []string{"inactivity", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			h, cookie, loop, status := runtimeHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var input struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&input)
				if mode == "capacity" {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"message":"PRIVATE_CAPACITY_ERROR"}}`)
					return
				}
				if input.Model == "a" {
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"backup"},"finish_reason":"stop"}]}`)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if mode == "inactivity" {
				answer, err := loop.ProcessDirect(ctx, "PRIVATE_PROMPT", "PRIVATE_SCOPE")
				if err != nil || answer != "backup" || ctx.Err() != nil {
					t.Fatalf("inactivity=%q %v", answer, err)
				}
				_, snapshot := runtimeSnapshot(t, h, cookie, status)
				record := snapshot.Records[0]
				found := false
				for _, attempt := range record.Attempts {
					if attempt.Reason == "inactivity_timeout" && attempt.InactivityTimeoutMS == 1000 {
						found = true
					}
				}
				if !found || record.Selected.ID != "b" || record.State != "completed" {
					t.Fatalf("wrong inactivity outcome: %+v", record)
				}
			} else {
				done := make(chan error, 1)
				go func() { _, err := loop.ProcessDirect(ctx, "PRIVATE_PROMPT", "PRIVATE_SCOPE"); done <- err }()
				waiting := false
				for i := 0; i < 100; i++ {
					_, snapshot := runtimeSnapshot(t, h, cookie, status)
					if len(snapshot.Records) > 0 && snapshot.Records[0].State == "waiting" {
						waiting = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !waiting {
					t.Fatal("503 wait not observable")
				}
				cancel()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("cancel succeeded as answer")
					}
				case <-time.After(time.Second):
					t.Fatal("503 cancellation hung")
				}
				_, snapshot := runtimeSnapshot(t, h, cookie, status)
				record := snapshot.Records[0]
				knownWait := false
				for _, attempt := range record.Attempts {
					if attempt.Phase == "waiting" && !attempt.NextEligibleAt.IsZero() {
						knownWait = true
					}
				}
				if record.State != "canceled" || !knownWait || calls.Load() != 2 {
					t.Fatalf("wrong canceled capacity state: %+v calls%d", record, calls.Load())
				}
			}
		})
	}
}

func TestRuntimeHistoryIsBoundedAndMarkedOfflineOrStale(t *testing.T) {
	h, cookie, loop, status := runtimeHTTPFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}]}`)
	})
	for i := 0; i < modelruntime.MaxRecords+2; i++ {
		if _, err := loop.ProcessDirect(t.Context(), "hello", "PRIVATE_HISTORY_SCOPE"); err != nil {
			t.Fatal(err)
		}
	}
	previous := gatewayHealthGet
	gatewayHealthGet = func(_ string, _ time.Duration) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"pid":%d,"status":"ok"}`, os.Getpid()+1))),
		}, nil
	}
	defer func() { gatewayHealthGet = previous }()
	state, snapshot := runtimeSnapshot(t, h, cookie, status)
	if state != "stale" || len(snapshot.Records) != modelruntime.MaxRecords || snapshot.DroppedRecords < 2 {
		t.Fatalf("retention/stale state=%s count%d dropped%d", state, len(snapshot.Records), snapshot.DroppedRecords)
	}
	gatewayHealthGet = func(_ string, _ time.Duration) (*http.Response, error) {
		return nil, errors.New("PRIVATE_HEALTH_ERROR")
	}
	state, snapshot = runtimeSnapshot(t, h, cookie, status)
	if state != "offline" || len(snapshot.Records) != modelruntime.MaxRecords ||
		snapshot.Availability[0].Reason != "runtime_unknown" {
		t.Fatalf("offline state=%s", state)
	}
}

func TestRuntimeLateBackgroundContextHasItsOwnOutcome(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	h, cookie, loop, status := runtimeHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Model    string              `json:"model"`
			Messages []providers.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		contextCall := len(input.Messages) == 1 && input.Messages[0].Role == "user"
		if contextCall && input.Model == "a" {
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":{"message":"PRIVATE_SUMMARY_ERROR"}}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, input.Model)
	}, func(cfg *config.Config) { cfg.Agents.Defaults.SummarizeMessageThreshold = 6 })
	for i := 0; i < 4; i++ {
		if _, err := loop.ProcessDirect(t.Context(), "PRIVATE_CONTEXT_PROMPT", "PRIVATE_CONTEXT_SCOPE"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background context request did not start")
	}
	_, before := runtimeSnapshot(t, h, cookie, status)
	if len(before.Records) != 5 || before.Records[3].State != "completed" || before.Records[3].Selected.ID != "a" ||
		before.Records[4].Kind != "context" ||
		before.Records[4].ParentID != before.Records[3].ID {
		t.Fatalf("context not isolated: %+v", before.Records)
	}
	releaseOnce.Do(func() { close(release) })
	var after modelruntime.Snapshot
	finished := false
	for i := 0; i < 100; i++ {
		_, after = runtimeSnapshot(t, h, cookie, status)
		if after.Records[4].State == "completed" {
			finished = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !finished || after.Records[3].Selected.ID != "a" || after.Records[3].State != "completed" ||
		after.Records[4].Selected.ID != "b" {
		t.Fatalf("late context overwrote parent: %+v", after.Records)
	}
}

func TestOAuthMachineFailuresSelectReserveWithoutLosingCredentials(t *testing.T) {
	for _, code := range []string{"invalid_client", "temporarily_unavailable", "server_error", "invalid_request"} {
		t.Run(code, func(t *testing.T) {
			var tokenCalls atomic.Int32
			tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				tokenCalls.Add(1)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"error":%q,"error_description":"PRIVATE_PROVIDER_DESCRIPTION"}`, code)
			}))
			defer tokens.Close()
			var backupCalls atomic.Int32
			h, cookie, loop, status := runtimeHTTPFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				backupCalls.Add(1)
				_, _ = io.WriteString(
					w,
					`{"choices":[{"message":{"content":"reserve answered"},"finish_reason":"stop"}]}`,
				)
			}, func(cfg *config.Config) {
				if err := auth.SaveAccount(&auth.AccountConnection{
					ID:       "oauth-first",
					Provider: "openai",
					Enabled:  true,
					OAuth:    auth.OAuthProviderConfig{TokenURL: tokens.URL, ClientID: "fixture-client"},
					Credential: &auth.AuthCredential{
						AccessToken:  "expired",
						RefreshToken: "retained-refresh",
						AuthMethod:   "oauth",
						ExpiresAt:    time.Now().Add(-time.Hour),
					},
				}); err != nil {
					t.Fatal(err)
				}
				cfg.ModelList[0].AuthMethod = "oauth"
				cfg.ModelList[0].AccountID = "oauth-first"
				if code == "invalid_client" {
					alias := cfg.ModelList[0]
					alias.ID, alias.ModelName = "same-account-alias", "Alias"
					cfg.ModelList = append(cfg.ModelList, alias)
					cfg.Agents.Defaults.ModelFallbacks = []string{
						config.ModelIDPrefix + alias.ID,
						config.ModelIDPrefix + "b",
					}
				}
			})
			answer, err := loop.ProcessDirect(t.Context(), "use reserve", "oauth-code")
			if code == "invalid_request" {
				if err == nil || backupCalls.Load() != 0 {
					t.Fatalf("invalid request retried: %q %v calls=%d", answer, err, backupCalls.Load())
				}
			} else {
				if err != nil || answer != "reserve answered" || backupCalls.Load() != 1 {
					t.Fatalf("code=%s answer=%q err=%v calls=%d", code, answer, err, backupCalls.Load())
				}
			}
			if code == "invalid_client" && tokenCalls.Load() != 1 {
				t.Fatalf("same-account alias repeated rejected client refresh: %d", tokenCalls.Load())
			}
			saved, err := auth.GetAccount("oauth-first")
			if err != nil || saved.Credential == nil || saved.Credential.RefreshToken != "retained-refresh" {
				t.Fatalf("client/service error erased session: %+v %v", saved, err)
			}
			_, snapshot := runtimeSnapshot(t, h, cookie, status)
			if code == "invalid_client" {
				for _, item := range snapshot.Availability {
					if item.Candidate.AccountID == "oauth-first" && item.Authorization != "authorized" {
						t.Fatalf("invalid client falsely revoked saved authorization: %+v", item)
					}
				}
				classified := false
				for _, attempt := range snapshot.Records[0].Attempts {
					classified = classified ||
						attempt.Candidate.AccountID == "oauth-first" && attempt.Reason == "client_configuration"
				}
				if !classified {
					t.Fatal("client registration failure lost its distinct runtime reason")
				}
			}
			serialized, _ := json.Marshal(snapshot)
			if strings.Contains(string(serialized), "PRIVATE_PROVIDER_DESCRIPTION") ||
				strings.Contains(string(serialized), "retained-refresh") {
				t.Fatal("runtime exposed token failure secrets")
			}
		})
	}
}

func TestRuntimeLegacyCodexReportsServedWireModel(t *testing.T) {
	h, cookie, loop, status := runtimeHTTPFixture(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("legacy account must retain its existing endpoint")
	}, func(cfg *config.Config) {
		if err := auth.SaveAccount(&auth.AccountConnection{
			ID: config.LegacyOpenAIAccountID, Provider: "openai", Enabled: true,
			Credential: &auth.AuthCredential{AccessToken: "legacy-token", AuthMethod: "oauth"},
		}); err != nil {
			t.Fatal(err)
		}
		cfg.ModelList[0].Model = "openai/o1"
		cfg.ModelList[0].AuthMethod = "oauth"
		cfg.ModelList[0].AccountID = config.LegacyOpenAIAccountID
	})
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	var wireModel string
	http.DefaultTransport = accountRecordingTransport{handler: func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "chatgpt.com" {
			return original.RoundTrip(r)
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		wireModel = body.Model
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body: io.NopCloser(
				strings.NewReader(
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"legacy\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"legacy answered\"}]}]}}\n\n",
				),
			),
		}, nil
	}}
	answer, err := loop.ProcessDirect(t.Context(), "hello", "legacy-wire")
	if err != nil || answer != "legacy answered" || wireModel != "gpt-5.3-codex" {
		t.Fatalf("answer=%q err=%v wire=%q", answer, err, wireModel)
	}
	_, snapshot := runtimeSnapshot(t, h, cookie, status)
	record := snapshot.Records[0]
	if record.Selected == nil || record.Selected.ID != "a" ||
		record.Selected.AccountID != config.LegacyOpenAIAccountID ||
		record.Selected.Model != wireModel ||
		record.Chain[0].Model != "o1" {
		t.Fatalf("actual identity must retain configured choice and captured chain: %+v", record)
	}
}
