package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/channels/pico"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestWebImageUploadReachesCurrentModelWithText(t *testing.T) {
	for _, rejectVision := range []bool{false, true} {
		t.Run(map[bool]string{false: "compatible", true: "unsupported-vision"}[rejectVision], func(t *testing.T) {
			runWebImageScenario(t, rejectVision, "pico")
		})
	}
	t.Run("named-pico", func(t *testing.T) { runWebImageScenario(t, false, "dashboard-images") })
}

func runWebImageScenario(t *testing.T, rejectVision bool, channelName string) {
	al, cfg, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	defer mb.Close()
	cfg.Channels.Pico = config.PicoConfig{Enabled: true, Token: "image-token"}
	if channelName != "pico" {
		cfg.Channels.Pico.Enabled = false
		cfg.ChannelList = config.ChannelList{
			channelName: &config.Channel{
				Enabled:  true,
				Type:     "pico",
				Settings: json.RawMessage(`{"token":"image-token"}`),
			},
		}
	}
	store := media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{MaxAge: time.Nanosecond})
	defer store.CleanExpired()
	al.SetMediaStore(store)
	manager, err := channels.NewManager(cfg, mb, store)
	if err != nil {
		t.Fatal(err)
	}
	al.SetChannelManager(manager)
	if err = manager.StartAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer manager.StopAll(t.Context())
	channel, _ := manager.GetChannel(channelName)
	gateway := httptest.NewServer(channel.(*pico.PicoChannel))
	defer gateway.Close()
	image, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a0XQAAAAASUVORK5CYII=",
	)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "pixel.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(image); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		gateway.URL+"/pico/uploads?session_id=images",
		&body,
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer image-token")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		response, _ := io.ReadAll(res.Body)
		t.Fatalf("upload=%d %s", res.StatusCode, response)
	}
	var uploaded struct {
		Ref string `json:"ref"`
	}
	if err = json.NewDecoder(res.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseAll(channels.BuildMediaScope(channelName, "pico:images", "image-message"))
	requests := make(chan map[string]any, 4)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
			t.Error(decodeErr)
			return
		}
		requests <- payload
		if rejectVision {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"This model does not support image inputs"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(
			w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"Image received\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		)
	}))
	defer modelServer.Close()
	agent := al.GetRegistry().GetDefaultAgent()
	agent.Provider = providers.NewHTTPProvider("fixture-key", modelServer.URL, "")
	agent.ImageProvider = &mockProvider{}
	agent.ImageModel = "different-image-model"
	agent.ImageCandidates = []providers.FallbackCandidate{{Provider: "openai", Model: "different-image-model"}}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- al.Run(ctx) }()
	defer func() { cancel(); <-done }()
	conn, handshake, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(gateway.URL, "http")+"/pico/ws?session_id=images",
		http.Header{"Authorization": []string{"Bearer image-token"}},
	)
	if handshake != nil {
		defer handshake.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteJSON(
		pico.PicoMessage{
			Type:    pico.TypeMessageSend,
			ID:      "image-message",
			Payload: map[string]any{"content": "Describe this pixel", "media": []string{uploaded.Ref}},
		},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-requests:
		if payload["model"] != "test-model" {
			t.Fatalf("current model lost: %#v", payload["model"])
		}
		serialized, _ := json.Marshal(payload["messages"])
		if !strings.Contains(string(serialized), "Describe this pixel") ||
			!strings.Contains(string(serialized), "data:image/png;base64,"+base64.StdEncoding.EncodeToString(image)) {
			t.Fatalf("text/image missing: %s", serialized)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("uploaded image did not reach selected HTTP model")
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var frame pico.PicoMessage
		if err = conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if frame.Payload["final"] == true {
			content, _ := frame.Payload["content"].(string)
			if rejectVision {
				if !strings.Contains(strings.ToLower(content), "provider rejected") ||
					!strings.Contains(strings.ToLower(content), "image") {
					t.Fatalf("unsupported vision guidance=%q", content)
				}
			} else if content != "Image received" {
				t.Fatalf("final=%#v", frame)
			}
			break
		}
	}
	select {
	case <-requests:
		t.Fatal("image caused another provider operation")
	default:
	}
}
