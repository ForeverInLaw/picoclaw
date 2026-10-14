package pico

import (
	"bytes"
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

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

//nolint:bodyclose // HTTP helpers register every response with t.Cleanup.
func TestImageUploadOwnershipLimitsAndDraftRemoval(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()
	c, err := NewPicoChannel(config.PicoConfig{Token: "upload-token", AllowedImageTypes: []string{"image/png"}}, mb)
	if err != nil {
		t.Fatal(err)
	}
	store := media.NewFileMediaStore()
	c.SetMediaStore(store)
	c.SetMaxMediaSize(256)
	if err = c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(t.Context())
	server := httptest.NewServer(c)
	defer server.Close()
	png, _ := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a0XQAAAAASUVORK5CYII=",
	)
	upload := func(data []byte, token string) *http.Response {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, createErr := writer.CreateFormFile("file", "test.png")
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, _ = part.Write(data)
		_ = writer.Close()
		req, _ := http.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			server.URL+"/pico/uploads?session_id=owner",
			&body,
		)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}
	if res := upload(png, ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous upload=%d", res.StatusCode)
	}
	if res := upload(bytes.Repeat(png, 10), "upload-token"); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("size limit=%d", res.StatusCode)
	}
	if res := upload(
		[]byte("<svg><script>alert(1)</script></svg>"),
		"upload-token",
	); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("type limit=%d", res.StatusCode)
	}
	res := upload(png, "upload-token")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("upload=%d", res.StatusCode)
	}
	var attachment struct {
		Ref string `json:"ref"`
		URL string `json:"url"`
	}
	if err = json.NewDecoder(res.Body).Decode(&attachment); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		result, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		t.Cleanup(func() { _ = result.Body.Close() })
		return result
	}
	if res = request(http.MethodGet, attachment.URL, ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous media=%d", res.StatusCode)
	}
	if res = request(
		http.MethodGet,
		strings.ReplaceAll(attachment.URL, "owner", "other"),
		"upload-token",
	); res.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign media=%d", res.StatusCode)
	}
	res = request(http.MethodGet, attachment.URL, "upload-token")
	downloaded, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !bytes.Equal(png, downloaded) {
		t.Fatalf("media roundtrip=%d", res.StatusCode)
	}
	conn, handshake, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http")+"/pico/ws?session_id=other",
		http.Header{"Authorization": []string{"Bearer upload-token"}},
	)
	if handshake != nil {
		defer handshake.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteJSON(
		PicoMessage{
			Type:    TypeMessageSend,
			ID:      "foreign",
			Payload: map[string]any{"content": "Use foreign image", "media": []string{attachment.Ref}},
		},
	); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var frame PicoMessage
	if err = conn.ReadJSON(&frame); err != nil || frame.Type != TypeError {
		t.Fatalf("foreign attachment error=%#v err=%v", frame, err)
	}
	select {
	case <-mb.InboundChan():
		t.Fatal("foreign image reached agent")
	default:
	}
	if res = request(http.MethodDelete, attachment.URL, "upload-token"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("remove=%d", res.StatusCode)
	}
	if res = request(http.MethodGet, attachment.URL, "upload-token"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("removed media=%d", res.StatusCode)
	}
}
